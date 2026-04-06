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

import "context"

// PipelineQuery holds filter criteria for listing pipelines.
type PipelineQuery struct {
	ProjectID string
	Name      string
	Status    *PipelineStatus
	Page      int
	Size      int
}

// PipelineRunQuery holds filter criteria for listing pipeline runs.
type PipelineRunQuery struct {
	PipelineID string
	Branch     string
	Status     *PipelineStatus
	Page       int
	Size       int
}

// IPipelineRepository defines persistence operations for Pipeline entities.
type IPipelineRepository interface {
	Create(ctx context.Context, pipeline *Pipeline) error
	Update(ctx context.Context, pipelineID string, updates map[string]any) error
	Get(ctx context.Context, pipelineID string) (*Pipeline, error)
	List(ctx context.Context, query *PipelineQuery) ([]*Pipeline, int64, error)
	CreateRun(ctx context.Context, run *PipelineRun) error
	GetRun(ctx context.Context, runID string) (*PipelineRun, error)
	UpdateRun(ctx context.Context, runID string, updates map[string]any) error
	GetRunByRequestID(ctx context.Context, pipelineID, requestID string) (*PipelineRun, error)
	ListRuns(ctx context.Context, query *PipelineRunQuery) ([]*PipelineRun, int64, error)
}
