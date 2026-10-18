// Copyright 2026 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package repo

import (
	"context"
	"fmt"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PipelineQuery defines query parameters for listing pipelines.
type PipelineQuery struct {
	ProjectID string
	Name      string
	Status    int
	Page      int
	PageSize  int
}

// PipelineRunQuery defines query parameters for listing pipeline runs.
type PipelineRunQuery struct {
	PipelineID string
	Status     int
	Page       int
	PageSize   int
}

// IPipelineRepository defines persistence methods for pipeline and pipeline run.
type IPipelineRepository interface {
	Create(ctx context.Context, pipeline *model.Pipeline) error
	Update(ctx context.Context, pipelineID string, updates map[string]any) error
	Get(ctx context.Context, pipelineID string) (*model.Pipeline, error)
	List(ctx context.Context, query *PipelineQuery) ([]*model.Pipeline, int64, error)
	CreateRun(ctx context.Context, run *model.PipelineRun) error
	GetRun(ctx context.Context, runID string) (*model.PipelineRun, error)
	UpdateRun(ctx context.Context, runID string, updates map[string]any) error
	GetRunByRequestID(ctx context.Context, pipelineID, requestID string) (*model.PipelineRun, error)
	ListRuns(ctx context.Context, query *PipelineRunQuery) ([]*model.PipelineRun, int64, error)
}

type PipelineRepo struct {
	database.IDatabase
}

// NewPipelineRepo creates pipeline repository.
func NewPipelineRepo(db database.IDatabase) IPipelineRepository {
	return &PipelineRepo{IDatabase: db}
}

// Create creates a pipeline.
func (r *PipelineRepo) Create(ctx context.Context, pipeline *model.Pipeline) error {
	if pipeline == nil {
		return gorm.ErrInvalidData
	}
	if err := normalizeJSONFields(map[string]*string{"metadata": &pipeline.Metadata}, pipelineJSONDefaults); err != nil {
		return err
	}
	return r.Database().WithContext(ctx).Create(pipeline).Error
}

// Update updates a pipeline by pipelineID.
func (r *PipelineRepo) Update(ctx context.Context, pipelineID string, updates map[string]any) error {
	var err error
	updates, err = normalizeJSONUpdates(updates, pipelineJSONDefaults)
	if err != nil {
		return err
	}
	return updatePipelineWithPollingInvalidation(ctx, r.Database(), pipelineID, updates)
}

// Get returns pipeline by pipelineID.
func (r *PipelineRepo) Get(ctx context.Context, pipelineID string) (*model.Pipeline, error) {
	var one model.Pipeline
	if err := r.Database().WithContext(ctx).
		Where("pipeline_id = ? AND is_deleted = ?", pipelineID, 0).
		First(&one).Error; err != nil {
		return nil, err
	}
	return &one, nil
}

// List returns pipeline list and total by query.
func (r *PipelineRepo) List(ctx context.Context, query *PipelineQuery) ([]*model.Pipeline, int64, error) {
	if query == nil {
		query = &PipelineQuery{}
	}
	if query.Page <= 0 {
		query.Page = 1
	}
	if query.PageSize <= 0 {
		query.PageSize = 20
	}
	if query.PageSize > 100 {
		query.PageSize = 100
	}

	tx := r.Database().WithContext(ctx).Model(&model.Pipeline{}).Where("is_deleted = ?", 0)
	if query.ProjectID != "" {
		tx = tx.Where("project_id = ?", query.ProjectID)
	}
	if strings.TrimSpace(query.Name) != "" {
		tx = tx.Where("name LIKE ?", "%"+strings.TrimSpace(query.Name)+"%")
	}
	if query.Status > 0 {
		tx = tx.Where("status = ?", query.Status)
	}

	total, err := Count(tx)
	if err != nil {
		return nil, 0, err
	}

	var list []*model.Pipeline
	err = tx.Order("created_at DESC").
		Offset((query.Page - 1) * query.PageSize).
		Limit(query.PageSize).
		Find(&list).Error
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// CreateRun creates a pipeline run.
func (r *PipelineRepo) CreateRun(ctx context.Context, run *model.PipelineRun) error {
	if run == nil || !emptyRunInboxBinding(run) || run.ProjectID != "" || run.TriggeredBy == "polling" || run.PollingContext != "" || run.PollingContextDigest != "" || run.RetryApprovalContext != "" || run.RetryApprovalContextDigest != "" {
		return gorm.ErrInvalidData
	}
	if err := normalizeJSONFields(map[string]*string{"env": &run.Env}, pipelineRunJSONDefaults); err != nil {
		return err
	}
	return r.Database().WithContext(ctx).Create(run).Error
}

var pipelineRunReadColumns = []string{"id", "created_at", "updated_at", "run_id", "pipeline_id", "project_id", "inbox_actor_id", "inbox_origin", "request_id", "pipeline_name", "branch", "commit_sha", "definition_commit_sha", "allow_deployments", "deployment_actor_id", "deployment_plan", "deployment_target_fingerprint", "deployment_plan_hash", "definition_path", "status", "trigger_type", "triggered_by", "env", "total_jobs", "completed_jobs", "failed_jobs", "running_jobs", "current_stage", "total_stages", "start_time", "end_time", "duration", "retry_approval_context", "retry_approval_context_digest", "polling_context", "polling_context_digest"}

// GetRun 通过运行业务标识读取完整运行与不可变证据
func (r *PipelineRepo) GetRun(ctx context.Context, runID string) (*model.PipelineRun, error) {
	var rows []model.PipelineRun
	if err := r.Database().WithContext(ctx).Clauses(database.Write()).
		Select(pipelineRunReadColumns).Where("run_id = ?", runID).
		Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if len(rows) != 1 || rows[0].RunID != runID {
		return nil, ErrManualTriggerConflict
	}
	return &rows[0], nil
}

// UpdateRun updates a pipeline run by runID.
func (r *PipelineRepo) UpdateRun(ctx context.Context, runID string, updates map[string]any) error {
	if err := rejectRunProjectUpdates(updates); err != nil {
		return err
	}
	if err := rejectPollingRunUpdates(updates); err != nil {
		return err
	}
	if err := rejectRetryApprovalRunUpdates(updates); err != nil {
		return err
	}
	var err error
	updates, err = normalizeJSONUpdates(updates, pipelineRunJSONDefaults)
	if err != nil {
		return err
	}
	return r.Database().WithContext(ctx).
		Model(&model.PipelineRun{}).
		Where("run_id = ?", runID).
		Updates(updates).Error
}

// GetRunByRequestID gets pipeline run by (pipeline_id, request_id).
// Returns (nil, nil) when not found.
func (r *PipelineRepo) GetRunByRequestID(ctx context.Context, pipelineID, requestID string) (*model.PipelineRun, error) {
	if strings.TrimSpace(pipelineID) == "" || strings.TrimSpace(requestID) == "" {
		return nil, nil
	}
	var rows []model.PipelineRun
	err := r.Database().WithContext(ctx).Clauses(database.Write()).
		Select(pipelineRunReadColumns).Where("pipeline_id = ? AND LOWER(request_id) = LOWER(?)", pipelineID, requestID).
		Limit(2).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if len(rows) != 1 || rows[0].PipelineID != pipelineID || rows[0].RequestID != requestID {
		return nil, ErrManualTriggerConflict
	}
	return &rows[0], nil
}

// ListRuns lists pipeline runs.
func (r *PipelineRepo) ListRuns(ctx context.Context, query *PipelineRunQuery) ([]*model.PipelineRun, int64, error) {
	if query == nil {
		query = &PipelineRunQuery{}
	}
	if query.Page <= 0 {
		query.Page = 1
	}
	if query.PageSize <= 0 {
		query.PageSize = 20
	}
	if query.PageSize > 100 {
		query.PageSize = 100
	}

	tx := r.Database().WithContext(ctx).Model(&model.PipelineRun{})
	if query.PipelineID != "" {
		tx = tx.Where("pipeline_id = ?", query.PipelineID)
	}
	if query.Status > 0 {
		tx = tx.Where("status = ?", query.Status)
	}
	total, err := Count(tx)
	if err != nil {
		return nil, 0, err
	}

	var list []*model.PipelineRun
	err = tx.Order("created_at DESC").
		Offset((query.Page - 1) * query.PageSize).
		Limit(query.PageSize).
		Find(&list).Error
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// IncrementRuns 原子更新流水线执行次数，避免并发触发丢失计数
func (r *PipelineRepo) IncrementRuns(ctx context.Context, pipelineID string) error {
	return r.Database().WithContext(ctx).Model(&model.Pipeline{}).Where("pipeline_id = ?", pipelineID).UpdateColumn("total_runs", gorm.Expr("total_runs + ?", 1)).Error
}

// RecordRunResult 原子更新流水线成功和失败统计
func (r *PipelineRepo) RecordRunResult(ctx context.Context, pipelineID string, status int) error {
	updates := map[string]any{"status": status}
	if status == model.PipelineStatusSuccess {
		updates["success_runs"] = gorm.Expr("success_runs + ?", 1)
	}
	if status == model.PipelineStatusFailed {
		updates["failed_runs"] = gorm.Expr("failed_runs + ?", 1)
	}
	return r.Database().WithContext(ctx).Model(&model.Pipeline{}).Where("pipeline_id = ?", pipelineID).Updates(updates).Error
}

// UpdateRunIfStatus 仅当当前状态匹配时推进运行状态，防止暂停操作覆盖终态
func (r *PipelineRepo) UpdateRunIfStatus(ctx context.Context, runID string, status int, updates map[string]any) error {
	if err := rejectRunProjectUpdates(updates); err != nil {
		return err
	}
	if err := rejectPollingRunUpdates(updates); err != nil {
		return err
	}
	if err := rejectRetryApprovalRunUpdates(updates); err != nil {
		return err
	}
	var err error
	updates, err = normalizeJSONUpdates(updates, pipelineRunJSONDefaults)
	if err != nil {
		return err
	}
	return r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		var run model.PipelineRun
		if err := tx.Select("pipeline_id").Where("run_id = ?", runID).First(&run).Error; err != nil {
			return err
		}
		// Admission takes pipeline -> project -> run. Acquire the pipeline
		// before updating a run too, so cancellation cannot form run -> pipeline
		// against a new admission waiting for that run.
		var pipeline model.Pipeline
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("pipeline_id").Where("pipeline_id = ?", run.PipelineID).First(&pipeline).Error; err != nil {
			return err
		}
		if pipeline.PipelineID != run.PipelineID {
			return gorm.ErrRecordNotFound
		}
		result := tx.Model(&model.PipelineRun{}).Where("run_id = ? AND pipeline_id = ? AND status = ?", runID, run.PipelineID, status).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if nextStatus, ok := updates["status"]; ok {
			return tx.Model(&model.Pipeline{}).Where("pipeline_id = ?", run.PipelineID).Update("status", nextStatus).Error
		}
		return nil
	})
}

// StartRunIfPending retains the legacy internal entry point with the same
// lifecycle and immutable identity admission boundary used by the process.
func (r *PipelineRepo) StartRunIfPending(ctx context.Context, runID string) error {
	run, err := r.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	admitted, err := r.StartManualRunIfPending(ctx, run)
	if err != nil {
		return err
	}
	if !admitted {
		return fmt.Errorf("pipeline run is no longer eligible for scheduling")
	}
	return nil
}
