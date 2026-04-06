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
	"fmt"

	domain "github.com/arcentrix/arcentra/internal/domain/execution"
)

// ManageStepRunUseCase coordinates step run persistence operations.
type ManageStepRunUseCase struct {
	repo domain.IStepRunRepository
}

func NewManageStepRunUseCase(repo domain.IStepRunRepository) *ManageStepRunUseCase {
	return &ManageStepRunUseCase{repo: repo}
}

func (uc *ManageStepRunUseCase) CreateStepRun(ctx context.Context, in CreateStepRunInput) (*domain.StepRun, error) {
	sr := &domain.StepRun{
		StepRunID:     in.StepRunID,
		Name:          in.Name,
		PipelineID:    in.PipelineID,
		PipelineRunID: in.PipelineRunID,
		StageID:       in.StageID,
		JobID:         in.JobID,
		JobRunID:      in.JobRunID,
		StepIndex:     in.StepIndex,
		Status:        domain.StepRunStatusWaiting,
		Priority:      in.Priority,
		Uses:          in.Uses,
		Action:        in.Action,
		Args:          in.Args,
		Workspace:     in.Workspace,
		CreatedBy:     in.CreatedBy,
	}
	if err := uc.repo.Create(ctx, sr); err != nil {
		return nil, fmt.Errorf("create step run: %w", err)
	}
	return sr, nil
}

func (uc *ManageStepRunUseCase) GetStepRun(ctx context.Context, stepRunID string) (*domain.StepRun, error) {
	return uc.repo.GetByID(ctx, stepRunID)
}

func (uc *ManageStepRunUseCase) UpdateStepRun(ctx context.Context, stepRunID string, updates map[string]any) error {
	return uc.repo.Patch(ctx, stepRunID, updates)
}

func (uc *ManageStepRunUseCase) ListStepRuns(ctx context.Context, filter domain.StepRunFilter) ([]domain.StepRun, int64, error) {
	return uc.repo.List(ctx, filter)
}

func (uc *ManageStepRunUseCase) ListArtifacts(ctx context.Context, stepRunID string) ([]domain.StepRunArtifact, error) {
	return uc.repo.ListArtifacts(ctx, stepRunID)
}
