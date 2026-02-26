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

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
)

// IStepRunRepository defines step run persistence with context support.
type IStepRunRepository interface {
	Get(ctx context.Context, pipelineId, jobId, stepRunId string) (*model.StepRun, error)
}

type StepRunRepo struct {
	database.IDatabase
}

func NewStepRunRepo(db database.IDatabase) IStepRunRepository {
	return &StepRunRepo{IDatabase: db}
}

// Get returns step run by pipelineId, jobId and stepRunId.
func (r *StepRunRepo) Get(ctx context.Context, pipelineId, jobId, stepRunId string) (*model.StepRun, error) {
	var stepRun model.StepRun
	err := r.Database().WithContext(ctx).
		Table(stepRun.TableName()).
		Where("pipeline_id = ? AND job_id = ? AND step_run_id = ?", pipelineId, jobId, stepRunId).
		First(&stepRun).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &stepRun, nil
}
