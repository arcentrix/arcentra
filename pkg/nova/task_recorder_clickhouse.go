// Copyright 2025 Arcade Team
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

	"github.com/arcentrix/arcade/pkg/log"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	// TaskRecordTableName is the default ClickHouse table name for task records
	TaskRecordTableName = "l_task_records"
)

// TaskRecordModel represents the GORM model for task records in ClickHouse
type TaskRecordModel struct {
	TaskID      string         `gorm:"column:task_id;type:String;primaryKey" json:"taskId"`
	TaskType    string         `gorm:"column:task_type;type:String" json:"taskType"`
	TaskPayload []byte         `gorm:"column:task_payload;type:String" json:"taskPayload"`
	Status      string         `gorm:"column:status;type:String;index" json:"status"`
	Queue       string         `gorm:"column:queue;type:String;index" json:"queue"`
	Priority    int            `gorm:"column:priority;type:Int32;index" json:"priority"`
	CreatedAt   time.Time      `gorm:"column:created_at;type:DateTime;index" json:"createdAt"`
	QueuedAt    *time.Time     `gorm:"column:queued_at;type:DateTime;index" json:"queuedAt,omitempty"`
	ProcessAt   *time.Time     `gorm:"column:process_at;type:DateTime" json:"processAt,omitempty"`
	StartedAt   *time.Time     `gorm:"column:started_at;type:DateTime" json:"startedAt,omitempty"`
	CompletedAt *time.Time     `gorm:"column:completed_at;type:DateTime;index" json:"completedAt,omitempty"`
	FailedAt    *time.Time     `gorm:"column:failed_at;type:DateTime" json:"failedAt,omitempty"`
	Error       string         `gorm:"column:error;type:String" json:"error,omitempty"`
	RetryCount  int            `gorm:"column:retry_count;type:Int32" json:"retryCount"`
	Metadata    datatypes.JSON `gorm:"column:metadata;type:String" json:"metadata,omitempty"`
}

func (TaskRecordModel) TableName() string {
	return TaskRecordTableName
}

// ClickHouseTaskRecorder implements TaskRecorder interface using ClickHouse
type ClickHouseTaskRecorder struct {
	db        *gorm.DB
	tableName string
}

// NewClickHouseTaskRecorder creates a new ClickHouse task recorder
// clickHouseDB: ClickHouse database connection (*gorm.DB)
// tableName: table name, if empty, uses default TaskRecordTableName
func NewClickHouseTaskRecorder(clickHouseDB *gorm.DB, tableName string) (*ClickHouseTaskRecorder, error) {
	if clickHouseDB == nil {
		return nil, fmt.Errorf("clickHouseDB cannot be nil")
	}

	if tableName == "" {
		tableName = TaskRecordTableName
	}

	recorder := &ClickHouseTaskRecorder{
		db:        clickHouseDB,
		tableName: tableName,
	}

	// Create table if not exists
	if err := recorder.createTable(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to create table: %w", err)
	}

	return recorder, nil
}

// createTable creates the task records table in ClickHouse
func (r *ClickHouseTaskRecorder) createTable(ctx context.Context) error {
	// ClickHouse CREATE TABLE IF NOT EXISTS statement
	createTableSQL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			task_id String,
			task_type String,
			task_payload String,
			status String,
			queue String,
			priority Int32,
			created_at DateTime,
			queued_at Nullable(DateTime),
			process_at Nullable(DateTime),
			started_at Nullable(DateTime),
			completed_at Nullable(DateTime),
			failed_at Nullable(DateTime),
			error String,
			retry_count Int32,
			metadata String,
			INDEX idx_status status TYPE minmax GRANULARITY 3,
			INDEX idx_queue queue TYPE minmax GRANULARITY 3,
			INDEX idx_priority priority TYPE minmax GRANULARITY 3,
			INDEX idx_created_at created_at TYPE minmax GRANULARITY 3,
			INDEX idx_queued_at queued_at TYPE minmax GRANULARITY 3,
			INDEX idx_completed_at completed_at TYPE minmax GRANULARITY 3
		) ENGINE = ReplacingMergeTree(created_at)
		ORDER BY (task_id)
		SETTINGS index_granularity = 8192
	`, r.tableName)

	if err := r.db.Exec(createTableSQL).Error; err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	log.Info("ClickHouse task records table created successfully", "table", r.tableName)
	return nil
}

// Record records a task in ClickHouse
// Uses REPLACE INTO for upsert operation
func (r *ClickHouseTaskRecorder) Record(ctx context.Context, record *TaskRecord) error {
	if record == nil {
		return fmt.Errorf("task record cannot be nil")
	}

	model := r.taskRecordToModel(record)

	// ClickHouse uses INSERT with ReplacingMergeTree engine for upsert behavior
	// We use INSERT with ON DUPLICATE KEY UPDATE equivalent behavior
	if err := r.db.WithContext(ctx).Table(r.tableName).Create(&model).Error; err != nil {
		return fmt.Errorf("failed to record task: %w", err)
	}

	return nil
}

// UpdateStatus updates the task status in ClickHouse
// Since ClickHouse doesn't support UPDATE well, we insert a new record with updated status
// ReplacingMergeTree will handle deduplication based on ORDER BY key
func (r *ClickHouseTaskRecorder) UpdateStatus(ctx context.Context, taskID string, status TaskStatus, err error) error {
	if taskID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}

	// First, get the existing record
	existing, getErr := r.Get(ctx, taskID)
	if getErr != nil {
		return fmt.Errorf("failed to get task record: %w", getErr)
	}

	// Update the status and timestamp fields
	existing.Status = status
	now := time.Now()
	switch status {
	case TaskStatusProcessing:
		existing.StartedAt = &now
	case TaskStatusCompleted:
		existing.CompletedAt = &now
	case TaskStatusFailed:
		existing.FailedAt = &now
		if err != nil {
			existing.Error = err.Error()
		}
	}

	// Insert updated record (ReplacingMergeTree will replace old one)
	return r.Record(ctx, existing)
}

// Get retrieves a task record by task ID
func (r *ClickHouseTaskRecorder) Get(ctx context.Context, taskID string) (*TaskRecord, error) {
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
func (r *ClickHouseTaskRecorder) ListTaskRecords(ctx context.Context, filter *TaskRecordFilter) ([]*TaskRecord, error) {
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
				// ClickHouse JSON functions
				query = query.Where("JSONExtractString(metadata, ?) = ?", k, v)
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
// ClickHouse supports ALTER TABLE DELETE for deletion
func (r *ClickHouseTaskRecorder) Delete(ctx context.Context, taskID string) error {
	if taskID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}

	// ClickHouse uses ALTER TABLE DELETE for deletion
	deleteSQL := fmt.Sprintf("ALTER TABLE %s DELETE WHERE task_id = ?", r.tableName)

	result := r.db.WithContext(ctx).Exec(deleteSQL, taskID)
	if result.Error != nil {
		return fmt.Errorf("failed to delete task record: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("task record not found: %s", taskID)
	}

	return nil
}

// taskRecordToModel converts TaskRecord to TaskRecordModel
func (r *ClickHouseTaskRecorder) taskRecordToModel(record *TaskRecord) TaskRecordModel {
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
			model.Metadata = datatypes.JSON(metadataJSON)
		}
	}

	return model
}

// modelToTaskRecord converts TaskRecordModel to TaskRecord
func (r *ClickHouseTaskRecorder) modelToTaskRecord(model *TaskRecordModel) (*TaskRecord, error) {
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
