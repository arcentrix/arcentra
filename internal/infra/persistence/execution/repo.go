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

package execution

import (
	"context"

	domain "github.com/arcentrix/arcentra/internal/domain/execution"
	"github.com/arcentrix/arcentra/pkg/store/database"
)

var _ domain.IStepRunRepository = (*StepRunRepo)(nil)

var stepRunSelectFields = []string{
	"id", "step_run_id", "name", "pipeline_id", "pipeline_run_id",
	"stage_id", "job_id", "job_run_id", "step_index", "agent_id",
	"status", "priority", "uses", "action", "args",
	"workspace", "env", "timeout",
	"retry_count", "current_retry", "allow_failure", "continue_on_error",
	"when_expr", "label_selector", "depends_on",
	"exit_code", "error_message",
	"start_time", "end_time", "duration",
	"created_by", "created_at", "updated_at",
}

// StepRunRepo implements domain.IStepRunRepository.
type StepRunRepo struct {
	db database.IDatabase
}

// NewStepRunRepo creates a new StepRunRepo.
func NewStepRunRepo(db database.IDatabase) *StepRunRepo {
	return &StepRunRepo{db: db}
}

// Create inserts a new step run record.
func (r *StepRunRepo) Create(ctx context.Context, stepRun *domain.StepRun) error {
	po := StepRunPOFromDomain(stepRun)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	stepRun.ID = po.ID
	stepRun.CreatedAt = po.CreatedAt
	stepRun.UpdatedAt = po.UpdatedAt
	return nil
}

// GetByID retrieves a step run by its business ID.
func (r *StepRunRepo) GetByID(ctx context.Context, stepRunID string) (*domain.StepRun, error) {
	var po StepRunPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Select(stepRunSelectFields).
		Where("step_run_id = ?", stepRunID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

// Get retrieves a step run scoped by pipeline, job, and step run IDs.
func (r *StepRunRepo) Get(ctx context.Context, pipelineID, jobID, stepRunID string) (*domain.StepRun, error) {
	var po StepRunPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Select(stepRunSelectFields).
		Where("pipeline_id = ? AND job_id = ? AND step_run_id = ?", pipelineID, jobID, stepRunID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

// List returns paginated step runs matching the given filter.
func (r *StepRunRepo) List(ctx context.Context, filter domain.StepRunFilter) ([]domain.StepRun, int64, error) {
	var pos []StepRunPO
	var count int64
	tbl := StepRunPO{}.TableName()

	q := r.db.Database().WithContext(ctx).Table(tbl)
	if filter.PipelineID != "" {
		q = q.Where("pipeline_id = ?", filter.PipelineID)
	}
	if filter.PipelineRunID != "" {
		q = q.Where("pipeline_run_id = ?", filter.PipelineRunID)
	}
	if filter.JobID != "" {
		q = q.Where("job_id = ?", filter.JobID)
	}
	if filter.JobRunID != "" {
		q = q.Where("job_run_id = ?", filter.JobRunID)
	}
	if filter.AgentID != "" {
		q = q.Where("agent_id = ?", filter.AgentID)
	}
	if filter.Status != nil {
		q = q.Where("status = ?", int(*filter.Status))
	}

	if err := q.Count(&count).Error; err != nil {
		return nil, 0, err
	}

	page, size := filter.Page, filter.Size
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	offset := (page - 1) * size

	if err := q.Select(stepRunSelectFields).
		Offset(offset).Limit(size).
		Order("id DESC").
		Find(&pos).Error; err != nil {
		return nil, 0, err
	}

	runs := make([]domain.StepRun, len(pos))
	for i := range pos {
		runs[i] = *pos[i].ToDomain()
	}
	return runs, count, nil
}

// Patch updates specific fields of a step run.
func (r *StepRunRepo) Patch(ctx context.Context, stepRunID string, updates map[string]any) error {
	return r.db.Database().WithContext(ctx).
		Table(StepRunPO{}.TableName()).
		Where("step_run_id = ?", stepRunID).
		Updates(updates).Error
}

// Delete removes a step run by its business ID.
func (r *StepRunRepo) Delete(ctx context.Context, stepRunID string) error {
	return r.db.Database().WithContext(ctx).
		Table(StepRunPO{}.TableName()).
		Where("step_run_id = ?", stepRunID).
		Delete(&StepRunPO{}).Error
}

// ListArtifacts returns all artifacts associated with a step run.
func (r *StepRunRepo) ListArtifacts(ctx context.Context, stepRunID string) ([]domain.StepRunArtifact, error) {
	var pos []StepRunArtifactPO
	if err := r.db.Database().WithContext(ctx).
		Table(StepRunArtifactPO{}.TableName()).
		Where("step_run_id = ?", stepRunID).
		Order("id ASC").
		Find(&pos).Error; err != nil {
		return nil, err
	}
	artifacts := make([]domain.StepRunArtifact, len(pos))
	for i := range pos {
		artifacts[i] = *pos[i].ToDomain()
	}
	return artifacts, nil
}
