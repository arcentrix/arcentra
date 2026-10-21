// Copyright 2025 Arcentra Authors.
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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IStepRunRepository defines step run persistence with context support.
type IStepRunRepository interface {
	Create(ctx context.Context, stepRun *model.StepRun) error
	GetByStepRunID(ctx context.Context, stepRunID string) (*model.StepRun, error)
	Get(ctx context.Context, pipelineID, jobID, stepRunID string) (*model.StepRun, error)
	List(ctx context.Context, filter StepRunFilter) ([]model.StepRun, int64, error)
	PatchByStepRunID(ctx context.Context, stepRunID string, updates map[string]any) error
	DeleteByStepRunID(ctx context.Context, stepRunID string) error
	ListArtifactsByStepRunID(ctx context.Context, stepRunID string) ([]model.StepRunArtifact, error)
}

type StepRunRepo struct {
	database.IDatabase
}

type StepRunFilter struct {
	// ExcludeRuntime 旧单步派发不得读取容器作业内容
	ExcludeRuntime bool
	StepRunIDs     []string
	PipelineID     string
	PipelineRunID  string
	JobID          string
	StepName       string
	AgentID        string
	Status         int
	Page           int
	PageSize       int
	SortBy         string
	SortDesc       bool
}

func NewStepRunRepo(db database.IDatabase) IStepRunRepository {
	return &StepRunRepo{IDatabase: db}
}

// Create inserts a new step run.
func (r *StepRunRepo) Create(ctx context.Context, stepRun *model.StepRun) error {
	if stepRun == nil || !emptyStepAdmission(stepRun) || stepRun.GenerationID != "" || stepRun.GenerationBindingDigest != "" || stepRun.RetryableShellExit {
		return gorm.ErrInvalidData
	}
	if err := normalizeJSONFields(map[string]*string{"retry_policy": &stepRun.RetryPolicy, "args": &stepRun.Args, "env": &stepRun.Env, "secrets": &stepRun.Secrets, "label_selector": &stepRun.LabelSelector}, stepRunJSONDefaults); err != nil {
		return err
	}
	parents, err := locateOrdinaryAttachmentScope(ctx, r.Database(), []string{stepRun.PipelineRunID}, stepRun.JobRunID)
	if err != nil {
		return err
	}
	return rerunAttachmentTransaction(ctx, r.Database(), func(tx *gorm.DB) error {
		if err := parents.lockParents(ctx, tx); err != nil {
			return err
		}
		if stepRun.JobRunID != "" {
			var parent model.JobRun
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("copilot_rerun_intent_id", "copilot_rerun_gate_id", "expected_source_commit", "job_run_id", "pipeline_id", "pipeline_run_id", "retry_plan", "retry_plan_digest").Where("job_run_id = ?", stepRun.JobRunID).First(&parent).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err := requireRerunAttachmentTransaction(tx); err != nil {
					return err
				}
			}
			if err == nil && (!emptyJobRerun(&parent) || parent.JobRunID != stepRun.JobRunID || parent.RetryPlan != "" || parent.RetryPlanDigest != "") {
				return fmt.Errorf("captured retry plan cannot acquire new step allocations")
			}
			var current *model.JobRun
			if err == nil {
				current = &parent
			}
			if err := parents.validateJob(current); err != nil {
				return err
			}
		}
		return tx.Table(stepRun.TableName()).Create(stepRun).Error
	})
}

// GetByStepRunID returns step run by business ID.
func (r *StepRunRepo) GetByStepRunID(ctx context.Context, stepRunID string) (*model.StepRun, error) {
	var stepRun model.StepRun
	err := r.Database().WithContext(ctx).
		Table(stepRun.TableName()).
		Where("step_run_id = ?", stepRunID).
		First(&stepRun).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &stepRun, nil
}

// Get returns step run by pipelineID, jobID and stepRunID.
func (r *StepRunRepo) Get(ctx context.Context, pipelineID, jobID, stepRunID string) (*model.StepRun, error) {
	var stepRun model.StepRun
	err := r.Database().WithContext(ctx).
		Table(stepRun.TableName()).
		Where("pipeline_id = ? AND job_id = ? AND step_run_id = ?", pipelineID, jobID, stepRunID).
		First(&stepRun).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &stepRun, nil
}

// List returns step runs with pagination and filters.
func (r *StepRunRepo) List(ctx context.Context, filter StepRunFilter) ([]model.StepRun, int64, error) {
	page := filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}

	var stepRun model.StepRun
	query := r.Database().WithContext(ctx).Table(stepRun.TableName())
	query = applyStepRunFilters(query, filter)
	if filter.ExcludeRuntime {
		query = ExcludeRuntimeSteps(query.Clauses(database.Write()))
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	sortBy := "created_at"
	switch strings.TrimSpace(filter.SortBy) {
	case "created_at", "updated_at", "step_index", "start_time", "end_time", "status", "name":
		sortBy = strings.TrimSpace(filter.SortBy)
	}

	order := sortBy + " ASC"
	if filter.SortDesc {
		order = sortBy + " DESC"
	}

	var stepRuns []model.StepRun
	if err := query.Order(order).Order("attempt ASC").Order("step_run_id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&stepRuns).Error; err != nil {
		return nil, 0, err
	}
	return stepRuns, total, nil
}

func applyStepRunFilters(query *gorm.DB, filter StepRunFilter) *gorm.DB {
	if len(filter.StepRunIDs) > 0 {
		query = query.Where("step_run_id IN ?", filter.StepRunIDs)
	}
	if strings.TrimSpace(filter.PipelineID) != "" {
		query = query.Where("pipeline_id = ?", strings.TrimSpace(filter.PipelineID))
	}
	if strings.TrimSpace(filter.PipelineRunID) != "" {
		query = query.Where("pipeline_run_id = ?", strings.TrimSpace(filter.PipelineRunID))
	}
	if strings.TrimSpace(filter.JobID) != "" {
		query = query.Where("job_id = ?", strings.TrimSpace(filter.JobID))
	}
	if strings.TrimSpace(filter.StepName) != "" {
		query = query.Where("name = ?", strings.TrimSpace(filter.StepName))
	}
	if strings.TrimSpace(filter.AgentID) != "" {
		query = query.Where("agent_id = ?", strings.TrimSpace(filter.AgentID))
	}
	if filter.Status > 0 {
		query = query.Where("status = ?", filter.Status)
	}
	return query
}

// PatchByStepRunID patches step run by stepRunID.
func (r *StepRunRepo) PatchByStepRunID(ctx context.Context, stepRunID string, updates map[string]any) error {
	if err := rejectStepGenerationUpdates(updates); err != nil {
		return err
	}
	var err error
	updates, err = normalizeJSONUpdates(updates, stepRunJSONDefaults)
	if err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}
	updates["updated_at"] = time.Now()
	return ordinaryStepMutation(ctx, r.Database(), stepRunID, updates, func(tx *gorm.DB) error {
		return reportedStatusResult(tx.Table((&model.StepRun{}).TableName()).Where("step_run_id = ?", stepRunID).Updates(updates))
	})
}

// DeleteByStepRunID 仅删除未被固定重试计划或代次回执引用的步骤
func (r *StepRunRepo) DeleteByStepRunID(ctx context.Context, stepRunID string) error {
	return rerunAttachmentTransaction(ctx, r.Database(), func(tx *gorm.DB) error {
		var step model.StepRun
		if err := tx.Select("step_run_id", "pipeline_run_id", "job_run_id", "generation_id", "generation_binding_digest", "attempt_admission_request", "attempt_admission_request_digest", "attempt_admission").Where("step_run_id = ?", stepRunID).First(&step).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := lockOrdinaryRerunParent(ctx, tx, step.PipelineRunID); err != nil {
			return err
		}
		if step.StepRunID != stepRunID {
			return fmt.Errorf("exact step identity is required")
		}
		if !emptyStepAdmission(&step) || step.GenerationID != "" || step.GenerationBindingDigest != "" {
			return fmt.Errorf("generation attempt evidence cannot be deleted")
		}
		if step.JobRunID != "" {
			var parent model.JobRun
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("copilot_rerun_intent_id", "copilot_rerun_gate_id", "expected_source_commit", "job_run_id", "retry_plan", "retry_plan_digest", "retry_approval_required", "retry_approval_id", "retry_approval_evidence_hash").Where("job_run_id = ?", step.JobRunID).First(&parent).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil && (!emptyJobRerun(&parent) || parent.JobRunID != step.JobRunID || parent.RetryPlan != "" || parent.RetryPlanDigest != "" || !emptyRetryApproval(&parent)) {
				return fmt.Errorf("captured retry attempts and receipts cannot be deleted")
			}
		}
		return reportedStatusResult(tx.Where("(pipeline_run_id = ? OR (pipeline_run_id IS NULL AND ? = ?))", step.PipelineRunID, step.PipelineRunID, "").Where("step_run_id = ? AND job_run_id = ? AND generation_id = ? AND generation_binding_digest = ?", stepRunID, step.JobRunID, "", "").Where("attempt_admission_request = ? AND attempt_admission_request_digest = ? AND attempt_admission = ?", "", "", "").Delete(&model.StepRun{}))
	})
}

// ListArtifactsByStepRunID returns artifacts by stepRunID.
func (r *StepRunRepo) ListArtifactsByStepRunID(ctx context.Context, stepRunID string) ([]model.StepRunArtifact, error) {
	var artifacts []model.StepRunArtifact
	err := r.Database().WithContext(ctx).
		Table((&model.StepRunArtifact{}).TableName()).
		Where("step_run_id = ?", stepRunID).
		Order("created_at DESC").
		Find(&artifacts).Error
	if err != nil {
		return nil, err
	}
	return artifacts, nil
}
