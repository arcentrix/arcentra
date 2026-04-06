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

// StepRunStatus represents the execution state of a step run.
type StepRunStatus int

const (
	StepRunStatusWaiting   StepRunStatus = 1
	StepRunStatusQueued    StepRunStatus = 2
	StepRunStatusRunning   StepRunStatus = 3
	StepRunStatusSuccess   StepRunStatus = 4
	StepRunStatusFailed    StepRunStatus = 5
	StepRunStatusCancelled StepRunStatus = 6
	StepRunStatusTimeout   StepRunStatus = 7
	StepRunStatusSkipped   StepRunStatus = 8
)

func (s StepRunStatus) String() string {
	switch s {
	case StepRunStatusWaiting:
		return "waiting"
	case StepRunStatusQueued:
		return "queued"
	case StepRunStatusRunning:
		return "running"
	case StepRunStatusSuccess:
		return "success"
	case StepRunStatusFailed:
		return "failed"
	case StepRunStatusCancelled:
		return "cancelled"
	case StepRunStatusTimeout:
		return "timeout"
	case StepRunStatusSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// IsTerminal returns true if the status represents a completed execution.
func (s StepRunStatus) IsTerminal() bool {
	switch s {
	case StepRunStatusSuccess, StepRunStatusFailed, StepRunStatusCancelled,
		StepRunStatusTimeout, StepRunStatusSkipped:
		return true
	default:
		return false
	}
}
