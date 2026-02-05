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

// StepRunLog 步骤执行日志(ClickHouse Table: l_step_run_logs)
type StepRunLog struct {
	LogId         string    `gorm:"column:log_id;type:String" json:"logId"`
	StepRunId     string    `gorm:"column:step_run_id;type:String;index" json:"stepRunId"`
	PipelineRunId string    `gorm:"column:pipeline_run_id;type:String;index" json:"pipelineRunId"`
	AgentId       string    `gorm:"column:agent_id;type:String;index" json:"agentId"`
	LineNumber    int       `gorm:"column:line_number;type:Int32" json:"lineNumber"`
	Content       string    `gorm:"column:content;type:String" json:"content"`
	Timestamp     time.Time `gorm:"column:timestamp;type:DateTime;index" json:"timestamp"`
	Level         string    `gorm:"column:level;type:String" json:"level"` // INFO/WARN/ERROR/DEBUG
	CreatedAt     time.Time `gorm:"column:created_at;type:DateTime" json:"createdAt"`
}

// TerminalLog 终端日志/构建日志(ClickHouse Table: l_terminal_logs)
type TerminalLog struct {
	SessionId        string              `gorm:"column:session_id;type:String;primaryKey" json:"sessionId"`
	SessionType      string              `gorm:"column:session_type;type:String" json:"sessionType"`      // build/deploy/release/debug
	Environment      string              `gorm:"column:environment;type:String;index" json:"environment"` // dev/test/staging/prod
	StepRunId        string              `gorm:"column:step_run_id;type:String;index" json:"stepRunId,omitempty"`
	PipelineId       string              `gorm:"column:pipeline_id;type:String" json:"pipelineId,omitempty"`
	PipelineRunId    string              `gorm:"column:pipeline_run_id;type:String;index" json:"pipelineRunId,omitempty"`
	UserId           string              `gorm:"column:user_id;type:String;index" json:"userId"`
	Hostname         string              `gorm:"column:hostname;type:String" json:"hostname"`
	WorkingDirectory string              `gorm:"column:working_directory;type:String" json:"workingDirectory"`
	Command          string              `gorm:"column:command;type:String" json:"command"`
	ExitCode         *int                `gorm:"column:exit_code;type:Int32" json:"exitCode,omitempty"`
	Logs             []TerminalLogLine   `gorm:"column:logs;type:String" json:"logs"`           // JSON 字符串
	Metadata         TerminalLogMetadata `gorm:"column:metadata;type:String" json:"metadata"`   // JSON 字符串
	Status           string              `gorm:"column:status;type:String;index" json:"status"` // running/completed/failed/timeout
	StartTime        time.Time           `gorm:"column:start_time;type:DateTime" json:"startTime"`
	EndTime          *time.Time          `gorm:"column:end_time;type:DateTime" json:"endTime,omitempty"`
	CreatedAt        time.Time           `gorm:"column:created_at;type:DateTime;index" json:"createdAt"`
	UpdatedAt        time.Time           `gorm:"column:updated_at;type:DateTime" json:"updatedAt"`
}

// TerminalLogLine 终端日志行
type TerminalLogLine struct {
	Line      int       `json:"line"`
	Timestamp time.Time `json:"timestamp"`
	Content   string    `json:"content"`
	Stream    string    `json:"stream"` // stdout/stderr
}

// TerminalLogMetadata 终端日志元数据
type TerminalLogMetadata struct {
	TotalLines      int   `json:"totalLines"`
	DurationMs      int64 `json:"durationMs"`
	OutputSizeBytes int64 `json:"outputSizeBytes"`
}

// BuildArtifactLog 产物构建日志(ClickHouse Table: l_build_artifacts_logs)
type BuildArtifactLog struct {
	ArtifactId   string    `gorm:"column:artifact_id;type:String;index" json:"artifactId"`
	StepRunId    string    `gorm:"column:step_run_id;type:String;index" json:"stepRunId"`
	Operation    string    `gorm:"column:operation;type:String;index" json:"operation"` // upload/download/delete
	FileName     string    `gorm:"column:file_name;type:String" json:"fileName"`
	FileSize     int64     `gorm:"column:file_size;type:Int64" json:"fileSize"`
	StorageType  string    `gorm:"column:storage_type;type:String" json:"storageType"` // minio/s3/oss/gcs/cos
	StoragePath  string    `gorm:"column:storage_path;type:String" json:"storagePath"`
	UserId       string    `gorm:"column:user_id;type:String;index" json:"userId"`
	Status       string    `gorm:"column:status;type:String" json:"status"` // success/failed
	ErrorMessage string    `gorm:"column:error_message;type:String" json:"errorMessage,omitempty"`
	DurationMs   int64     `gorm:"column:duration_ms;type:Int64" json:"durationMs"`
	Timestamp    time.Time `gorm:"column:timestamp;type:DateTime;index" json:"timestamp"`
}
