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

import (
	"time"

	domain "github.com/arcentrix/arcentra/internal/domain/pipeline"
)

type PipelinePO struct {
	ID                uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	PipelineID        string     `gorm:"column:pipeline_id"`
	ProjectID         string     `gorm:"column:project_id"`
	Name              string     `gorm:"column:name"`
	Description       string     `gorm:"column:description"`
	RepoURL           string     `gorm:"column:repo_url"`
	DefaultBranch     string     `gorm:"column:default_branch"`
	PipelineFilePath  string     `gorm:"column:pipeline_file_path"`
	Status            int        `gorm:"column:status"`
	SaveMode          int        `gorm:"column:save_mode"`
	PrTargetBranch    string     `gorm:"column:pr_target_branch"`
	Metadata          string     `gorm:"column:metadata"`
	LastSyncStatus    int        `gorm:"column:last_sync_status"`
	LastSyncMessage   string     `gorm:"column:last_sync_message"`
	LastSyncedAt      *time.Time `gorm:"column:last_synced_at"`
	LastEditor        string     `gorm:"column:last_editor"`
	LastCommitSha     string     `gorm:"column:last_commit_sha"`
	LastSaveRequestID string     `gorm:"column:last_save_request_id"`
	TotalRuns         int        `gorm:"column:total_runs"`
	SuccessRuns       int        `gorm:"column:success_runs"`
	FailedRuns        int        `gorm:"column:failed_runs"`
	CreatedBy         string     `gorm:"column:created_by"`
	IsEnabled         int        `gorm:"column:is_enabled"`
	CreatedAt         time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt         time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (PipelinePO) TableName() string { return "t_pipeline" }

func (po *PipelinePO) ToDomain() *domain.Pipeline {
	return &domain.Pipeline{
		ID:                po.ID,
		PipelineID:        po.PipelineID,
		ProjectID:         po.ProjectID,
		Name:              po.Name,
		Description:       po.Description,
		RepoURL:           po.RepoURL,
		DefaultBranch:     po.DefaultBranch,
		PipelineFilePath:  po.PipelineFilePath,
		Status:            domain.PipelineStatus(po.Status),
		SaveMode:          domain.PipelineSaveMode(po.SaveMode),
		PrTargetBranch:    po.PrTargetBranch,
		Metadata:          po.Metadata,
		LastSyncStatus:    domain.PipelineSyncStatus(po.LastSyncStatus),
		LastSyncMessage:   po.LastSyncMessage,
		LastSyncedAt:      po.LastSyncedAt,
		LastEditor:        po.LastEditor,
		LastCommitSha:     po.LastCommitSha,
		LastSaveRequestID: po.LastSaveRequestID,
		TotalRuns:         po.TotalRuns,
		SuccessRuns:       po.SuccessRuns,
		FailedRuns:        po.FailedRuns,
		CreatedBy:         po.CreatedBy,
		IsEnabled:         po.IsEnabled == 1,
		CreatedAt:         po.CreatedAt,
		UpdatedAt:         po.UpdatedAt,
	}
}

func PipelinePOFromDomain(p *domain.Pipeline) *PipelinePO {
	isEnabled := 0
	if p.IsEnabled {
		isEnabled = 1
	}

	return &PipelinePO{
		ID:                p.ID,
		PipelineID:        p.PipelineID,
		ProjectID:         p.ProjectID,
		Name:              p.Name,
		Description:       p.Description,
		RepoURL:           p.RepoURL,
		DefaultBranch:     p.DefaultBranch,
		PipelineFilePath:  p.PipelineFilePath,
		Status:            int(p.Status),
		SaveMode:          int(p.SaveMode),
		PrTargetBranch:    p.PrTargetBranch,
		Metadata:          p.Metadata,
		LastSyncStatus:    int(p.LastSyncStatus),
		LastSyncMessage:   p.LastSyncMessage,
		LastSyncedAt:      p.LastSyncedAt,
		LastEditor:        p.LastEditor,
		LastCommitSha:     p.LastCommitSha,
		LastSaveRequestID: p.LastSaveRequestID,
		TotalRuns:         p.TotalRuns,
		SuccessRuns:       p.SuccessRuns,
		FailedRuns:        p.FailedRuns,
		CreatedBy:         p.CreatedBy,
		IsEnabled:         isEnabled,
		CreatedAt:         p.CreatedAt,
		UpdatedAt:         p.UpdatedAt,
	}
}

type PipelineRunPO struct {
	ID                  uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	RunID               string     `gorm:"column:run_id"`
	PipelineID          string     `gorm:"column:pipeline_id"`
	RequestID           string     `gorm:"column:request_id"`
	PipelineName        string     `gorm:"column:pipeline_name"`
	Branch              string     `gorm:"column:branch"`
	CommitSha           string     `gorm:"column:commit_sha"`
	DefinitionCommitSha string     `gorm:"column:definition_commit_sha"`
	DefinitionPath      string     `gorm:"column:definition_path"`
	Status              int        `gorm:"column:status"`
	TriggerType         int        `gorm:"column:trigger_type"`
	TriggeredBy         string     `gorm:"column:triggered_by"`
	Env                 string     `gorm:"column:env"`
	TotalJobs           int        `gorm:"column:total_jobs"`
	CompletedJobs       int        `gorm:"column:completed_jobs"`
	FailedJobs          int        `gorm:"column:failed_jobs"`
	RunningJobs         int        `gorm:"column:running_jobs"`
	CurrentStage        int        `gorm:"column:current_stage"`
	TotalStages         int        `gorm:"column:total_stages"`
	StartTime           *time.Time `gorm:"column:start_time"`
	EndTime             *time.Time `gorm:"column:end_time"`
	Duration            int64      `gorm:"column:duration"`
	CreatedAt           time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt           time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (PipelineRunPO) TableName() string { return "t_pipeline_run" }

func (po *PipelineRunPO) ToDomain() *domain.PipelineRun {
	return &domain.PipelineRun{
		ID:                  po.ID,
		RunID:               po.RunID,
		PipelineID:          po.PipelineID,
		RequestID:           po.RequestID,
		PipelineName:        po.PipelineName,
		Branch:              po.Branch,
		CommitSha:           po.CommitSha,
		DefinitionCommitSha: po.DefinitionCommitSha,
		DefinitionPath:      po.DefinitionPath,
		Status:              domain.PipelineStatus(po.Status),
		TriggerType:         po.TriggerType,
		TriggeredBy:         po.TriggeredBy,
		Env:                 po.Env,
		TotalJobs:           po.TotalJobs,
		CompletedJobs:       po.CompletedJobs,
		FailedJobs:          po.FailedJobs,
		RunningJobs:         po.RunningJobs,
		CurrentStage:        po.CurrentStage,
		TotalStages:         po.TotalStages,
		StartTime:           po.StartTime,
		EndTime:             po.EndTime,
		Duration:            po.Duration,
		CreatedAt:           po.CreatedAt,
		UpdatedAt:           po.UpdatedAt,
	}
}

func PipelineRunPOFromDomain(r *domain.PipelineRun) *PipelineRunPO {
	return &PipelineRunPO{
		ID:                  r.ID,
		RunID:               r.RunID,
		PipelineID:          r.PipelineID,
		RequestID:           r.RequestID,
		PipelineName:        r.PipelineName,
		Branch:              r.Branch,
		CommitSha:           r.CommitSha,
		DefinitionCommitSha: r.DefinitionCommitSha,
		DefinitionPath:      r.DefinitionPath,
		Status:              int(r.Status),
		TriggerType:         r.TriggerType,
		TriggeredBy:         r.TriggeredBy,
		Env:                 r.Env,
		TotalJobs:           r.TotalJobs,
		CompletedJobs:       r.CompletedJobs,
		FailedJobs:          r.FailedJobs,
		RunningJobs:         r.RunningJobs,
		CurrentStage:        r.CurrentStage,
		TotalStages:         r.TotalStages,
		StartTime:           r.StartTime,
		EndTime:             r.EndTime,
		Duration:            r.Duration,
		CreatedAt:           r.CreatedAt,
		UpdatedAt:           r.UpdatedAt,
	}
}

type PipelineStagePO struct {
	ID         uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	StageID    string    `gorm:"column:stage_id"`
	PipelineID string    `gorm:"column:pipeline_id"`
	Name       string    `gorm:"column:name"`
	StageOrder int       `gorm:"column:stage_order"`
	Parallel   int       `gorm:"column:parallel"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt  time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (PipelineStagePO) TableName() string { return "t_pipeline_stage" }

func (po *PipelineStagePO) ToDomain() *domain.PipelineStage {
	return &domain.PipelineStage{
		ID:         po.ID,
		StageID:    po.StageID,
		PipelineID: po.PipelineID,
		Name:       po.Name,
		StageOrder: po.StageOrder,
		Parallel:   po.Parallel == 1,
		CreatedAt:  po.CreatedAt,
		UpdatedAt:  po.UpdatedAt,
	}
}

func PipelineStagePOFromDomain(s *domain.PipelineStage) *PipelineStagePO {
	parallel := 0
	if s.Parallel {
		parallel = 1
	}

	return &PipelineStagePO{
		ID:         s.ID,
		StageID:    s.StageID,
		PipelineID: s.PipelineID,
		Name:       s.Name,
		StageOrder: s.StageOrder,
		Parallel:   parallel,
		CreatedAt:  s.CreatedAt,
		UpdatedAt:  s.UpdatedAt,
	}
}
