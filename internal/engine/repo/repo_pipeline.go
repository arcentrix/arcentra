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
	"errors"
	"strings"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
)

// PipelineQuery defines query parameters for listing pipelines.
type PipelineQuery struct {
	ProjectId string
	Name      string
	Status    int
	Page      int
	PageSize  int
}

// PipelineRunQuery defines query parameters for listing pipeline runs.
type PipelineRunQuery struct {
	PipelineId string
	Status     int
	Page       int
	PageSize   int
}

// IPipelineRepository defines persistence methods for pipeline and pipeline run.
type IPipelineRepository interface {
	Create(ctx context.Context, pipeline *model.Pipeline) error
	Update(ctx context.Context, pipelineId string, updates map[string]any) error
	Get(ctx context.Context, pipelineId string) (*model.Pipeline, error)
	List(ctx context.Context, query *PipelineQuery) ([]*model.Pipeline, int64, error)
	CreateRun(ctx context.Context, run *model.PipelineRun) error
	GetRun(ctx context.Context, runId string) (*model.PipelineRun, error)
	UpdateRun(ctx context.Context, runId string, updates map[string]any) error
	GetRunByRequestId(ctx context.Context, pipelineId, requestId string) (*model.PipelineRun, error)
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
	return r.Database().WithContext(ctx).Create(pipeline).Error
}

// Update updates a pipeline by pipelineId.
func (r *PipelineRepo) Update(ctx context.Context, pipelineId string, updates map[string]any) error {
	return r.Database().WithContext(ctx).
		Model(&model.Pipeline{}).
		Where("pipeline_id = ?", pipelineId).
		Updates(updates).Error
}

// Get returns pipeline by pipelineId.
func (r *PipelineRepo) Get(ctx context.Context, pipelineId string) (*model.Pipeline, error) {
	var one model.Pipeline
	if err := r.Database().WithContext(ctx).
		Where("pipeline_id = ?", pipelineId).
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

	tx := r.Database().WithContext(ctx).Model(&model.Pipeline{})
	if query.ProjectId != "" {
		tx = tx.Where("project_id = ?", query.ProjectId)
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
	return r.Database().WithContext(ctx).Create(run).Error
}

// GetRun gets pipeline run by runId.
func (r *PipelineRepo) GetRun(ctx context.Context, runId string) (*model.PipelineRun, error) {
	var one model.PipelineRun
	if err := r.Database().WithContext(ctx).
		Where("run_id = ?", runId).
		First(&one).Error; err != nil {
		return nil, err
	}
	return &one, nil
}

// UpdateRun updates a pipeline run by runId.
func (r *PipelineRepo) UpdateRun(ctx context.Context, runId string, updates map[string]any) error {
	return r.Database().WithContext(ctx).
		Model(&model.PipelineRun{}).
		Where("run_id = ?", runId).
		Updates(updates).Error
}

// GetRunByRequestId gets pipeline run by (pipeline_id, request_id).
// Returns (nil, nil) when not found.
func (r *PipelineRepo) GetRunByRequestId(ctx context.Context, pipelineId, requestId string) (*model.PipelineRun, error) {
	if strings.TrimSpace(pipelineId) == "" || strings.TrimSpace(requestId) == "" {
		return nil, nil
	}
	var one model.PipelineRun
	err := r.Database().WithContext(ctx).
		Where("pipeline_id = ? AND request_id = ?", pipelineId, requestId).
		First(&one).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &one, nil
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
	if query.PipelineId != "" {
		tx = tx.Where("pipeline_id = ?", query.PipelineId)
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
