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

import (
	"time"

	domain "github.com/arcentrix/arcentra/internal/domain/execution"
)

type StepRunPO struct {
	ID              uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	StepRunID       string     `gorm:"column:step_run_id"`
	Name            string     `gorm:"column:name"`
	PipelineID      string     `gorm:"column:pipeline_id"`
	PipelineRunID   string     `gorm:"column:pipeline_run_id"`
	StageID         string     `gorm:"column:stage_id"`
	JobID           string     `gorm:"column:job_id"`
	JobRunID        string     `gorm:"column:job_run_id"`
	StepIndex       int        `gorm:"column:step_index"`
	AgentID         string     `gorm:"column:agent_id"`
	Status          int        `gorm:"column:status"`
	Priority        int        `gorm:"column:priority"`
	Uses            string     `gorm:"column:uses"`
	Action          string     `gorm:"column:action"`
	Args            string     `gorm:"column:args"`
	Workspace       string     `gorm:"column:workspace"`
	Env             string     `gorm:"column:env"`
	Secrets         string     `gorm:"column:secrets"`
	Timeout         string     `gorm:"column:timeout"`
	RetryCount      int        `gorm:"column:retry_count"`
	CurrentRetry    int        `gorm:"column:current_retry"`
	AllowFailure    int        `gorm:"column:allow_failure"`
	ContinueOnError int        `gorm:"column:continue_on_error"`
	When            string     `gorm:"column:when_expr"`
	LabelSelector   string     `gorm:"column:label_selector"`
	DependsOn       string     `gorm:"column:depends_on"`
	ExitCode        *int       `gorm:"column:exit_code"`
	ErrorMessage    string     `gorm:"column:error_message"`
	StartTime       *time.Time `gorm:"column:start_time"`
	EndTime         *time.Time `gorm:"column:end_time"`
	Duration        int64      `gorm:"column:duration"`
	CreatedBy       string     `gorm:"column:created_by"`
	CreatedAt       time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (StepRunPO) TableName() string { return "t_step_run" }

func (po *StepRunPO) ToDomain() *domain.StepRun {
	return &domain.StepRun{
		ID:              po.ID,
		StepRunID:       po.StepRunID,
		Name:            po.Name,
		PipelineID:      po.PipelineID,
		PipelineRunID:   po.PipelineRunID,
		StageID:         po.StageID,
		JobID:           po.JobID,
		JobRunID:        po.JobRunID,
		StepIndex:       po.StepIndex,
		AgentID:         po.AgentID,
		Status:          domain.StepRunStatus(po.Status),
		Priority:        po.Priority,
		Uses:            po.Uses,
		Action:          po.Action,
		Args:            po.Args,
		Workspace:       po.Workspace,
		Env:             po.Env,
		Secrets:         po.Secrets,
		Timeout:         po.Timeout,
		RetryCount:      po.RetryCount,
		CurrentRetry:    po.CurrentRetry,
		AllowFailure:    po.AllowFailure == 1,
		ContinueOnError: po.ContinueOnError == 1,
		When:            po.When,
		LabelSelector:   po.LabelSelector,
		DependsOn:       po.DependsOn,
		ExitCode:        po.ExitCode,
		ErrorMessage:    po.ErrorMessage,
		StartTime:       po.StartTime,
		EndTime:         po.EndTime,
		Duration:        po.Duration,
		CreatedBy:       po.CreatedBy,
		CreatedAt:       po.CreatedAt,
		UpdatedAt:       po.UpdatedAt,
	}
}

func StepRunPOFromDomain(sr *domain.StepRun) *StepRunPO {
	allowFailure := 0
	if sr.AllowFailure {
		allowFailure = 1
	}
	continueOnError := 0
	if sr.ContinueOnError {
		continueOnError = 1
	}

	return &StepRunPO{
		ID:              sr.ID,
		StepRunID:       sr.StepRunID,
		Name:            sr.Name,
		PipelineID:      sr.PipelineID,
		PipelineRunID:   sr.PipelineRunID,
		StageID:         sr.StageID,
		JobID:           sr.JobID,
		JobRunID:        sr.JobRunID,
		StepIndex:       sr.StepIndex,
		AgentID:         sr.AgentID,
		Status:          int(sr.Status),
		Priority:        sr.Priority,
		Uses:            sr.Uses,
		Action:          sr.Action,
		Args:            sr.Args,
		Workspace:       sr.Workspace,
		Env:             sr.Env,
		Secrets:         sr.Secrets,
		Timeout:         sr.Timeout,
		RetryCount:      sr.RetryCount,
		CurrentRetry:    sr.CurrentRetry,
		AllowFailure:    allowFailure,
		ContinueOnError: continueOnError,
		When:            sr.When,
		LabelSelector:   sr.LabelSelector,
		DependsOn:       sr.DependsOn,
		ExitCode:        sr.ExitCode,
		ErrorMessage:    sr.ErrorMessage,
		StartTime:       sr.StartTime,
		EndTime:         sr.EndTime,
		Duration:        sr.Duration,
		CreatedBy:       sr.CreatedBy,
		CreatedAt:       sr.CreatedAt,
		UpdatedAt:       sr.UpdatedAt,
	}
}

type StepRunArtifactPO struct {
	ID            uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	ArtifactID    string     `gorm:"column:artifact_id"`
	StepRunID     string     `gorm:"column:step_run_id"`
	JobRunID      string     `gorm:"column:job_run_id"`
	PipelineRunID string     `gorm:"column:pipeline_run_id"`
	Name          string     `gorm:"column:name"`
	Path          string     `gorm:"column:path"`
	Destination   string     `gorm:"column:destination"`
	Size          int64      `gorm:"column:size"`
	StorageType   string     `gorm:"column:storage_type"`
	StoragePath   string     `gorm:"column:storage_path"`
	Expire        int        `gorm:"column:expire"`
	ExpireDays    *int       `gorm:"column:expire_days"`
	ExpiredAt     *time.Time `gorm:"column:expired_at"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (StepRunArtifactPO) TableName() string { return "t_step_run_artifact" }

func (po *StepRunArtifactPO) ToDomain() *domain.StepRunArtifact {
	return &domain.StepRunArtifact{
		ID:            po.ID,
		ArtifactID:    po.ArtifactID,
		StepRunID:     po.StepRunID,
		JobRunID:      po.JobRunID,
		PipelineRunID: po.PipelineRunID,
		Name:          po.Name,
		Path:          po.Path,
		Destination:   po.Destination,
		Size:          po.Size,
		StorageType:   po.StorageType,
		StoragePath:   po.StoragePath,
		Expire:        po.Expire == 1,
		ExpireDays:    po.ExpireDays,
		ExpiredAt:     po.ExpiredAt,
		CreatedAt:     po.CreatedAt,
		UpdatedAt:     po.UpdatedAt,
	}
}

func StepRunArtifactPOFromDomain(a *domain.StepRunArtifact) *StepRunArtifactPO {
	expire := 0
	if a.Expire {
		expire = 1
	}

	return &StepRunArtifactPO{
		ID:            a.ID,
		ArtifactID:    a.ArtifactID,
		StepRunID:     a.StepRunID,
		JobRunID:      a.JobRunID,
		PipelineRunID: a.PipelineRunID,
		Name:          a.Name,
		Path:          a.Path,
		Destination:   a.Destination,
		Size:          a.Size,
		StorageType:   a.StorageType,
		StoragePath:   a.StoragePath,
		Expire:        expire,
		ExpireDays:    a.ExpireDays,
		ExpiredAt:     a.ExpiredAt,
		CreatedAt:     a.CreatedAt,
		UpdatedAt:     a.UpdatedAt,
	}
}
