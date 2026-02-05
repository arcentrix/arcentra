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
	"testing"
	"time"
)

func TestTaskStatusConstants(t *testing.T) {
	tests := []struct {
		name   string
		status TaskStatus
		want   string
	}{
		{"Pending", TaskStatusPending, "pending"},
		{"Queued", TaskStatusQueued, "queued"},
		{"Processing", TaskStatusProcessing, "processing"},
		{"Completed", TaskStatusCompleted, "completed"},
		{"Failed", TaskStatusFailed, "failed"},
		{"Cancelled", TaskStatusCancelled, "cancelled"},
		{"Timeout", TaskStatusTimeout, "timeout"},
		{"Skipped", TaskStatusSkipped, "skipped"},
		{"Unknown", TaskStatusUnknown, "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.status) != tt.want {
				t.Errorf("expected TaskStatus%s to be '%s', got '%s'", tt.name, tt.want, string(tt.status))
			}
		})
	}
}

func TestTaskRecord_Fields(t *testing.T) {
	now := time.Now()
	task := &Task{Type: "test", Payload: []byte("data")}
	queuedAt := now.Add(1 * time.Minute)
	processAt := now.Add(2 * time.Minute)
	startedAt := now.Add(3 * time.Minute)
	completedAt := now.Add(4 * time.Minute)
	failedAt := now.Add(5 * time.Minute)

	record := &TaskRecord{
		TaskID:      "task-123",
		Task:        task,
		Status:      TaskStatusCompleted,
		Queue:       "test-queue",
		Priority:    PriorityHigh,
		CreatedAt:   now,
		QueuedAt:    &queuedAt,
		ProcessAt:   &processAt,
		StartedAt:   &startedAt,
		CompletedAt: &completedAt,
		FailedAt:    &failedAt,
		Error:       "test-error",
		RetryCount:  3,
		Metadata:    map[string]any{"key": "value"},
	}

	if record.TaskID != "task-123" {
		t.Errorf("expected TaskID to be 'task-123', got %s", record.TaskID)
	}
	if record.Task != task {
		t.Error("expected Task to match")
	}
	if record.Status != TaskStatusCompleted {
		t.Errorf("expected Status to be TaskStatusCompleted, got %v", record.Status)
	}
	if record.Queue != "test-queue" {
		t.Errorf("expected Queue to be 'test-queue', got %s", record.Queue)
	}
	if record.Priority != PriorityHigh {
		t.Errorf("expected Priority to be PriorityHigh, got %v", record.Priority)
	}
	if !record.CreatedAt.Equal(now) {
		t.Errorf("expected CreatedAt to be %v, got %v", now, record.CreatedAt)
	}
	if record.QueuedAt == nil || !record.QueuedAt.Equal(queuedAt) {
		t.Error("expected QueuedAt to be set correctly")
	}
	if record.ProcessAt == nil || !record.ProcessAt.Equal(processAt) {
		t.Error("expected ProcessAt to be set correctly")
	}
	if record.StartedAt == nil || !record.StartedAt.Equal(startedAt) {
		t.Error("expected StartedAt to be set correctly")
	}
	if record.CompletedAt == nil || !record.CompletedAt.Equal(completedAt) {
		t.Error("expected CompletedAt to be set correctly")
	}
	if record.FailedAt == nil || !record.FailedAt.Equal(failedAt) {
		t.Error("expected FailedAt to be set correctly")
	}
	if record.Error != "test-error" {
		t.Errorf("expected Error to be 'test-error', got %s", record.Error)
	}
	if record.RetryCount != 3 {
		t.Errorf("expected RetryCount to be 3, got %d", record.RetryCount)
	}
	if record.Metadata["key"] != "value" {
		t.Error("expected Metadata to be set correctly")
	}
}

func TestTaskRecord_OptionalFields(t *testing.T) {
	record := &TaskRecord{
		TaskID:    "task-123",
		Task:      &Task{Type: "test", Payload: []byte("data")},
		Status:    TaskStatusPending,
		CreatedAt: time.Now(),
	}

	// Optional fields should be nil
	if record.QueuedAt != nil {
		t.Error("expected QueuedAt to be nil")
	}
	if record.ProcessAt != nil {
		t.Error("expected ProcessAt to be nil")
	}
	if record.StartedAt != nil {
		t.Error("expected StartedAt to be nil")
	}
	if record.CompletedAt != nil {
		t.Error("expected CompletedAt to be nil")
	}
	if record.FailedAt != nil {
		t.Error("expected FailedAt to be nil")
	}
	if record.Error != "" {
		t.Error("expected Error to be empty")
	}
	if record.RetryCount != 0 {
		t.Error("expected RetryCount to be 0")
	}
	if record.Metadata != nil {
		t.Error("expected Metadata to be nil")
	}
	if len(record.Metadata) != 0 {
		t.Errorf("expected Metadata to be empty, got %d", len(record.Metadata))
	}
	if record.TaskID != "task-123" {
		t.Errorf("expected TaskID to be 'task-123', got %s", record.TaskID)
	}
	if record.Task != nil {
		t.Error("expected Task to be nil")
	}
	if record.Status != TaskStatusPending {
		t.Errorf("expected Status to be TaskStatusPending, got %v", record.Status)
	}
	if record.Queue != "" {
		t.Errorf("expected Queue to be empty, got %s", record.Queue)
	}
	if !record.CreatedAt.Equal(time.Now()) {
		t.Errorf("expected CreatedAt to be %v, got %v", time.Now(), record.CreatedAt)
	}
}

func TestTaskRecordFilter_Fields(t *testing.T) {
	now := time.Now()
	startTime := now.Add(-1 * time.Hour)
	endTime := now
	priority := PriorityHigh

	filter := &TaskRecordFilter{
		Status:    []TaskStatus{TaskStatusCompleted, TaskStatusFailed},
		Queue:     "test-queue",
		Priority:  &priority,
		StartTime: &startTime,
		EndTime:   &endTime,
		Limit:     100,
		Offset:    10,
		Metadata:  map[string]any{"key": "value"},
	}

	if len(filter.Status) != 2 {
		t.Errorf("expected Status length to be 2, got %d", len(filter.Status))
	}
	if filter.Queue != "test-queue" {
		t.Errorf("expected Queue to be 'test-queue', got %s", filter.Queue)
	}
	if filter.Priority == nil || *filter.Priority != priority {
		t.Error("expected Priority to be set correctly")
	}
	if filter.StartTime == nil || !filter.StartTime.Equal(startTime) {
		t.Error("expected StartTime to be set correctly")
	}
	if filter.EndTime == nil || !filter.EndTime.Equal(endTime) {
		t.Error("expected EndTime to be set correctly")
	}
	if filter.Limit != 100 {
		t.Errorf("expected Limit to be 100, got %d", filter.Limit)
	}
	if filter.Offset != 10 {
		t.Errorf("expected Offset to be 10, got %d", filter.Offset)
	}
	if filter.Metadata["key"] != "value" {
		t.Error("expected Metadata to be set correctly")
	}
}

func TestTaskRecordFilter_EmptyFields(t *testing.T) {
	filter := &TaskRecordFilter{}

	if filter.Status != nil {
		t.Error("expected Status to be nil")
	}
	if filter.Queue != "" {
		t.Error("expected Queue to be empty")
	}
	if filter.Priority != nil {
		t.Error("expected Priority to be nil")
	}
	if filter.StartTime != nil {
		t.Error("expected StartTime to be nil")
	}
	if filter.EndTime != nil {
		t.Error("expected EndTime to be nil")
	}
	if filter.Limit != 0 {
		t.Error("expected Limit to be 0")
	}
	if filter.Offset != 0 {
		t.Error("expected Offset to be 0")
	}
	if filter.Metadata != nil {
		t.Error("expected Metadata to be nil")
	}
}
