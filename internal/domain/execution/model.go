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

package execution

import "time"

// StepRun represents the execution of a single step in a pipeline.
type StepRun struct {
	ID              uint64        `json:"id"`
	StepRunID       string        `json:"stepRunId"`
	Name            string        `json:"name"`
	PipelineID      string        `json:"pipelineId"`
	PipelineRunID   string        `json:"pipelineRunId"`
	StageID         string        `json:"stageId"`
	JobID           string        `json:"jobId"`
	JobRunID        string        `json:"jobRunId"`
	StepIndex       int           `json:"stepIndex"`
	AgentID         string        `json:"agentId"`
	Status          StepRunStatus `json:"status"`
	Priority        int           `json:"priority"`
	Uses            string        `json:"uses"`
	Action          string        `json:"action"`
	Args            string        `json:"args"`
	Workspace       string        `json:"workspace"`
	Env             string        `json:"env"`
	Secrets         string        `json:"-"`
	Timeout         string        `json:"timeout"`
	RetryCount      int           `json:"retryCount"`
	CurrentRetry    int           `json:"currentRetry"`
	AllowFailure    bool          `json:"allowFailure"`
	ContinueOnError bool          `json:"continueOnError"`
	When            string        `json:"when"`
	LabelSelector   string        `json:"labelSelector"`
	DependsOn       string        `json:"dependsOn"`
	ExitCode        *int          `json:"exitCode"`
	ErrorMessage    string        `json:"errorMessage"`
	StartTime       *time.Time    `json:"startTime"`
	EndTime         *time.Time    `json:"endTime"`
	Duration        int64         `json:"duration"`
	CreatedBy       string        `json:"createdBy"`
	CreatedAt       time.Time     `json:"createdAt"`
	UpdatedAt       time.Time     `json:"updatedAt"`
}

// IsTerminal returns true if the step run has finished executing.
func (sr *StepRun) IsTerminal() bool {
	return sr.Status.IsTerminal()
}

// StepRunArtifact represents a build artifact produced by a step run.
type StepRunArtifact struct {
	ID            uint64     `json:"id"`
	ArtifactID    string     `json:"artifactId"`
	StepRunID     string     `json:"stepRunId"`
	JobRunID      string     `json:"jobRunId"`
	PipelineRunID string     `json:"pipelineRunId"`
	Name          string     `json:"name"`
	Path          string     `json:"path"`
	Destination   string     `json:"destination"`
	Size          int64      `json:"size"`
	StorageType   string     `json:"storageType"`
	StoragePath   string     `json:"storagePath"`
	Expire        bool       `json:"expire"`
	ExpireDays    *int       `json:"expireDays"`
	ExpiredAt     *time.Time `json:"expiredAt"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// StepRunRecord represents a persisted execution record of a step run.
type StepRunRecord struct {
	StepRunID     string         `json:"stepRunId"`
	StepRunType   string         `json:"stepRunType"`
	Status        string         `json:"status"`
	Priority      int            `json:"priority"`
	Queue         string         `json:"queue"`
	PipelineID    string         `json:"pipelineId"`
	PipelineRunID string         `json:"pipelineRunId"`
	StageID       string         `json:"stageId"`
	JobID         string         `json:"jobId"`
	JobRunID      string         `json:"jobRunId"`
	AgentID       string         `json:"agentId"`
	Payload       map[string]any `json:"payload"`
	ErrorMessage  string         `json:"errorMessage"`
	CreateTime    time.Time      `json:"createTime"`
	StartTime     *time.Time     `json:"startTime"`
	EndTime       *time.Time     `json:"endTime"`
	Duration      int64          `json:"duration"`
	RetryCount    int            `json:"retryCount"`
	CurrentRetry  int            `json:"currentRetry"`
}
