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

package nova

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
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

const (
	// TaskRecordTableName is the default MySQL table name for task records
	TaskRecordTableName = "l_task_records"
)

// TaskRecordModel represents the GORM model for task records in MySQL
type TaskRecordModel struct {
	TaskID      string         `gorm:"column:task_id;type:VARCHAR(64);primaryKey" json:"taskId"`
	TaskType    string         `gorm:"column:task_type;type:VARCHAR(64)" json:"taskType"`
	TaskPayload []byte         `gorm:"column:task_payload;type:JSON" json:"taskPayload"`
	Status      string         `gorm:"column:status;type:VARCHAR(32);index" json:"status"`
	Queue       string         `gorm:"column:queue;type:VARCHAR(64);index" json:"queue"`
	Priority    int            `gorm:"column:priority;type:INT;index" json:"priority"`
	CreatedAt   time.Time      `gorm:"column:created_at;type:DATETIME;index" json:"createdAt"`
	QueuedAt    *time.Time     `gorm:"column:queued_at;type:DATETIME;index" json:"queuedAt,omitempty"`
	ProcessAt   *time.Time     `gorm:"column:process_at;type:DATETIME" json:"processAt,omitempty"`
	StartedAt   *time.Time     `gorm:"column:started_at;type:DATETIME" json:"startedAt,omitempty"`
	CompletedAt *time.Time     `gorm:"column:completed_at;type:DATETIME;index" json:"completedAt,omitempty"`
	FailedAt    *time.Time     `gorm:"column:failed_at;type:DATETIME" json:"failedAt,omitempty"`
	Error       string         `gorm:"column:error;type:TEXT" json:"error,omitempty"`
	RetryCount  int            `gorm:"column:retry_count;type:INT" json:"retryCount"`
	Metadata    datatypes.JSON `gorm:"column:metadata;type:JSON" json:"metadata,omitempty"`
}

func (TaskRecordModel) TableName() string {
	return TaskRecordTableName
}

// MySQLTaskRecorder implements TaskRecorder interface using MySQL
type MySQLTaskRecorder struct {
	db        *gorm.DB
	tableName string
}

// NewTaskRecorder NewMySQLTaskRecorder creates a new MySQL task recorder
// mysqlDB: MySQL database connection (*gorm.DB)
// tableName: table name, if empty, uses default TaskRecordTableName
func NewTaskRecorder(mysqlDB *gorm.DB, tableName string) (*MySQLTaskRecorder, error) {
	if mysqlDB == nil {
		return nil, fmt.Errorf("mysqlDB cannot be nil")
	}

	if tableName == "" {
		tableName = TaskRecordTableName
	}

	recorder := &MySQLTaskRecorder{
		db:        mysqlDB,
		tableName: tableName,
	}

	return recorder, nil
}

// Record records a task in MySQL
func (r *MySQLTaskRecorder) Record(ctx context.Context, record *TaskRecord) error {
	if record == nil {
		return fmt.Errorf("task record cannot be nil")
	}

	model := r.taskRecordToModel(record)
	if err := r.db.WithContext(ctx).Table(r.tableName).Save(&model).Error; err != nil {
		return fmt.Errorf("failed to record task: %w", err)
	}

	return nil
}

// UpdateStatus updates the task status in MySQL
func (r *MySQLTaskRecorder) UpdateStatus(ctx context.Context, taskID string, status TaskStatus, err error) error {
	if taskID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}

	now := time.Now()
	updates := map[string]any{
		"status": string(status),
	}

	switch status {
	case TaskStatusProcessing:
		updates["started_at"] = &now
	case TaskStatusCompleted:
		updates["completed_at"] = &now
	case TaskStatusFailed:
		updates["failed_at"] = &now
		if err != nil {
			updates["error"] = err.Error()
		}
	}

	result := r.db.WithContext(ctx).Table(r.tableName).
		Where("task_id = ?", taskID).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to update task status: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("task record not found: %s", taskID)
	}

	return nil
}

// Get retrieves a task record by task ID
func (r *MySQLTaskRecorder) Get(ctx context.Context, taskID string) (*TaskRecord, error) {
	if taskID == "" {
		return nil, fmt.Errorf("task ID cannot be empty")
	}

	var model TaskRecordModel
	if err := r.db.WithContext(ctx).Table(r.tableName).
		Where("task_id = ?", taskID).
		First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("task record not found: %s", taskID)
		}
		return nil, fmt.Errorf("failed to get task record: %w", err)
	}

	return r.modelToTaskRecord(&model)
}

// ListTaskRecords lists task records based on filter criteria
// Results are sorted by created_at in descending order
func (r *MySQLTaskRecorder) ListTaskRecords(ctx context.Context, filter *TaskRecordFilter) ([]*TaskRecord, error) {
	query := r.db.WithContext(ctx).Table(r.tableName)

	// Apply filters
	if filter != nil {
		if len(filter.Status) > 0 {
			statuses := make([]string, len(filter.Status))
			for i, s := range filter.Status {
				statuses[i] = string(s)
			}
			query = query.Where("status IN ?", statuses)
		}
		if filter.Queue != "" {
			query = query.Where("queue = ?", filter.Queue)
		}
		if filter.Priority != nil {
			query = query.Where("priority = ?", int(*filter.Priority))
		}
		if filter.StartTime != nil || filter.EndTime != nil {
			if filter.StartTime != nil && filter.EndTime != nil {
				query = query.Where("created_at >= ? AND created_at <= ?", *filter.StartTime, *filter.EndTime)
			} else if filter.StartTime != nil {
				query = query.Where("created_at >= ?", *filter.StartTime)
			} else if filter.EndTime != nil {
				query = query.Where("created_at <= ?", *filter.EndTime)
			}
		}
		if len(filter.Metadata) > 0 {
			for k, v := range filter.Metadata {
				jsonPath := fmt.Sprintf("$.%s", k)
				query = query.Where("JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?", jsonPath, v)
			}
		}
	}

	// Apply sorting
	query = query.Order("created_at DESC")

	// Apply pagination
	if filter != nil {
		if filter.Limit > 0 {
			query = query.Limit(filter.Limit)
		}
		if filter.Offset > 0 {
			query = query.Offset(filter.Offset)
		}
	}

	var models []TaskRecordModel
	if err := query.Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list task records: %w", err)
	}

	records := make([]*TaskRecord, 0, len(models))
	for _, model := range models {
		record, err := r.modelToTaskRecord(&model)
		if err != nil {
			continue // Skip invalid records
		}
		records = append(records, record)
	}

	return records, nil
}

// Delete deletes a task record by task ID
func (r *MySQLTaskRecorder) Delete(ctx context.Context, taskID string) error {
	if taskID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}

	result := r.db.WithContext(ctx).Table(r.tableName).
		Where("task_id = ?", taskID).
		Delete(&TaskRecordModel{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete task record: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("task record not found: %s", taskID)
	}

	return nil
}

// taskRecordToModel converts TaskRecord to TaskRecordModel
func (r *MySQLTaskRecorder) taskRecordToModel(record *TaskRecord) TaskRecordModel {
	model := TaskRecordModel{
		TaskID:      record.TaskID,
		Status:      string(record.Status),
		Queue:       record.Queue,
		Priority:    int(record.Priority),
		CreatedAt:   record.CreatedAt,
		QueuedAt:    record.QueuedAt,
		ProcessAt:   record.ProcessAt,
		StartedAt:   record.StartedAt,
		CompletedAt: record.CompletedAt,
		FailedAt:    record.FailedAt,
		Error:       record.Error,
		RetryCount:  record.RetryCount,
	}

	if record.Task != nil {
		model.TaskType = record.Task.Type
		model.TaskPayload = record.Task.Payload
	}

	if len(record.Metadata) > 0 {
		metadataJSON, err := json.Marshal(record.Metadata)
		if err == nil {
			model.Metadata = metadataJSON
		}
	}

	return model
}

// modelToTaskRecord converts TaskRecordModel to TaskRecord
func (r *MySQLTaskRecorder) modelToTaskRecord(model *TaskRecordModel) (*TaskRecord, error) {
	record := &TaskRecord{
		TaskID:      model.TaskID,
		Status:      TaskStatus(model.Status),
		Queue:       model.Queue,
		Priority:    Priority(model.Priority),
		CreatedAt:   model.CreatedAt,
		QueuedAt:    model.QueuedAt,
		ProcessAt:   model.ProcessAt,
		StartedAt:   model.StartedAt,
		CompletedAt: model.CompletedAt,
		FailedAt:    model.FailedAt,
		Error:       model.Error,
		RetryCount:  model.RetryCount,
	}

	if model.TaskType != "" || len(model.TaskPayload) > 0 {
		record.Task = &Task{
			Type:    model.TaskType,
			Payload: model.TaskPayload,
		}
	}

	if len(model.Metadata) > 0 {
		var metadata map[string]any
		if err := json.Unmarshal(model.Metadata, &metadata); err == nil {
			record.Metadata = metadata
		}
	}

	return record, nil
}
