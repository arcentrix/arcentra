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

package pipeline

import "time"

// Pipeline represents a pipeline definition (design-time entity).
type Pipeline struct {
	ID                uint64             `json:"id"`
	PipelineID        string             `json:"pipelineId"`
	ProjectID         string             `json:"projectId"`
	Name              string             `json:"name"`
	Description       string             `json:"description"`
	RepoURL           string             `json:"repoUrl"`
	DefaultBranch     string             `json:"defaultBranch"`
	PipelineFilePath  string             `json:"pipelineFilePath"`
	Status            PipelineStatus     `json:"status"`
	SaveMode          PipelineSaveMode   `json:"saveMode"`
	PrTargetBranch    string             `json:"prTargetBranch"`
	Metadata          string             `json:"metadata"`
	LastSyncStatus    PipelineSyncStatus `json:"lastSyncStatus"`
	LastSyncMessage   string             `json:"lastSyncMessage"`
	LastSyncedAt      *time.Time         `json:"lastSyncedAt"`
	LastEditor        string             `json:"lastEditor"`
	LastCommitSha     string             `json:"lastCommitSha"`
	LastSaveRequestID string             `json:"lastSaveRequestId"`
	TotalRuns         int                `json:"totalRuns"`
	SuccessRuns       int                `json:"successRuns"`
	FailedRuns        int                `json:"failedRuns"`
	CreatedBy         string             `json:"createdBy"`
	IsEnabled         bool               `json:"isEnabled"`
	CreatedAt         time.Time          `json:"createdAt"`
	UpdatedAt         time.Time          `json:"updatedAt"`
}

// PipelineRun represents a single execution of a pipeline.
type PipelineRun struct {
	ID                  uint64         `json:"id"`
	RunID               string         `json:"runId"`
	PipelineID          string         `json:"pipelineId"`
	RequestID           string         `json:"requestId"`
	PipelineName        string         `json:"pipelineName"`
	Branch              string         `json:"branch"`
	CommitSha           string         `json:"commitSha"`
	DefinitionCommitSha string         `json:"definitionCommitSha"`
	DefinitionPath      string         `json:"definitionPath"`
	Status              PipelineStatus `json:"status"`
	TriggerType         int            `json:"triggerType"`
	TriggeredBy         string         `json:"triggeredBy"`
	Env                 string         `json:"env"`
	TotalJobs           int            `json:"totalJobs"`
	CompletedJobs       int            `json:"completedJobs"`
	FailedJobs          int            `json:"failedJobs"`
	RunningJobs         int            `json:"runningJobs"`
	CurrentStage        int            `json:"currentStage"`
	TotalStages         int            `json:"totalStages"`
	StartTime           *time.Time     `json:"startTime"`
	EndTime             *time.Time     `json:"endTime"`
	Duration            int64          `json:"duration"`
	CreatedAt           time.Time      `json:"createdAt"`
	UpdatedAt           time.Time      `json:"updatedAt"`
}

// PipelineStage represents one stage in a pipeline definition.
type PipelineStage struct {
	ID         uint64    `json:"id"`
	StageID    string    `json:"stageId"`
	PipelineID string    `json:"pipelineId"`
	Name       string    `json:"name"`
	StageOrder int       `json:"stageOrder"`
	Parallel   bool      `json:"parallel"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}
