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

import "time"

// StepRunStarted is raised when a step run begins execution.
type StepRunStarted struct {
	StepRunID  string
	AgentID    string
	OccurredAt time.Time
}

func (e StepRunStarted) EventType() string { return "execution.step_run_started" }

// StepRunCompleted is raised when a step run finishes.
type StepRunCompleted struct {
	StepRunID  string
	Status     StepRunStatus
	ExitCode   *int
	Duration   int64
	OccurredAt time.Time
}

func (e StepRunCompleted) EventType() string { return "execution.step_run_completed" }

// StepRunRetried is raised when a failed step run is retried.
type StepRunRetried struct {
	StepRunID    string
	RetryAttempt int
	OccurredAt   time.Time
}

func (e StepRunRetried) EventType() string { return "execution.step_run_retried" }
