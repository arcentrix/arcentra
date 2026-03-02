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

package model

import (
	"time"
)

// Pipeline 流水线定义表
type Pipeline struct {
	BaseModel
	PipelineId        string     `gorm:"column:pipeline_id" json:"pipelineId"`
	ProjectId         string     `gorm:"column:project_id" json:"projectId"`
	Name              string     `gorm:"column:name" json:"name"`
	Description       string     `gorm:"column:description" json:"description"`
	RepoUrl           string     `gorm:"column:repo_url" json:"repoUrl"`
	DefaultBranch     string     `gorm:"column:default_branch" json:"defaultBranch"`
	PipelineFilePath  string     `gorm:"column:pipeline_file_path" json:"pipelineFilePath"`
	Status            int        `gorm:"column:status" json:"status"` // 0:unknown 1:pending 2:running 3:success 4:failed 5:cancelled 6:paused
	SaveMode          int        `gorm:"column:save_mode" json:"saveMode"`
	PrTargetBranch    string     `gorm:"column:pr_target_branch" json:"prTargetBranch"`
	Metadata          string     `gorm:"column:metadata;type:json" json:"metadata"`
	LastSyncStatus    int        `gorm:"column:last_sync_status" json:"lastSyncStatus"`
	LastSyncMessage   string     `gorm:"column:last_sync_message" json:"lastSyncMessage"`
	LastSyncedAt      *time.Time `gorm:"column:last_synced_at" json:"lastSyncedAt"`
	LastEditor        string     `gorm:"column:last_editor" json:"lastEditor"`
	LastCommitSha     string     `gorm:"column:last_commit_sha" json:"lastCommitSha"`
	LastSaveRequestId string     `gorm:"column:last_save_request_id" json:"lastSaveRequestId"`
	TotalRuns         int        `gorm:"column:total_runs" json:"totalRuns"`
	SuccessRuns       int        `gorm:"column:success_runs" json:"successRuns"`
	FailedRuns        int        `gorm:"column:failed_runs" json:"failedRuns"`
	CreatedBy         string     `gorm:"column:created_by" json:"createdBy"`
	IsEnabled         int        `gorm:"column:is_enabled" json:"isEnabled"` // 0: disabled, 1: enabled
}

func (Pipeline) TableName() string {
	return "t_pipeline"
}

// PipelineRun 流水线执行记录表
type PipelineRun struct {
	BaseModel
	RunId               string     `gorm:"column:run_id" json:"runId"`
	PipelineId          string     `gorm:"column:pipeline_id" json:"pipelineId"`
	RequestId           string     `gorm:"column:request_id" json:"requestId"`
	PipelineName        string     `gorm:"column:pipeline_name" json:"pipelineName"`
	Branch              string     `gorm:"column:branch" json:"branch"`
	CommitSha           string     `gorm:"column:commit_sha" json:"commitSha"`
	DefinitionCommitSha string     `gorm:"column:definition_commit_sha" json:"definitionCommitSha"`
	DefinitionPath      string     `gorm:"column:definition_path" json:"definitionPath"`
	Status              int        `gorm:"column:status" json:"status"`
	TriggerType         int        `gorm:"column:trigger_type" json:"triggerType"`
	TriggeredBy         string     `gorm:"column:triggered_by" json:"triggeredBy"`
	Env                 string     `gorm:"column:env;type:json" json:"env"` // JSON格式
	TotalJobs           int        `gorm:"column:total_jobs" json:"totalJobs"`
	CompletedJobs       int        `gorm:"column:completed_jobs" json:"completedJobs"`
	FailedJobs          int        `gorm:"column:failed_jobs" json:"failedJobs"`
	RunningJobs         int        `gorm:"column:running_jobs" json:"runningJobs"`
	CurrentStage        int        `gorm:"column:current_stage" json:"currentStage"`
	TotalStages         int        `gorm:"column:total_stages" json:"totalStages"`
	StartTime           *time.Time `gorm:"column:start_time" json:"startTime"`
	EndTime             *time.Time `gorm:"column:end_time" json:"endTime"`
	Duration            int64      `gorm:"column:duration" json:"duration"` // 毫秒
}

func (PipelineRun) TableName() string {
	return "t_pipeline_run"
}

// PipelineStage 流水线阶段表
type PipelineStage struct {
	BaseModel
	StageId    string `gorm:"column:stage_id" json:"stageId"`
	PipelineId string `gorm:"column:pipeline_id" json:"pipelineId"`
	Name       string `gorm:"column:name" json:"name"`
	StageOrder int    `gorm:"column:stage_order" json:"stageOrder"`
	Parallel   int    `gorm:"column:parallel" json:"parallel"` // 0:否 1:是
}

func (PipelineStage) TableName() string {
	return "t_pipeline_stage"
}

const (
	PipelineStatusUnknown   = 0
	PipelineStatusPending   = 1
	PipelineStatusRunning   = 2
	PipelineStatusSuccess   = 3
	PipelineStatusFailed    = 4
	PipelineStatusCancelled = 5
	PipelineStatusPaused    = 6
)

const (
	PipelineSaveModeDirect = 1
	PipelineSaveModePR     = 2
)

const (
	PipelineSyncStatusUnknown = 0
	PipelineSyncStatusSuccess = 1
	PipelineSyncStatusFailed  = 2
)
