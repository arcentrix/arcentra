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

package model

import (
	"time"
)

// StepRunQueueRecord queue 步骤执行记录（ClickHouse）
type StepRunQueueRecord struct {
	StepRunID     string         `gorm:"column:step_run_id;type:String" json:"step_run_id"`
	StepRunType   string         `gorm:"column:step_run_type;type:String" json:"step_run_type"`
	Status        string         `gorm:"column:status;type:String" json:"status"` // pending/running/completed/failed
	Priority      int            `gorm:"column:priority;type:Int32" json:"priority"`
	Queue         string         `gorm:"column:queue;type:String" json:"queue"`
	PipelineID    string         `gorm:"column:pipeline_id;type:String" json:"pipeline_id,omitempty"`
	PipelineRunID string         `gorm:"column:pipeline_run_id;type:String" json:"pipeline_run_id,omitempty"`
	StageID       string         `gorm:"column:stage_id;type:String" json:"stage_id,omitempty"`
	JobID         string         `gorm:"column:job_id;type:String" json:"job_id,omitempty"`
	JobRunID      string         `gorm:"column:job_run_id;type:String" json:"job_run_id,omitempty"`
	AgentID       string         `gorm:"column:agent_id;type:String" json:"agent_id,omitempty"`
	Payload       map[string]any `gorm:"column:payload;type:String" json:"payload"` // 步骤执行负载数据（JSON 字符串）
	ErrorMessage  string         `gorm:"column:error_message;type:String" json:"error_message,omitempty"`
	CreateTime    time.Time      `gorm:"column:create_time;type:DateTime" json:"create_time"`
	StartTime     *time.Time     `gorm:"column:start_time;type:DateTime" json:"start_time,omitempty"`
	EndTime       *time.Time     `gorm:"column:end_time;type:DateTime" json:"end_time,omitempty"`
	Duration      int64          `gorm:"column:duration;type:Int64" json:"duration,omitempty"` // 毫秒
	RetryCount    int            `gorm:"column:retry_count;type:Int32" json:"retry_count"`
	CurrentRetry  int            `gorm:"column:current_retry;type:Int32" json:"current_retry"`
}

// CollectionName 返回表名称（ClickHouse）
func (StepRunQueueRecord) CollectionName() string {
	return "l_step_run_queue_records"
}
