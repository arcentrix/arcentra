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

package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/log"
	"gorm.io/gorm"
)

// StepRunRecordManager 步骤执行记录管理器，负责将步骤执行状态写入 ClickHouse
type StepRunRecordManager struct {
	db        *gorm.DB
	tableName string
}

// NewStepRunRecordManager 创建步骤执行记录管理器
func NewStepRunRecordManager(clickHouse *gorm.DB) (*StepRunRecordManager, error) {
	if clickHouse == nil {
		return nil, nil
	}

	tableName := model.StepRunQueueRecord{}.CollectionName()
	manager := &StepRunRecordManager{
		db:        clickHouse,
		tableName: tableName,
	}

	// 创建表（如果不存在）
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := manager.createTableIfNotExists(ctx); err != nil {
		log.Warnw("failed to create step run records table", "error", err)
		// 不返回错误，允许继续运行
	}

	return manager, nil
}

// createTableIfNotExists 创建表（如果不存在）
func (m *StepRunRecordManager) createTableIfNotExists(ctx context.Context) error {
	sqlDB, err := m.db.DB()
	if err != nil {
		return fmt.Errorf("failed to get SQL DB from GORM: %w", err)
	}

	// ClickHouse 表结构
	createTableSQL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			step_run_id String,
			step_run_type String,
			status String,
			queue String,
			priority Int32,
			pipeline_id String,
			pipeline_run_id String,
			stage_id String,
			job_id String,
			job_run_id String,
			agent_id String,
			payload String,
			create_time DateTime,
			start_time Nullable(DateTime),
			end_time Nullable(DateTime),
			duration Nullable(Int64),
			retry_count Int32,
			current_retry Int32,
			error_message Nullable(String)
		) ENGINE = MergeTree()
		ORDER BY (step_run_id, create_time)
		PRIMARY KEY step_run_id
		SETTINGS index_granularity = 8192
	`, m.tableName)

	_, err = sqlDB.ExecContext(ctx, createTableSQL)
	return err
}

// RecordStepRunEnqueued 记录步骤执行入队
func (m *StepRunRecordManager) RecordStepRunEnqueued(payload *TaskPayload, queueName string) {
	if m == nil || m.db == nil {
		return
	}

	now := time.Now()
	payloadJSON, _ := json.Marshal(payload.Data)

	sqlDB, err := m.db.DB()
	if err != nil {
		log.Warnw("failed to get SQL DB from GORM", "step_run_id", payload.TaskID, "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 使用 INSERT 语句（ClickHouse 支持 INSERT ... ON DUPLICATE KEY UPDATE，但更推荐使用 ReplacingMergeTree）
	insertSQL := fmt.Sprintf(`
		INSERT INTO %s (
			step_run_id, step_run_type, status, queue, priority, pipeline_id, pipeline_run_id,
			stage_id, job_id, job_run_id, agent_id, payload, create_time, retry_count, current_retry
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, m.tableName)

	// 从 payload.Data 中提取 job_id 和 job_run_id（如果存在）
	jobID := ""
	jobRunID := ""
	if payload.Data != nil {
		if v, ok := payload.Data["job_id"].(string); ok {
			jobID = v
		}
		if v, ok := payload.Data["job_run_id"].(string); ok {
			jobRunID = v
		}
	}

	_, err = sqlDB.ExecContext(ctx, insertSQL,
		payload.TaskID,   // step_run_id
		payload.TaskType, // step_run_type
		TaskRecordStatusPending,
		queueName,
		payload.Priority,
		payload.PipelineID,
		payload.PipelineRunID,
		payload.StageID,
		jobID,
		jobRunID,
		payload.AgentID,
		string(payloadJSON),
		now,
		payload.RetryCount,
		0,
	)
	if err != nil {
		log.Warnw("failed to record step run enqueued", "step_run_id", payload.TaskID, "error", err)
	}
}

// RecordStepRunStarted 记录步骤执行开始
func (m *StepRunRecordManager) RecordStepRunStarted(payload *TaskPayload) {
	if m == nil || m.db == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sqlDB, err := m.db.DB()
	if err != nil {
		log.Warnw("failed to get SQL DB from GORM", "step_run_id", payload.TaskID, "error", err)
		return
	}

	now := time.Now()
	// ClickHouse 使用 ALTER TABLE UPDATE 或 INSERT 来更新数据
	// 由于 ClickHouse 是列式数据库，更适合使用 INSERT 覆盖旧数据
	// 这里我们使用 ALTER TABLE UPDATE（需要表引擎支持）
	updateSQL := fmt.Sprintf(`
		ALTER TABLE %s UPDATE status = ?, start_time = ? WHERE step_run_id = ?
	`, m.tableName)

	_, err = sqlDB.ExecContext(ctx, updateSQL, TaskRecordStatusRunning, now, payload.TaskID)
	if err != nil {
		log.Warnw("failed to record step run started", "step_run_id", payload.TaskID, "error", err)
		// 如果 ALTER UPDATE 失败，尝试使用 INSERT 覆盖
		m.insertOrUpdateStepRun(ctx, payload.TaskID, TaskRecordStatusRunning, &now, nil, nil, nil)
	}
}

// RecordStepRunCompleted 记录步骤执行完成
func (m *StepRunRecordManager) RecordStepRunCompleted(payload *TaskPayload) {
	if m == nil || m.db == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sqlDB, err := m.db.DB()
	if err != nil {
		log.Warnw("failed to get SQL DB from GORM", "step_run_id", payload.TaskID, "error", err)
		return
	}

	now := time.Now()

	// 先获取开始时间以计算耗时
	var startTime sql.NullTime
	var duration sql.NullInt64
	selectSQL := fmt.Sprintf(`SELECT start_time FROM %s WHERE step_run_id = ?`, m.tableName)
	row := sqlDB.QueryRowContext(ctx, selectSQL, payload.TaskID)
	if err := row.Scan(&startTime); err == nil && startTime.Valid {
		durationValue := now.Sub(startTime.Time).Milliseconds()
		duration = sql.NullInt64{Int64: durationValue, Valid: true}
	}

	updateSQL := fmt.Sprintf(`
		ALTER TABLE %s UPDATE status = ?, end_time = ?, duration = ? WHERE step_run_id = ?
	`, m.tableName)

	_, err = sqlDB.ExecContext(ctx, updateSQL, TaskRecordStatusCompleted, now, duration, payload.TaskID)
	if err != nil {
		log.Warnw("failed to record step run completed", "step_run_id", payload.TaskID, "error", err)
		// 如果 ALTER UPDATE 失败，尝试使用 INSERT 覆盖
		m.insertOrUpdateStepRun(ctx, payload.TaskID, TaskRecordStatusCompleted, nil, &now, &duration, nil)
	}
}

// RecordStepRunFailed 记录步骤执行失败
func (m *StepRunRecordManager) RecordStepRunFailed(payload *TaskPayload, err error) {
	if m == nil || m.db == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sqlDB, dbErr := m.db.DB()
	if dbErr != nil {
		log.Warnw("failed to get SQL DB from GORM", "step_run_id", payload.TaskID, "error", dbErr)
		return
	}

	now := time.Now()
	errorMsg := ""
	if err != nil {
		errorMsg = err.Error()
	}

	// 先获取开始时间以计算耗时
	var startTime sql.NullTime
	var duration sql.NullInt64
	selectSQL := fmt.Sprintf(`SELECT start_time FROM %s WHERE step_run_id = ?`, m.tableName)
	row := sqlDB.QueryRowContext(ctx, selectSQL, payload.TaskID)
	if scanErr := row.Scan(&startTime); scanErr == nil && startTime.Valid {
		durationValue := now.Sub(startTime.Time).Milliseconds()
		duration = sql.NullInt64{Int64: durationValue, Valid: true}
	}

	updateSQL := fmt.Sprintf(`
		ALTER TABLE %s UPDATE status = ?, error_message = ?, end_time = ?, duration = ? WHERE step_run_id = ?
	`, m.tableName)

	_, updateErr := sqlDB.ExecContext(ctx, updateSQL, TaskRecordStatusFailed, errorMsg, now, duration, payload.TaskID)
	if updateErr != nil {
		log.Warnw("failed to record step run failed", "step_run_id", payload.TaskID, "error", updateErr)
		// 如果 ALTER UPDATE 失败，尝试使用 INSERT 覆盖
		m.insertOrUpdateStepRun(ctx, payload.TaskID, TaskRecordStatusFailed, nil, &now, &duration, &errorMsg)
	}
}

// insertOrUpdateStepRun 插入或更新步骤执行记录（备用方法）
func (m *StepRunRecordManager) insertOrUpdateStepRun(ctx context.Context, stepRunID, status string, startTime, endTime *time.Time, duration *sql.NullInt64, errorMsg *string) {
	sqlDB, err := m.db.DB()
	if err != nil {
		return
	}

	// 先查询现有记录
	var existingRecord struct {
		StepRunType   string
		Queue         string
		Priority      int32
		PipelineID    string
		PipelineRunID string
		StageID       string
		JobID         string
		JobRunID      string
		AgentID       string
		Payload       string
		CreateTime    time.Time
		RetryCount    int32
		CurrentRetry  int32
	}

	selectSQL := fmt.Sprintf(`SELECT step_run_type, queue, priority, pipeline_id, pipeline_run_id, stage_id, job_id, job_run_id, agent_id, payload, create_time, retry_count, current_retry FROM %s WHERE step_run_id = ?`, m.tableName)
	row := sqlDB.QueryRowContext(ctx, selectSQL, stepRunID)
	if err := row.Scan(&existingRecord.StepRunType, &existingRecord.Queue, &existingRecord.Priority,
		&existingRecord.PipelineID, &existingRecord.PipelineRunID, &existingRecord.StageID,
		&existingRecord.JobID, &existingRecord.JobRunID, &existingRecord.AgentID, &existingRecord.Payload,
		&existingRecord.CreateTime, &existingRecord.RetryCount, &existingRecord.CurrentRetry); err != nil {
		// 记录不存在，无法更新
		return
	}

	// 插入新记录（覆盖旧记录）
	insertSQL := fmt.Sprintf(`
		INSERT INTO %s (
			step_run_id, step_run_type, status, queue, priority, pipeline_id, pipeline_run_id,
			stage_id, job_id, job_run_id, agent_id, payload, create_time, start_time, end_time, duration,
			retry_count, current_retry, error_message
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, m.tableName)

	var durationValue interface{}
	if duration != nil && duration.Valid {
		durationValue = duration.Int64
	} else {
		durationValue = nil
	}

	var errorMsgValue interface{}
	if errorMsg != nil {
		errorMsgValue = *errorMsg
	} else {
		errorMsgValue = nil
	}

	_, err = sqlDB.ExecContext(ctx, insertSQL,
		stepRunID,
		existingRecord.StepRunType,
		status,
		existingRecord.Queue,
		existingRecord.Priority,
		existingRecord.PipelineID,
		existingRecord.PipelineRunID,
		existingRecord.StageID,
		existingRecord.JobID,
		existingRecord.JobRunID,
		existingRecord.AgentID,
		existingRecord.Payload,
		existingRecord.CreateTime,
		startTime,
		endTime,
		durationValue,
		existingRecord.RetryCount,
		existingRecord.CurrentRetry,
		errorMsgValue,
	)
	if err != nil {
		log.Warnw("failed to insert/update step run record", "step_run_id", stepRunID, "error", err)
	}
}
