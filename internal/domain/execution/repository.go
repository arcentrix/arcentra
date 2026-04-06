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

import "context"

// StepRunFilter holds criteria for listing step runs.
type StepRunFilter struct {
	PipelineID    string
	PipelineRunID string
	JobID         string
	JobRunID      string
	AgentID       string
	Status        *StepRunStatus
	Page          int
	Size          int
}

// IStepRunRepository defines persistence operations for StepRun entities.
type IStepRunRepository interface {
	Create(ctx context.Context, stepRun *StepRun) error
	GetByID(ctx context.Context, stepRunID string) (*StepRun, error)
	Get(ctx context.Context, pipelineID, jobID, stepRunID string) (*StepRun, error)
	List(ctx context.Context, filter StepRunFilter) ([]StepRun, int64, error)
	Patch(ctx context.Context, stepRunID string, updates map[string]any) error
	Delete(ctx context.Context, stepRunID string) error
	ListArtifacts(ctx context.Context, stepRunID string) ([]StepRunArtifact, error)
}
