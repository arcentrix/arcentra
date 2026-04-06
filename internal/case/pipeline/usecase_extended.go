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

package pipeline

import (
	"context"
	"fmt"
)

func (uc *ManagePipelineUseCase) DeletePipeline(ctx context.Context, pipelineID string) error {
	_, err := uc.repo.Get(ctx, pipelineID)
	if err != nil {
		return fmt.Errorf("pipeline not found: %w", err)
	}
	return uc.repo.Update(ctx, pipelineID, map[string]any{"is_enabled": false})
}

func (uc *ManagePipelineUseCase) GetPipelineSpec(ctx context.Context, pipelineID string) (any, error) {
	p, err := uc.repo.Get(ctx, pipelineID)
	if err != nil {
		return nil, fmt.Errorf("pipeline not found: %w", err)
	}
	return map[string]any{
		"pipelineId":       p.PipelineID,
		"pipelineFilePath": p.PipelineFilePath,
		"branch":           p.DefaultBranch,
		"headCommitSha":    p.LastCommitSha,
	}, nil
}

func (uc *ManagePipelineUseCase) ValidatePipelineSpec(ctx context.Context, pipelineID string, spec map[string]any) (any, error) {
	return map[string]any{
		"valid":    true,
		"warnings": []string{},
	}, nil
}

func (uc *ManagePipelineUseCase) SavePipelineSpec(ctx context.Context, pipelineID string, data map[string]any) (any, error) {
	return map[string]any{
		"pipelineId": pipelineID,
		"saved":      true,
	}, nil
}

func (uc *ManagePipelineUseCase) StopRun(ctx context.Context, pipelineID, runID, reason string) error {
	return uc.repo.UpdateRun(ctx, runID, map[string]any{
		"status": "cancelled",
	})
}

func (uc *ManagePipelineUseCase) PauseRun(ctx context.Context, pipelineID, runID, reason, operator string) error {
	return uc.repo.UpdateRun(ctx, runID, map[string]any{
		"status": "paused",
	})
}

func (uc *ManagePipelineUseCase) ResumeRun(ctx context.Context, pipelineID, runID, reason, operator string) error {
	return uc.repo.UpdateRun(ctx, runID, map[string]any{
		"status": "running",
	})
}
