// Copyright 2025 Arcentra Team
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

package nova

import (
	"context"
	"time"
)

// TaskStatus represents the status of a task
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"    // Task is pending
	TaskStatusQueued     TaskStatus = "queued"     // Task is queued
	TaskStatusProcessing TaskStatus = "processing" // Task is being processed
	TaskStatusCompleted  TaskStatus = "completed"  // Task is completed
	TaskStatusFailed     TaskStatus = "failed"     // Task failed
	TaskStatusCancelled  TaskStatus = "cancelled"  // Task is cancelled
	TaskStatusTimeout    TaskStatus = "timeout"    // Task timed out
	TaskStatusSkipped    TaskStatus = "skipped"    // Task is skipped
	TaskStatusUnknown    TaskStatus = "unknown"    // Unknown status
)

// TaskRecord represents a task execution record
type TaskRecord struct {
	TaskID      string         // Task ID
	Task        *Task          // Task content
	Status      TaskStatus     // Task status
	Queue       string         // Queue name
	Priority    Priority       // Priority
	CreatedAt   time.Time      // Creation time
	QueuedAt    *time.Time     // Queued time
	ProcessAt   *time.Time     // Scheduled execution time
	StartedAt   *time.Time     // Processing start time
	CompletedAt *time.Time     // Completion time
	FailedAt    *time.Time     // Failure time
	Error       string         // Error message
	RetryCount  int            // Retry count
	Metadata    map[string]any // Metadata
}

// TaskRecorder is the interface for recording and querying task execution history
type TaskRecorder interface {
	// Record records a task
	Record(ctx context.Context, record *TaskRecord) error

	// UpdateStatus updates the task status
	UpdateStatus(ctx context.Context, taskID string, status TaskStatus, err error) error

	// Get retrieves a task record by task ID
	Get(ctx context.Context, taskID string) (*TaskRecord, error)

	// ListTaskRecords lists task records based on filter criteria
	ListTaskRecords(ctx context.Context, filter *TaskRecordFilter) ([]*TaskRecord, error)

	// Delete deletes a task record by task ID
	Delete(ctx context.Context, taskID string) error
}

// TaskRecordFilter is used to filter task records
type TaskRecordFilter struct {
	Status    []TaskStatus   // Status filter
	Queue     string         // Queue filter
	Priority  *Priority      // Priority filter
	StartTime *time.Time     // Start time
	EndTime   *time.Time     // End time
	Limit     int            // Limit count
	Offset    int            // Offset
	Metadata  map[string]any // Metadata filter
}
