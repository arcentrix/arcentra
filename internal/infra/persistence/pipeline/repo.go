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

package pipeline

import (
	"context"

	domain "github.com/arcentrix/arcentra/internal/domain/pipeline"
	"github.com/arcentrix/arcentra/pkg/store/database"
)

var _ domain.IPipelineRepository = (*PipelineRepo)(nil)

var pipelineSelectFields = []string{
	"id", "pipeline_id", "project_id", "name", "description",
	"repo_url", "default_branch", "pipeline_file_path",
	"status", "save_mode", "pr_target_branch", "metadata",
	"last_sync_status", "last_sync_message", "last_synced_at",
	"last_editor", "last_commit_sha", "last_save_request_id",
	"total_runs", "success_runs", "failed_runs",
	"created_by", "is_enabled", "created_at", "updated_at",
}

var pipelineRunSelectFields = []string{
	"id", "run_id", "pipeline_id", "request_id", "pipeline_name",
	"branch", "commit_sha", "definition_commit_sha", "definition_path",
	"status", "trigger_type", "triggered_by", "env",
	"total_jobs", "completed_jobs", "failed_jobs", "running_jobs",
	"current_stage", "total_stages",
	"start_time", "end_time", "duration",
	"created_at", "updated_at",
}

// PipelineRepo implements domain.IPipelineRepository.
type PipelineRepo struct {
	db database.IDatabase
}

// NewPipelineRepo creates a new PipelineRepo.
func NewPipelineRepo(db database.IDatabase) *PipelineRepo {
	return &PipelineRepo{db: db}
}

// Create inserts a new pipeline definition record.
func (r *PipelineRepo) Create(ctx context.Context, pipeline *domain.Pipeline) error {
	po := PipelinePOFromDomain(pipeline)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	pipeline.ID = po.ID
	pipeline.CreatedAt = po.CreatedAt
	pipeline.UpdatedAt = po.UpdatedAt
	return nil
}

// Update patches specific fields of a pipeline definition.
func (r *PipelineRepo) Update(ctx context.Context, pipelineID string, updates map[string]any) error {
	return r.db.Database().WithContext(ctx).
		Table(PipelinePO{}.TableName()).
		Where("pipeline_id = ?", pipelineID).
		Updates(updates).Error
}

// Get retrieves a pipeline by its business ID.
func (r *PipelineRepo) Get(ctx context.Context, pipelineID string) (*domain.Pipeline, error) {
	var po PipelinePO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Select(pipelineSelectFields).
		Where("pipeline_id = ?", pipelineID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

// List returns paginated pipelines matching the given query.
func (r *PipelineRepo) List(ctx context.Context, query *domain.PipelineQuery) ([]*domain.Pipeline, int64, error) {
	var pos []PipelinePO
	var count int64
	tbl := PipelinePO{}.TableName()

	q := r.db.Database().WithContext(ctx).Table(tbl)
	if query.ProjectID != "" {
		q = q.Where("project_id = ?", query.ProjectID)
	}
	if query.Name != "" {
		q = q.Where("name LIKE ?", "%"+query.Name+"%")
	}
	if query.Status != nil {
		q = q.Where("status = ?", int(*query.Status))
	}

	if err := q.Count(&count).Error; err != nil {
		return nil, 0, err
	}

	page, size := query.Page, query.Size
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	offset := (page - 1) * size

	if err := q.Select(pipelineSelectFields).
		Offset(offset).Limit(size).
		Order("id DESC").
		Find(&pos).Error; err != nil {
		return nil, 0, err
	}

	pipelines := make([]*domain.Pipeline, len(pos))
	for i := range pos {
		pipelines[i] = pos[i].ToDomain()
	}
	return pipelines, count, nil
}

// CreateRun inserts a new pipeline run record.
func (r *PipelineRepo) CreateRun(ctx context.Context, run *domain.PipelineRun) error {
	po := PipelineRunPOFromDomain(run)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	run.ID = po.ID
	run.CreatedAt = po.CreatedAt
	run.UpdatedAt = po.UpdatedAt
	return nil
}

// GetRun retrieves a pipeline run by its business ID.
func (r *PipelineRepo) GetRun(ctx context.Context, runID string) (*domain.PipelineRun, error) {
	var po PipelineRunPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Select(pipelineRunSelectFields).
		Where("run_id = ?", runID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

// UpdateRun patches specific fields of a pipeline run.
func (r *PipelineRepo) UpdateRun(ctx context.Context, runID string, updates map[string]any) error {
	return r.db.Database().WithContext(ctx).
		Table(PipelineRunPO{}.TableName()).
		Where("run_id = ?", runID).
		Updates(updates).Error
}

// GetRunByRequestID retrieves a pipeline run by its idempotency request ID.
func (r *PipelineRepo) GetRunByRequestID(ctx context.Context, pipelineID, requestID string) (*domain.PipelineRun, error) {
	var po PipelineRunPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Select(pipelineRunSelectFields).
		Where("pipeline_id = ? AND request_id = ?", pipelineID, requestID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

// ListRuns returns paginated pipeline runs matching the given query.
func (r *PipelineRepo) ListRuns(ctx context.Context, query *domain.PipelineRunQuery) ([]*domain.PipelineRun, int64, error) {
	var pos []PipelineRunPO
	var count int64
	tbl := PipelineRunPO{}.TableName()

	q := r.db.Database().WithContext(ctx).Table(tbl)
	if query.PipelineID != "" {
		q = q.Where("pipeline_id = ?", query.PipelineID)
	}
	if query.Branch != "" {
		q = q.Where("branch = ?", query.Branch)
	}
	if query.Status != nil {
		q = q.Where("status = ?", int(*query.Status))
	}

	if err := q.Count(&count).Error; err != nil {
		return nil, 0, err
	}

	page, size := query.Page, query.Size
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	offset := (page - 1) * size

	if err := q.Select(pipelineRunSelectFields).
		Offset(offset).Limit(size).
		Order("id DESC").
		Find(&pos).Error; err != nil {
		return nil, 0, err
	}

	runs := make([]*domain.PipelineRun, len(pos))
	for i := range pos {
		runs[i] = pos[i].ToDomain()
	}
	return runs, count, nil
}
