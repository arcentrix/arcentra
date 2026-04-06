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

// PipelineStatus represents the execution status of a pipeline or run.
type PipelineStatus int

const (
	PipelineStatusUnknown   PipelineStatus = 0
	PipelineStatusPending   PipelineStatus = 1
	PipelineStatusRunning   PipelineStatus = 2
	PipelineStatusSuccess   PipelineStatus = 3
	PipelineStatusFailed    PipelineStatus = 4
	PipelineStatusCancelled PipelineStatus = 5
	PipelineStatusPaused    PipelineStatus = 6
)

func (s PipelineStatus) String() string {
	switch s {
	case PipelineStatusPending:
		return "pending"
	case PipelineStatusRunning:
		return "running"
	case PipelineStatusSuccess:
		return "success"
	case PipelineStatusFailed:
		return "failed"
	case PipelineStatusCancelled:
		return "cancelled"
	case PipelineStatusPaused:
		return "paused"
	default:
		return "unknown"
	}
}

// IsTerminal returns true if the status represents a completed execution.
func (s PipelineStatus) IsTerminal() bool {
	switch s {
	case PipelineStatusSuccess, PipelineStatusFailed, PipelineStatusCancelled:
		return true
	default:
		return false
	}
}

// PipelineSaveMode defines how pipeline definitions are persisted.
type PipelineSaveMode int

const (
	PipelineSaveModeDirect PipelineSaveMode = 1
	PipelineSaveModePR     PipelineSaveMode = 2
)

// PipelineSyncStatus indicates the last sync result of a pipeline definition.
type PipelineSyncStatus int

const (
	PipelineSyncStatusUnknown PipelineSyncStatus = 0
	PipelineSyncStatusSuccess PipelineSyncStatus = 1
	PipelineSyncStatusFailed  PipelineSyncStatus = 2
)
