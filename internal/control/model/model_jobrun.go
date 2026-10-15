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
	RetryApprovalRequired     bool   `gorm:"column:retry_approval_required;not null;default:false" json:"retryApprovalRequired"`
	RetryApprovalID           string `gorm:"column:retry_approval_id;type:varchar(64);not null;default:''" json:"retryApprovalId"`
	RetryApprovalEvidenceHash string `gorm:"column:retry_approval_evidence_hash;type:varchar(67);not null;default:''" json:"retryApprovalEvidenceHash"`
	BaseModel
	JobRunID                   string     `gorm:"column:job_run_id;type:varchar(64);uniqueIndex:uk_job_run_business_id" json:"jobRunId"`
	PipelineID                 string     `gorm:"column:pipeline_id" json:"pipelineId"`
	PipelineRunID              string     `gorm:"column:pipeline_run_id;type:varchar(64);index:idx_job_run_pipeline_run" json:"pipelineRunId"`
	StageID                    string     `gorm:"column:stage_id" json:"stageId"`
	JobName                    string     `gorm:"column:job_name" json:"jobName"`
	AgentID                    string     `gorm:"column:agent_id" json:"agentId"`
	ClaimID                    string     `gorm:"column:claim_id;type:varchar(64);default:''" json:"-"`
	CancellationClaimID        string     `gorm:"column:cancellation_claim_id;type:varchar(64);not null;default:''" json:"-"` // 原作业认领，仅由控制面取消事务写入
	TerminalReportDigest       string     `gorm:"column:terminal_report_digest;type:varchar(64);not null;default:''" json:"-"`
	SourceExpectationCaptured  bool       `gorm:"column:source_expectation_captured;not null;default:false" json:"-"`
	SourceType                 string     `gorm:"column:source_type;type:varchar(32);not null;default:''" json:"-"`
	SourceRepository           string     `gorm:"column:source_repository;type:varchar(2048);not null;default:''" json:"-"`
	SourceRef                  string     `gorm:"column:source_ref;type:varchar(512);not null;default:''" json:"-"`
	SourceBindingDigest        string     `gorm:"column:source_binding_digest;type:varchar(67);not null;default:''" json:"-"`
	SourceResolvedCommit       string     `gorm:"column:source_resolved_commit;type:varchar(64);not null;default:''" json:"-"`
	SourceProvenanceState      string     `gorm:"column:source_provenance_state;type:varchar(16);not null;default:'unknown'" json:"-"`
	RuntimeType                string     `gorm:"column:runtime_type;type:varchar(64);not null;default:''" json:"runtimeType,omitempty"`
	RuntimeImage               string     `gorm:"column:runtime_image;type:varchar(512);not null;default:''" json:"runtimeImage,omitempty"`
	RuntimeCPU                 string     `gorm:"column:runtime_cpu;type:varchar(64);not null;default:''" json:"runtimeCpu,omitempty"`
	RuntimeMemory              string     `gorm:"column:runtime_memory;type:varchar(64);not null;default:''" json:"runtimeMemory,omitempty"`
	RuntimeBindingDigest       string     `gorm:"column:runtime_binding_digest;type:varchar(67);not null;default:''" json:"-"`
	RetryPlan                  string     `gorm:"column:retry_plan;type:mediumtext;not null;default:('')" json:"-"`
	RetryPlanDigest            string     `gorm:"column:retry_plan_digest;type:varchar(67);not null;default:''" json:"-"`
	CurrentGenerationID        string     `gorm:"column:current_generation_id;type:varchar(64);not null;default:''" json:"-"`
	CurrentGeneration          int        `gorm:"column:current_generation;not null;default:0" json:"-"`
	InputExpectation           string     `gorm:"column:input_expectation;type:mediumtext;not null;default:('')" json:"-"`
	InputManifestState         string     `gorm:"column:input_manifest_state;type:varchar(16);not null;default:'unknown'" json:"-"`
	InputManifest              string     `gorm:"column:input_manifest;type:mediumtext;not null;default:('')" json:"-"`
	InputManifestDigest        string     `gorm:"column:input_manifest_digest;type:varchar(67);not null;default:''" json:"-"`
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
