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
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
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
	StepRunIDs    []string
	PipelineID    string
	PipelineRunID string
	JobID         string
	StepName      string
	AgentID       string
	Status        int
	Page          int
	PageSize      int
	SortBy        string
	SortDesc      bool
}

func NewStepRunRepo(db database.IDatabase) IStepRunRepository {
	return &StepRunRepo{IDatabase: db}
}

// Create inserts a new step run.
func (r *StepRunRepo) Create(ctx context.Context, stepRun *model.StepRun) error {
	if stepRun == nil {
		return gorm.ErrInvalidData
	}
	return r.Database().WithContext(ctx).Table(stepRun.TableName()).Create(stepRun).Error
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

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	sortBy := "created_at"
	if strings.TrimSpace(filter.SortBy) != "" {
		sortBy = strings.TrimSpace(filter.SortBy)
	}
	order := sortBy + " ASC"
	if filter.SortDesc {
		order = sortBy + " DESC"
	}

	var stepRuns []model.StepRun
	if err := query.Order(order).Offset((page - 1) * pageSize).Limit(pageSize).Find(&stepRuns).Error; err != nil {
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
	if len(updates) == 0 {
		return nil
	}
	updates["updated_at"] = time.Now()
	return r.Database().WithContext(ctx).
		Table((&model.StepRun{}).TableName()).
		Where("step_run_id = ?", stepRunID).
		Updates(updates).Error
}

// DeleteByStepRunID deletes a step run by stepRunID.
func (r *StepRunRepo) DeleteByStepRunID(ctx context.Context, stepRunID string) error {
	return r.Database().WithContext(ctx).
		Table((&model.StepRun{}).TableName()).
		Where("step_run_id = ?", stepRunID).
		Delete(&model.StepRun{}).Error
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
