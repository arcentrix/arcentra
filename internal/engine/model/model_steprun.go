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

// StepRun 步骤执行表
// 根据 DSL: Step → StepRun
type StepRun struct {
	BaseModel
	StepRunId       string     `gorm:"column:step_run_id" json:"stepRunId"`
	Name            string     `gorm:"column:name" json:"name"`
	PipelineId      string     `gorm:"column:pipeline_id" json:"pipelineId"`
	PipelineRunId   string     `gorm:"column:pipeline_run_id" json:"pipelineRunId"`
	StageId         string     `gorm:"column:stage_id" json:"stageId"`
	JobId           string     `gorm:"column:job_id" json:"jobId"`
	JobRunId        string     `gorm:"column:job_run_id" json:"jobRunId"`
	StepIndex       int        `gorm:"column:step_index" json:"stepIndex"`
	AgentId         string     `gorm:"column:agent_id" json:"agentId"`
	Status          int        `gorm:"column:status" json:"status"`       // 1:等待 2:入队 3:运行中 4:成功 5:失败 6:已取消 7:超时 8:已跳过
	Priority        int        `gorm:"column:priority" json:"priority"`   // 1:最高 5:普通 10:最低
	Uses            string     `gorm:"column:uses" json:"uses"`           // 插件标识
	Action          string     `gorm:"column:action" json:"action"`       // 插件动作
	Args            string     `gorm:"column:args;type:json" json:"args"` // JSON格式
	Workspace       string     `gorm:"column:workspace" json:"workspace"`
	Env             string     `gorm:"column:env;type:json" json:"env"`         // JSON格式
	Secrets         string     `gorm:"column:secrets;type:json" json:"secrets"` // JSON格式
	Timeout         string     `gorm:"column:timeout" json:"timeout"`           // 超时时间（如 "30m", "1h"）
	RetryCount      int        `gorm:"column:retry_count" json:"retryCount"`
	CurrentRetry    int        `gorm:"column:current_retry" json:"currentRetry"`
	AllowFailure    int        `gorm:"column:allow_failure" json:"allowFailure"`             // 0:否 1:是
	ContinueOnError int        `gorm:"column:continue_on_error" json:"continueOnError"`      // 0:否 1:是
	When            string     `gorm:"column:when" json:"when"`                              // 条件表达式
	LabelSelector   string     `gorm:"column:label_selector;type:json" json:"labelSelector"` // JSON格式
	DependsOn       string     `gorm:"column:depends_on" json:"dependsOn"`                   // 逗号分隔
	ExitCode        *int       `gorm:"column:exit_code" json:"exitCode"`
	ErrorMessage    string     `gorm:"column:error_message;type:text" json:"errorMessage"`
	StartTime       *time.Time `gorm:"column:start_time" json:"startTime"`
	EndTime         *time.Time `gorm:"column:end_time" json:"endTime"`
	Duration        int64      `gorm:"column:duration" json:"duration"` // 毫秒
	CreatedBy       string     `gorm:"column:created_by" json:"createdBy"`
}

func (StepRun) TableName() string {
	return "t_step_run"
}

// StepRunArtifact 步骤执行产物表
type StepRunArtifact struct {
	BaseModel
	ArtifactId    string     `gorm:"column:artifact_id" json:"artifactId"`
	StepRunId     string     `gorm:"column:step_run_id" json:"stepRunId"`
	JobRunId      string     `gorm:"column:job_run_id" json:"jobRunId"`
	PipelineRunId string     `gorm:"column:pipeline_run_id" json:"pipelineRunId"`
	Name          string     `gorm:"column:name" json:"name"`
	Path          string     `gorm:"column:path" json:"path"`
	Destination   string     `gorm:"column:destination" json:"destination"`
	Size          int64      `gorm:"column:size" json:"size"`                // 字节
	StorageType   string     `gorm:"column:storage_type" json:"storageType"` // minio/s3/oss/gcs/cos
	StoragePath   string     `gorm:"column:storage_path" json:"storagePath"`
	Expire        int        `gorm:"column:expire" json:"expire"` // 0:否 1:是
	ExpireDays    *int       `gorm:"column:expire_days" json:"expireDays"`
	ExpiredAt     *time.Time `gorm:"column:expired_at" json:"expiredAt"`
}

func (StepRunArtifact) TableName() string {
	return "t_step_run_artifact"
}
