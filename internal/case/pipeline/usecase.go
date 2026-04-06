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
	"fmt"

	"github.com/arcentrix/arcentra/internal/domain/pipeline"
	"github.com/arcentrix/arcentra/pkg/foundation/id"
)

// ManagePipelineUseCase coordinates pipeline definition and run operations.
type ManagePipelineUseCase struct {
	repo pipeline.IPipelineRepository
}

func NewManagePipelineUseCase(repo pipeline.IPipelineRepository) *ManagePipelineUseCase {
	return &ManagePipelineUseCase{repo: repo}
}

func (uc *ManagePipelineUseCase) CreatePipeline(ctx context.Context, in CreatePipelineInput) (*pipeline.Pipeline, error) {
	p := &pipeline.Pipeline{
		PipelineID:       id.GetUUID(),
		ProjectID:        in.ProjectID,
		Name:             in.Name,
		Description:      in.Description,
		RepoURL:          in.RepoURL,
		DefaultBranch:    in.DefaultBranch,
		PipelineFilePath: in.PipelineFilePath,
		CreatedBy:        in.CreatedBy,
		Status:           pipeline.PipelineStatusPending,
		SaveMode:         pipeline.PipelineSaveModeDirect,
		IsEnabled:        true,
	}
	if err := uc.repo.Create(ctx, p); err != nil {
		return nil, fmt.Errorf("create pipeline: %w", err)
	}
	return p, nil
}

func (uc *ManagePipelineUseCase) GetPipeline(ctx context.Context, pipelineID string) (*pipeline.Pipeline, error) {
	return uc.repo.Get(ctx, pipelineID)
}

func (uc *ManagePipelineUseCase) UpdatePipeline(ctx context.Context, pipelineID string, updates map[string]any) error {
	return uc.repo.Update(ctx, pipelineID, updates)
}

func (uc *ManagePipelineUseCase) ListPipelines(ctx context.Context, query *pipeline.PipelineQuery) ([]*pipeline.Pipeline, int64, error) {
	return uc.repo.List(ctx, query)
}

func (uc *ManagePipelineUseCase) TriggerRun(ctx context.Context, in TriggerRunInput) (*pipeline.PipelineRun, error) {
	pl, err := uc.repo.Get(ctx, in.PipelineID)
	if err != nil {
		return nil, fmt.Errorf("get pipeline: %w", err)
	}
	defCommit := in.CommitSha
	if defCommit == "" {
		defCommit = pl.LastCommitSha
	}
	run := &pipeline.PipelineRun{
		RunID:               id.GetUUID(),
		PipelineID:          in.PipelineID,
		RequestID:           id.GetUUID(),
		PipelineName:        pl.Name,
		Branch:              in.Branch,
		CommitSha:           in.CommitSha,
		DefinitionCommitSha: defCommit,
		DefinitionPath:      pl.PipelineFilePath,
		Status:              pipeline.PipelineStatusPending,
		TriggerType:         in.TriggerType,
		TriggeredBy:         in.TriggeredBy,
	}
	if err := uc.repo.CreateRun(ctx, run); err != nil {
		return nil, fmt.Errorf("create pipeline run: %w", err)
	}
	return run, nil
}

func (uc *ManagePipelineUseCase) GetRun(ctx context.Context, runID string) (*pipeline.PipelineRun, error) {
	return uc.repo.GetRun(ctx, runID)
}

func (uc *ManagePipelineUseCase) ListRuns(ctx context.Context, query *pipeline.PipelineRunQuery) ([]*pipeline.PipelineRun, int64, error) {
	return uc.repo.ListRuns(ctx, query)
}
