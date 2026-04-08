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
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
)

// IJobRunRepository defines persistence methods for job run records.
type IJobRunRepository interface {
	Create(ctx context.Context, jobRun *model.JobRun) error
	GetByJobRunID(ctx context.Context, jobRunID string) (*model.JobRun, error)
	UpdateByJobRunID(ctx context.Context, jobRunID string, updates map[string]any) error
	ListByPipelineRunID(ctx context.Context, pipelineRunID string) ([]*model.JobRun, error)
	GetStatus(ctx context.Context, jobRunID string) (int, error)
}

// JobRunRepo implements IJobRunRepository using GORM.
type JobRunRepo struct {
	database.IDatabase
}

// NewJobRunRepo creates a new job run repository.
func NewJobRunRepo(db database.IDatabase) IJobRunRepository {
	return &JobRunRepo{IDatabase: db}
}

// Create inserts a new job run record.
func (r *JobRunRepo) Create(ctx context.Context, jobRun *model.JobRun) error {
	if jobRun == nil {
		return gorm.ErrInvalidData
	}
	return r.Database().WithContext(ctx).Create(jobRun).Error
}

// GetByJobRunID returns a job run by its business ID.
func (r *JobRunRepo) GetByJobRunID(ctx context.Context, jobRunID string) (*model.JobRun, error) {
	var jr model.JobRun
	err := r.Database().WithContext(ctx).
		Where("job_run_id = ?", jobRunID).
		First(&jr).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &jr, nil
}

// UpdateByJobRunID patches a job run by jobRunID.
func (r *JobRunRepo) UpdateByJobRunID(ctx context.Context, jobRunID string, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	updates["updated_at"] = time.Now()
	return r.Database().WithContext(ctx).
		Model(&model.JobRun{}).
		Where("job_run_id = ?", jobRunID).
		Updates(updates).Error
}

// ListByPipelineRunID returns all job runs belonging to a pipeline run.
func (r *JobRunRepo) ListByPipelineRunID(ctx context.Context, pipelineRunID string) ([]*model.JobRun, error) {
	if strings.TrimSpace(pipelineRunID) == "" {
		return nil, nil
	}
	var list []*model.JobRun
	err := r.Database().WithContext(ctx).
		Where("pipeline_run_id = ?", pipelineRunID).
		Order("created_at ASC").
		Find(&list).Error
	if err != nil {
		return nil, err
	}
	return list, nil
}

// GetStatus returns only the status column for a job run (lightweight polling).
func (r *JobRunRepo) GetStatus(ctx context.Context, jobRunID string) (int, error) {
	var status int
	err := r.Database().WithContext(ctx).
		Model(&model.JobRun{}).
		Where("job_run_id = ?", jobRunID).
		Select("status").
		Scan(&status).Error
	return status, err
}
