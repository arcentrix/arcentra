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

import "time"

// PipelineCreated is raised when a new pipeline definition is created.
type PipelineCreated struct {
	PipelineID string
	ProjectID  string
	Name       string
	OccurredAt time.Time
}

func (e PipelineCreated) EventType() string { return "pipeline.created" }

// PipelineRunStarted is raised when a pipeline run begins execution.
type PipelineRunStarted struct {
	RunID      string
	PipelineID string
	Branch     string
	OccurredAt time.Time
}

func (e PipelineRunStarted) EventType() string { return "pipeline.run_started" }

// PipelineRunCompleted is raised when a pipeline run finishes.
type PipelineRunCompleted struct {
	RunID      string
	PipelineID string
	Status     PipelineStatus
	Duration   int64
	OccurredAt time.Time
}

func (e PipelineRunCompleted) EventType() string { return "pipeline.run_completed" }
