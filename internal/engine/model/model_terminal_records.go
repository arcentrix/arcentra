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

import "time"

// TerminalOutputRecord 终端输出记录
type TerminalOutputRecord struct {
	SessionId        string                 `gorm:"column:session_id;type:VARCHAR(64);primaryKey" json:"sessionId"`
	SessionType      string                 `gorm:"column:session_type;type:VARCHAR(32)" json:"sessionType"`      // build/deploy/release/debug
	Environment      string                 `gorm:"column:environment;type:VARCHAR(32);index" json:"environment"` // dev/test/staging/prod
	StepRunId        string                 `gorm:"column:step_run_id;type:VARCHAR(64);index" json:"stepRunId,omitempty"`
	PipelineId       string                 `gorm:"column:pipeline_id;type:VARCHAR(64)" json:"pipelineId,omitempty"`
	PipelineRunId    string                 `gorm:"column:pipeline_run_id;type:VARCHAR(64);index" json:"pipelineRunId,omitempty"`
	UserId           string                 `gorm:"column:user_id;type:VARCHAR(64);index" json:"userId"`
	Hostname         string                 `gorm:"column:hostname;type:VARCHAR(255)" json:"hostname"`
	WorkingDirectory string                 `gorm:"column:working_directory;type:VARCHAR(255)" json:"workingDirectory"`
	Command          string                 `gorm:"column:command;type:TEXT" json:"command"`
	ExitCode         *int                   `gorm:"column:exit_code;type:INT" json:"exitCode,omitempty"`
	Logs             []TerminalOutputLine   `gorm:"column:logs;type:JSON" json:"logs"`                  // JSON 字符串
	Metadata         TerminalOutputMetadata `gorm:"column:metadata;type:JSON" json:"metadata"`          // JSON 字符串
	Status           string                 `gorm:"column:status;type:VARCHAR(32);index" json:"status"` // running/completed/failed/timeout
	StartTime        time.Time              `gorm:"column:start_time;type:DATETIME" json:"startTime"`
	EndTime          *time.Time             `gorm:"column:end_time;type:DATETIME" json:"endTime,omitempty"`
	CreatedAt        time.Time              `gorm:"column:created_at;type:DATETIME;index" json:"createdAt"`
	UpdatedAt        time.Time              `gorm:"column:updated_at;type:DATETIME" json:"updatedAt"`
}

// TerminalOutputLine 终端输出行
type TerminalOutputLine struct {
	Line      int       `json:"line"`
	Timestamp time.Time `json:"timestamp"`
	Content   string    `json:"content"`
	Stream    string    `json:"stream"` // stdout/stderr
}

// TerminalOutputMetadata 终端输出元数据
type TerminalOutputMetadata struct {
	TotalLines      int   `json:"totalLines"`
	DurationMs      int64 `json:"durationMs"`
	OutputSizeBytes int64 `json:"outputSizeBytes"`
}

// TableName 返回表名称
func (TerminalOutputRecord) TableName() string {
	return "l_terminal_output_records"
}
