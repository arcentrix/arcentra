// Copyright 2026 Arcentra Authors.
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

// JobRun represents a single job execution record.
// Job is the minimum schedulable unit; an Agent receives a complete JobRun
// and executes all its Steps sequentially.
type JobRun struct {
	BaseModel
	JobRunID                   string     `gorm:"column:job_run_id;type:varchar(64);uniqueIndex:uk_job_run_business_id" json:"jobRunId"`
	PipelineID                 string     `gorm:"column:pipeline_id" json:"pipelineId"`
	PipelineRunID              string     `gorm:"column:pipeline_run_id;type:varchar(64);index:idx_job_run_pipeline_run" json:"pipelineRunId"`
	StageID                    string     `gorm:"column:stage_id" json:"stageId"`
	JobName                    string     `gorm:"column:job_name" json:"jobName"`
	AgentID                    string     `gorm:"column:agent_id" json:"agentId"`
	ClaimID                    string     `gorm:"column:claim_id;type:varchar(64);default:''" json:"-"`
	TerminalReportDigest       string     `gorm:"column:terminal_report_digest;type:varchar(64);not null;default:''" json:"-"`
	Status                     int        `gorm:"column:status" json:"status"`
	Priority                   int        `gorm:"column:priority" json:"priority"`
	Env                        string     `gorm:"column:env;type:json" json:"env"`
	Workspace                  string     `gorm:"column:workspace" json:"workspace"`
	Timeout                    string     `gorm:"column:timeout" json:"timeout"`
	TotalSteps                 int        `gorm:"column:total_steps" json:"totalSteps"`
	CompletedSteps             int        `gorm:"column:completed_steps" json:"completedSteps"`
	FailedSteps                int        `gorm:"column:failed_steps" json:"failedSteps"`
	ErrorMessage               string     `gorm:"column:error_message;type:text" json:"errorMessage"`
	ArtifactStorageID          string     `gorm:"column:artifact_storage_id;type:varchar(64)" json:"artifactStorageId"`
	ArtifactStorageFingerprint string     `gorm:"column:artifact_storage_fingerprint;type:varchar(64)" json:"artifactStorageFingerprint"`
	ArtifactStorageType        string     `gorm:"column:artifact_storage_type;type:varchar(32)" json:"artifactStorageType"`
	ArtifactReferences         string     `gorm:"column:artifact_references;type:json" json:"artifactReferences"`
	ArtifactURIs               string     `gorm:"column:artifact_uris;type:json" json:"artifactUris"`
	StartTime                  *time.Time `gorm:"column:start_time" json:"startTime"`
	EndTime                    *time.Time `gorm:"column:end_time" json:"endTime"`
	Duration                   int64      `gorm:"column:duration" json:"duration"`
}

// TableName returns the database table name.
func (JobRun) TableName() string {
	return "job_run"
}

// JobRun status constants align with StepRun status for consistency.
const (
	JobRunStatusPending   = 1
	JobRunStatusQueued    = 2
	JobRunStatusRunning   = 3
	JobRunStatusSuccess   = 4
	JobRunStatusFailed    = 5
	JobRunStatusCancelled = 6
	JobRunStatusTimeout   = 7
	JobRunStatusSkipped   = 8
)

// IsJobRunTerminal returns true when the status represents a final state.
func IsJobRunTerminal(status int) bool {
	return status == JobRunStatusSuccess ||
		status == JobRunStatusFailed ||
		status == JobRunStatusCancelled ||
		status == JobRunStatusTimeout ||
		status == JobRunStatusSkipped
}

// IsJobRunRunning returns true when the job is actively executing.
func IsJobRunRunning(status int) bool {
	return status == JobRunStatusRunning
}
