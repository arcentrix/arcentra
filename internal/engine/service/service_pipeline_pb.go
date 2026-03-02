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

package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/pkg/pipeline/validation"
	"github.com/arcentrix/arcentra/pkg/dispatch"
	"github.com/arcentrix/arcentra/pkg/git"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/serde"
	timepkg "github.com/arcentrix/arcentra/pkg/time"
)

type PipelineServiceImpl struct {
	pipelinev1.UnimplementedPipelineServiceServer
	pipelineRepo repo.IPipelineRepository
	projectRepo  repo.IProjectRepository
}

// NewPipelineServiceImpl creates pipeline grpc service.
func NewPipelineServiceImpl(services *Services) *PipelineServiceImpl {
	return &PipelineServiceImpl{
		pipelineRepo: services.PipelineRepo,
		projectRepo:  services.ProjectRepo,
	}
}

func (s *PipelineServiceImpl) CreatePipeline(ctx context.Context, req *pipelinev1.CreatePipelineRequest) (*pipelinev1.CreatePipelineResponse, error) {
	if strings.TrimSpace(req.GetProjectId()) == "" || strings.TrimSpace(req.GetName()) == "" || strings.TrimSpace(req.GetPipelineFilePath()) == "" {
		return &pipelinev1.CreatePipelineResponse{
			Success: false,
			Message: "projectId, name and pipelineFilePath are required",
			Error:   s.error(400, "missing required fields", "validation", nil),
		}, nil
	}

	project, err := s.projectRepo.Get(ctx, req.GetProjectId())
	if err != nil {
		return &pipelinev1.CreatePipelineResponse{
			Success: false,
			Message: "project not found",
			Error:   s.error(404, err.Error(), "not_found", nil),
		}, nil
	}

	repoUrl := strings.TrimSpace(req.GetRepoUrl())
	if repoUrl == "" {
		repoUrl = project.RepoUrl
	}
	branch := strings.TrimSpace(req.GetDefaultBranch())
	if branch == "" {
		branch = project.DefaultBranch
	}
	if branch == "" {
		branch = "main"
	}

	now := time.Now()
	metadataJSON := serde.MarshalStringMap(req.GetMetadata())
	saveMode := mapSaveModeToModel(req.GetSaveMode())
	if saveMode == model.PipelineSaveModeDirect && strings.TrimSpace(req.GetPrTargetBranch()) != "" {
		saveMode = model.PipelineSaveModePR
	}

	p := &model.Pipeline{
		PipelineId:       id.GetUild(),
		ProjectId:        req.GetProjectId(),
		Name:             strings.TrimSpace(req.GetName()),
		Description:      strings.TrimSpace(req.GetDescription()),
		RepoUrl:          repoUrl,
		DefaultBranch:    branch,
		PipelineFilePath: normalizePipelinePath(req.GetPipelineFilePath()),
		SaveMode:         saveMode,
		PrTargetBranch:   strings.TrimSpace(req.GetPrTargetBranch()),
		Metadata:         metadataJSON,
		Status:           model.PipelineStatusPending,
		LastSyncStatus:   model.PipelineSyncStatusUnknown,
		LastEditor:       strings.TrimSpace(req.GetCreatedBy()),
		LastSyncedAt:     &now,
		CreatedBy:        strings.TrimSpace(req.GetCreatedBy()),
		IsEnabled:        1,
	}
	if err := s.pipelineRepo.Create(ctx, p); err != nil {
		return &pipelinev1.CreatePipelineResponse{
			Success: false,
			Message: "create pipeline failed",
			Error:   s.error(500, err.Error(), "internal", nil),
		}, nil
	}

	return &pipelinev1.CreatePipelineResponse{
		Success:    true,
		Message:    "pipeline created",
		PipelineId: p.PipelineId,
	}, nil
}

func (s *PipelineServiceImpl) UpdatePipeline(ctx context.Context, req *pipelinev1.UpdatePipelineRequest) (*pipelinev1.UpdatePipelineResponse, error) {
	if strings.TrimSpace(req.GetPipelineId()) == "" {
		return &pipelinev1.UpdatePipelineResponse{
			Success: false,
			Message: "pipelineId is required",
			Error:   s.error(400, "pipelineId is required", "validation", nil),
		}, nil
	}
	_, err := s.pipelineRepo.Get(ctx, req.GetPipelineId())
	if err != nil {
		return &pipelinev1.UpdatePipelineResponse{
			Success: false,
			Message: "pipeline not found",
			Error:   s.error(404, err.Error(), "not_found", nil),
		}, nil
	}

	updates := map[string]any{}
	if v := strings.TrimSpace(req.GetName()); v != "" {
		updates["name"] = v
	}
	if req.Description != "" {
		updates["description"] = strings.TrimSpace(req.GetDescription())
	}
	if v := strings.TrimSpace(req.GetRepoUrl()); v != "" {
		updates["repo_url"] = v
	}
	if v := strings.TrimSpace(req.GetDefaultBranch()); v != "" {
		updates["default_branch"] = v
	}
	if v := strings.TrimSpace(req.GetPipelineFilePath()); v != "" {
		updates["pipeline_file_path"] = normalizePipelinePath(v)
	}
	if req.GetSaveMode() != pipelinev1.PipelineSaveMode_PIPELINE_SAVE_MODE_UNSPECIFIED {
		updates["save_mode"] = mapSaveModeToModel(req.GetSaveMode())
	}
	if req.PrTargetBranch != "" {
		updates["pr_target_branch"] = strings.TrimSpace(req.GetPrTargetBranch())
	}
	if req.Metadata != nil {
		updates["metadata"] = serde.MarshalStringMap(req.GetMetadata())
	}
	if req.IsEnabled != 0 {
		updates["is_enabled"] = req.GetIsEnabled()
	}
	if len(updates) == 0 {
		return &pipelinev1.UpdatePipelineResponse{Success: true, Message: "no changes"}, nil
	}
	if err := s.pipelineRepo.Update(ctx, req.GetPipelineId(), updates); err != nil {
		return &pipelinev1.UpdatePipelineResponse{
			Success: false,
			Message: "update pipeline failed",
			Error:   s.error(500, err.Error(), "internal", nil),
		}, nil
	}
	return &pipelinev1.UpdatePipelineResponse{Success: true, Message: "pipeline updated"}, nil
}

func (s *PipelineServiceImpl) GetPipeline(ctx context.Context, req *pipelinev1.GetPipelineRequest) (*pipelinev1.GetPipelineResponse, error) {
	if strings.TrimSpace(req.GetPipelineId()) == "" {
		return &pipelinev1.GetPipelineResponse{
			Success: false,
			Message: "pipelineId is required",
			Error:   s.error(400, "pipelineId is required", "validation", nil),
		}, nil
	}
	p, err := s.pipelineRepo.Get(ctx, req.GetPipelineId())
	if err != nil {
		return &pipelinev1.GetPipelineResponse{
			Success: false,
			Message: "pipeline not found",
			Error:   s.error(404, err.Error(), "not_found", nil),
		}, nil
	}
	return &pipelinev1.GetPipelineResponse{
		Success:  true,
		Message:  "ok",
		Pipeline: toPipelineDetail(p),
	}, nil
}

func (s *PipelineServiceImpl) ListPipelines(ctx context.Context, req *pipelinev1.ListPipelinesRequest) (*pipelinev1.ListPipelinesResponse, error) {
	query := &repo.PipelineQuery{
		ProjectId: strings.TrimSpace(req.GetProjectId()),
		Name:      strings.TrimSpace(req.GetName()),
		Status:    int(req.GetStatus()),
		Page:      int(req.GetPage()),
		PageSize:  int(req.GetPageSize()),
	}
	list, total, err := s.pipelineRepo.List(ctx, query)
	if err != nil {
		return &pipelinev1.ListPipelinesResponse{
			Success: false,
			Message: "list pipelines failed",
		}, nil
	}
	out := make([]*pipelinev1.PipelineDetail, 0, len(list))
	for _, item := range list {
		out = append(out, toPipelineDetail(item))
	}
	return &pipelinev1.ListPipelinesResponse{
		Success:   true,
		Message:   "ok",
		Pipelines: out,
		Total:     int32(total),
		Page:      int32(dispatch.Max(query.Page, 1)),
		PageSize:  int32(defaultPageSize(query.PageSize)),
	}, nil
}

func (s *PipelineServiceImpl) DeletePipeline(ctx context.Context, req *pipelinev1.DeletePipelineRequest) (*pipelinev1.DeletePipelineResponse, error) {
	if strings.TrimSpace(req.GetPipelineId()) == "" {
		return &pipelinev1.DeletePipelineResponse{
			Success: false,
			Message: "pipelineId is required",
			Error:   s.error(400, "pipelineId is required", "validation", nil),
		}, nil
	}
	if err := s.pipelineRepo.Update(ctx, req.GetPipelineId(), map[string]any{"is_enabled": 0, "status": model.PipelineStatusCancelled}); err != nil {
		return &pipelinev1.DeletePipelineResponse{
			Success: false,
			Message: "delete pipeline failed",
			Error:   s.error(500, err.Error(), "internal", nil),
		}, nil
	}
	return &pipelinev1.DeletePipelineResponse{Success: true, Message: "pipeline deleted"}, nil
}

func (s *PipelineServiceImpl) TriggerPipeline(ctx context.Context, req *pipelinev1.TriggerPipelineRequest) (*pipelinev1.TriggerPipelineResponse, error) {
	if strings.TrimSpace(req.GetPipelineId()) == "" {
		return &pipelinev1.TriggerPipelineResponse{
			Success: false,
			Message: "pipelineId is required",
			Error:   s.error(400, "pipelineId is required", "validation", nil),
		}, nil
	}

	pipeline, err := s.pipelineRepo.Get(ctx, req.GetPipelineId())
	if err != nil {
		return &pipelinev1.TriggerPipelineResponse{
			Success: false,
			Message: "pipeline not found",
			Error:   s.error(404, err.Error(), "not_found", nil),
		}, nil
	}
	requestId := strings.TrimSpace(req.GetRequestId())
	if requestId != "" {
		existing, err := s.pipelineRepo.GetRunByRequestId(ctx, pipeline.PipelineId, requestId)
		if err != nil {
			return &pipelinev1.TriggerPipelineResponse{
				Success: false,
				Message: "check request id failed",
				Error:   s.error(500, err.Error(), "internal", nil),
			}, nil
		}
		if existing != nil {
			return &pipelinev1.TriggerPipelineResponse{
				Success: true,
				Message: "idempotent request",
				RunId:   existing.RunId,
			}, nil
		}
	}
	project, err := s.projectRepo.Get(ctx, pipeline.ProjectId)
	if err != nil {
		return &pipelinev1.TriggerPipelineResponse{
			Success: false,
			Message: "project not found",
			Error:   s.error(404, err.Error(), "not_found", nil),
		}, nil
	}

	content, headSha, err := s.loadDefinitionFromRepo(ctx, pipeline, project)
	if err != nil {
		return &pipelinev1.TriggerPipelineResponse{
			Success: false,
			Message: "load definition failed",
			Error:   s.error(500, err.Error(), "internal", nil),
		}, nil
	}
	parsedSpec, err := spec.ParseContentToProto(content, pipelinev1.SpecFormat_SPEC_FORMAT_UNSPECIFIED)
	if err != nil {
		return &pipelinev1.TriggerPipelineResponse{
			Success: false,
			Message: "spec parse failed",
			Error:   s.error(400, err.Error(), "validation", nil),
		}, nil
	}
	if err := validation.NewSchemaValidator().Validate((*spec.Pipeline)(parsedSpec)); err != nil {
		return &pipelinev1.TriggerPipelineResponse{
			Success: false,
			Message: "spec validation failed",
			Error:   s.error(400, err.Error(), "validation", nil),
		}, nil
	}

	variablesJSON := serde.MarshalStringMap(req.GetVariables())
	run := &model.PipelineRun{
		RunId:               id.GetUild(),
		PipelineId:          pipeline.PipelineId,
		RequestId:           requestId,
		PipelineName:        pipeline.Name,
		Branch:              pipeline.DefaultBranch,
		DefinitionCommitSha: headSha,
		DefinitionPath:      pipeline.PipelineFilePath,
		Status:              model.PipelineStatusPending,
		TriggerType:         int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL),
		TriggeredBy:         strings.TrimSpace(req.GetTriggeredBy()),
		Env:                 variablesJSON,
		TotalJobs:           0,
	}
	if err := s.pipelineRepo.CreateRun(ctx, run); err != nil {
		if requestId != "" && isDuplicateEntryError(err) {
			existing, getErr := s.pipelineRepo.GetRunByRequestId(ctx, pipeline.PipelineId, requestId)
			if getErr == nil && existing != nil {
				return &pipelinev1.TriggerPipelineResponse{
					Success: true,
					Message: "idempotent request",
					RunId:   existing.RunId,
				}, nil
			}
		}
		return &pipelinev1.TriggerPipelineResponse{
			Success: false,
			Message: "create run failed",
			Error:   s.error(500, err.Error(), "internal", nil),
		}, nil
	}
	_ = s.pipelineRepo.Update(ctx, pipeline.PipelineId, map[string]any{
		"status":            model.PipelineStatusPending,
		"total_runs":        pipeline.TotalRuns + 1,
		"last_sync_status":  model.PipelineSyncStatusSuccess,
		"last_sync_message": "triggered from repository definition",
		"last_synced_at":    time.Now(),
		"last_commit_sha":   headSha,
	})

	return &pipelinev1.TriggerPipelineResponse{
		Success: true,
		Message: "pipeline triggered",
		RunId:   run.RunId,
	}, nil
}

func (s *PipelineServiceImpl) StopPipeline(ctx context.Context, req *pipelinev1.StopPipelineRequest) (*pipelinev1.StopPipelineResponse, error) {
	if strings.TrimSpace(req.GetRunId()) == "" {
		return &pipelinev1.StopPipelineResponse{
			Success: false,
			Message: "runId is required",
			Error:   s.error(400, "runId is required", "validation", nil),
		}, nil
	}
	run, err := s.pipelineRepo.GetRun(ctx, req.GetRunId())
	if err != nil {
		return &pipelinev1.StopPipelineResponse{
			Success: false,
			Message: "run not found",
			Error:   s.error(404, err.Error(), "not_found", nil),
		}, nil
	}
	now := time.Now()
	duration := int64(0)
	if run.StartTime != nil {
		duration = now.Sub(*run.StartTime).Milliseconds()
	}
	if err := s.pipelineRepo.Update(ctx, run.PipelineId, map[string]any{"status": model.PipelineStatusCancelled}); err != nil {
		log.Warnw("stop pipeline update pipeline status failed", "pipelineId", run.PipelineId, "error", err)
	}
	if err := s.pipelineRepo.Update(ctx, run.PipelineId, map[string]any{"last_sync_message": strings.TrimSpace(req.GetReason())}); err != nil {
		log.Warnw("stop pipeline update sync message failed", "pipelineId", run.PipelineId, "error", err)
	}
	if err := s.pipelineRepo.UpdateRun(ctx, run.RunId, map[string]any{
		"status":   model.PipelineStatusCancelled,
		"end_time": now,
		"duration": duration,
	}); err != nil {
		return &pipelinev1.StopPipelineResponse{
			Success: false,
			Message: "stop pipeline failed",
			Error:   s.error(500, err.Error(), "internal", nil),
		}, nil
	}
	return &pipelinev1.StopPipelineResponse{Success: true, Message: "pipeline stopped"}, nil
}

func (s *PipelineServiceImpl) PausePipeline(ctx context.Context, req *pipelinev1.PausePipelineRequest) (*pipelinev1.PausePipelineResponse, error) {

	if strings.TrimSpace(req.GetRunId()) == "" {
		return &pipelinev1.PausePipelineResponse{
			Success: false,
			Message: "runId is required",
			Error:   s.error(400, "runId is required", "validation", nil),
		}, nil
	}
	run, err := s.pipelineRepo.GetRun(ctx, req.GetRunId())
	if err != nil {
		return &pipelinev1.PausePipelineResponse{
			Success: false,
			Message: "run not found",
			Error:   s.error(404, err.Error(), "not_found", nil),
		}, nil
	}
	if strings.TrimSpace(req.GetPipelineId()) != "" && strings.TrimSpace(req.GetPipelineId()) != run.PipelineId {
		return &pipelinev1.PausePipelineResponse{
			Success: false,
			Message: "pipelineId does not match run",
			Error:   s.error(400, "pipelineId does not match run", "validation", nil),
		}, nil
	}
	if run.Status != model.PipelineStatusRunning {
		return &pipelinev1.PausePipelineResponse{
			Success: false,
			Message: "invalid run state for pause",
			Error:   s.error(409, "run is not running", "conflict", nil),
		}, nil
	}

	now := time.Now()
	if err := s.pipelineRepo.UpdateRun(ctx, run.RunId, map[string]any{
		"status": model.PipelineStatusPaused,
	}); err != nil {
		return &pipelinev1.PausePipelineResponse{
			Success: false,
			Message: "pause pipeline failed",
			Error:   s.error(500, err.Error(), "internal", nil),
		}, nil
	}
	_ = s.pipelineRepo.Update(ctx, run.PipelineId, map[string]any{
		"status":            model.PipelineStatusPaused,
		"last_sync_status":  model.PipelineSyncStatusSuccess,
		"last_sync_message": strings.TrimSpace(req.GetReason()),
		"last_synced_at":    now,
		"last_editor":       strings.TrimSpace(req.GetOperator()),
	})
	return &pipelinev1.PausePipelineResponse{Success: true, Message: "pipeline paused"}, nil
}

func (s *PipelineServiceImpl) ResumePipeline(ctx context.Context, req *pipelinev1.ResumePipelineRequest) (*pipelinev1.ResumePipelineResponse, error) {
	if strings.TrimSpace(req.GetRunId()) == "" {
		return &pipelinev1.ResumePipelineResponse{
			Success: false,
			Message: "runId is required",
			Error:   s.error(400, "runId is required", "validation", nil),
		}, nil
	}
	run, err := s.pipelineRepo.GetRun(ctx, req.GetRunId())
	if err != nil {
		return &pipelinev1.ResumePipelineResponse{
			Success: false,
			Message: "run not found",
			Error:   s.error(404, err.Error(), "not_found", nil),
		}, nil
	}
	if strings.TrimSpace(req.GetPipelineId()) != "" && strings.TrimSpace(req.GetPipelineId()) != run.PipelineId {
		return &pipelinev1.ResumePipelineResponse{
			Success: false,
			Message: "pipelineId does not match run",
			Error:   s.error(400, "pipelineId does not match run", "validation", nil),
		}, nil
	}
	if run.Status != model.PipelineStatusPaused {
		return &pipelinev1.ResumePipelineResponse{
			Success: false,
			Message: "invalid run state for resume",
			Error:   s.error(409, "run is not paused", "conflict", nil),
		}, nil
	}

	now := time.Now()
	if err := s.pipelineRepo.UpdateRun(ctx, run.RunId, map[string]any{
		"status": model.PipelineStatusRunning,
	}); err != nil {
		return &pipelinev1.ResumePipelineResponse{
			Success: false,
			Message: "resume pipeline failed",
			Error:   s.error(500, err.Error(), "internal", nil),
		}, nil
	}
	_ = s.pipelineRepo.Update(ctx, run.PipelineId, map[string]any{
		"status":            model.PipelineStatusRunning,
		"last_sync_status":  model.PipelineSyncStatusSuccess,
		"last_sync_message": strings.TrimSpace(req.GetReason()),
		"last_synced_at":    now,
		"last_editor":       strings.TrimSpace(req.GetOperator()),
	})
	return &pipelinev1.ResumePipelineResponse{Success: true, Message: "pipeline resumed"}, nil
}

func (s *PipelineServiceImpl) GetPipelineRun(ctx context.Context, req *pipelinev1.GetPipelineRunRequest) (*pipelinev1.GetPipelineRunResponse, error) {
	if strings.TrimSpace(req.GetRunId()) == "" {
		return &pipelinev1.GetPipelineRunResponse{
			Success: false,
			Message: "runId is required",
			Error:   s.error(400, "runId is required", "validation", nil),
		}, nil
	}
	run, err := s.pipelineRepo.GetRun(ctx, req.GetRunId())
	if err != nil {
		return &pipelinev1.GetPipelineRunResponse{
			Success: false,
			Message: "run not found",
			Error:   s.error(404, err.Error(), "not_found", nil),
		}, nil
	}
	return &pipelinev1.GetPipelineRunResponse{
		Success: true,
		Message: "ok",
		Run:     toPipelineRunDetail(run),
	}, nil
}

func (s *PipelineServiceImpl) ListPipelineRuns(ctx context.Context, req *pipelinev1.ListPipelineRunsRequest) (*pipelinev1.ListPipelineRunsResponse, error) {
	query := &repo.PipelineRunQuery{
		PipelineId: strings.TrimSpace(req.GetPipelineId()),
		Status:     int(req.GetStatus()),
		Page:       int(req.GetPage()),
		PageSize:   int(req.GetPageSize()),
	}
	list, total, err := s.pipelineRepo.ListRuns(ctx, query)
	if err != nil {
		return &pipelinev1.ListPipelineRunsResponse{
			Success: false,
			Message: "list runs failed",
		}, nil
	}
	out := make([]*pipelinev1.PipelineRunDetail, 0, len(list))
	for _, item := range list {
		out = append(out, toPipelineRunDetail(item))
	}
	return &pipelinev1.ListPipelineRunsResponse{
		Success:  true,
		Message:  "ok",
		Runs:     out,
		Total:    int32(total),
		Page:     int32(dispatch.Max(query.Page, 1)),
		PageSize: int32(defaultPageSize(query.PageSize)),
	}, nil
}

func (s *PipelineServiceImpl) GetPipelineSpec(ctx context.Context, req *pipelinev1.GetPipelineSpecRequest) (*pipelinev1.GetPipelineSpecResponse, error) {
	p, project, content, headSha, err := s.readDefinition(ctx, req.GetPipelineId())
	if err != nil {
		return &pipelinev1.GetPipelineSpecResponse{
			Success: false,
			Message: "get definition failed",
			Error:   s.error(500, err.Error(), "internal", nil),
		}, nil
	}

	_ = project
	format := inferSpecFormat(p.PipelineFilePath, content)
	def, err := spec.ParseContentToProto(content, format)
	if err != nil {
		return &pipelinev1.GetPipelineSpecResponse{
			Success: false,
			Message: "parse spec failed",
			Error:   s.error(500, err.Error(), "internal", nil),
		}, nil
	}
	if err := validation.NewSchemaValidator().Validate((*spec.Pipeline)(def)); err != nil {
		return &pipelinev1.GetPipelineSpecResponse{
			Success: false,
			Message: "validate spec failed",
			Error:   s.error(400, err.Error(), "validation", nil),
		}, nil
	}
	return &pipelinev1.GetPipelineSpecResponse{
		Success:          true,
		Message:          "ok",
		Spec:             def,
		Format:           format,
		HeadCommitSha:    headSha,
		Branch:           p.DefaultBranch,
		PipelineFilePath: p.PipelineFilePath,
	}, nil
}

func (s *PipelineServiceImpl) ValidatePipelineSpec(ctx context.Context, req *pipelinev1.ValidatePipelineSpecRequest) (*pipelinev1.ValidatePipelineSpecResponse, error) {
	var def *pipelinev1.Spec
	var err error
	if req.GetSpec() == nil && strings.TrimSpace(req.GetPipelineId()) != "" {
		_, _, loaded, _, err := s.readDefinition(ctx, req.GetPipelineId())
		if err != nil {
			return &pipelinev1.ValidatePipelineSpecResponse{
				Success: false,
				Message: "load spec failed",
				Error:   s.error(500, err.Error(), "internal", nil),
			}, nil
		}
		def, err = spec.ParseContentToProto(loaded, req.GetFormat())
		if err != nil {
			return &pipelinev1.ValidatePipelineSpecResponse{
				Success: false,
				Message: "validation failed",
				Error:   s.error(400, err.Error(), "validation", nil),
			}, nil
		}
		if err := validation.NewSchemaValidator().Validate((*spec.Pipeline)(def)); err != nil {
			return &pipelinev1.ValidatePipelineSpecResponse{
				Success: false,
				Message: "validation failed",
				Error:   s.error(400, err.Error(), "validation", nil),
			}, nil
		}
	} else {
		if req.GetSpec() == nil {
			return &pipelinev1.ValidatePipelineSpecResponse{
				Success: false,
				Message: "spec is required",
				Error:   s.error(400, "spec is required", "validation", nil),
			}, nil
		}
		def = req.GetSpec()
		err = validation.NewSchemaValidator().Validate((*spec.Pipeline)(def))
		if err != nil {
			return &pipelinev1.ValidatePipelineSpecResponse{
				Success: false,
				Message: "validation failed",
				Error:   s.error(400, err.Error(), "validation", nil),
			}, nil
		}
	}
	return &pipelinev1.ValidatePipelineSpecResponse{
		Success:   true,
		Message:   "spec is valid",
		JobsCount: int32(len(def.GetJobs())),
		Warnings:  []string{},
	}, nil
}

func (s *PipelineServiceImpl) SavePipelineSpec(ctx context.Context, req *pipelinev1.SavePipelineSpecRequest) (*pipelinev1.SavePipelineSpecResponse, error) {
	if strings.TrimSpace(req.GetPipelineId()) == "" || req.GetSpec() == nil {
		return &pipelinev1.SavePipelineSpecResponse{
			Success: false,
			Message: "pipelineId and spec are required",
			Error:   s.error(400, "pipelineId and spec are required", "validation", nil),
		}, nil
	}
	pipeline, project, _, _, err := s.readDefinition(ctx, req.GetPipelineId())
	if err != nil {
		return &pipelinev1.SavePipelineSpecResponse{
			Success: false,
			Message: "load spec failed",
			Error:   s.error(500, err.Error(), "internal", nil),
		}, nil
	}

	if req.GetRequestId() != "" && req.GetRequestId() == pipeline.LastSaveRequestId && pipeline.LastCommitSha != "" {
		return &pipelinev1.SavePipelineSpecResponse{
			Success:   true,
			Message:   "idempotent request",
			CommitSha: pipeline.LastCommitSha,
			Branch:    pipeline.DefaultBranch,
			SaveMode:  mapSaveModeFromModel(pipeline.SaveMode),
		}, nil
	}

	if err := validation.NewSchemaValidator().Validate((*spec.Pipeline)(req.GetSpec())); err != nil {
		return &pipelinev1.SavePipelineSpecResponse{
			Success: false,
			Message: "validation failed",
			Error:   s.error(400, err.Error(), "validation", nil),
		}, nil
	}
	serialized, err := spec.MarshalProtoByFormat(req.GetSpec(), req.GetFormat(), pipeline.PipelineFilePath)
	if err != nil {
		return &pipelinev1.SavePipelineSpecResponse{
			Success: false,
			Message: "serialize spec failed",
			Error:   s.error(400, err.Error(), "validation", nil),
		}, nil
	}

	auth := scmAuthFromProject(project)
	workdir, err := os.MkdirTemp("", "arcentra-pipeline-edit-*")
	if err != nil {
		return &pipelinev1.SavePipelineSpecResponse{
			Success: false,
			Message: "create workspace failed",
			Error:   s.error(500, err.Error(), "internal", nil),
		}, nil
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	if err := git.Clone(git.GitCloneRequest{
		Workdir: workdir,
		RepoURL: pipeline.RepoUrl,
		Branch:  pipeline.DefaultBranch,
		Auth:    git.NewGitAuthFromMap(auth),
	}); err != nil {
		return s.saveFailed(ctx, pipeline, req.GetEditor(), fmt.Sprintf("clone repo failed: %v", err)), nil
	}
	headSha, err := git.HeadSHA(git.GitHeadSHARequest{Workdir: workdir})
	if err != nil {
		return s.saveFailed(ctx, pipeline, req.GetEditor(), fmt.Sprintf("read head failed: %v", err)), nil
	}
	if req.GetExpectedHeadCommitSha() != "" && req.GetExpectedHeadCommitSha() != headSha {
		return &pipelinev1.SavePipelineSpecResponse{
			Success: false,
			Message: "head commit conflict",
			Error: s.error(409, "expectedHeadCommitSha mismatch", "conflict", map[string]string{
				"currentHeadCommitSha": headSha,
			}),
		}, nil
	}

	targetFile := filepath.Join(workdir, normalizePipelinePath(pipeline.PipelineFilePath))
	if err := os.MkdirAll(filepath.Dir(targetFile), 0o755); err != nil {
		return s.saveFailed(ctx, pipeline, req.GetEditor(), fmt.Sprintf("create directory failed: %v", err)), nil
	}
	if err := os.WriteFile(targetFile, []byte(serialized), 0o644); err != nil {
		return s.saveFailed(ctx, pipeline, req.GetEditor(), fmt.Sprintf("write spec failed: %v", err)), nil
	}

	if err := git.Add(git.GitAddRequest{
		Workdir:  workdir,
		FilePath: normalizePipelinePath(pipeline.PipelineFilePath),
	}); err != nil {
		return s.saveFailed(ctx, pipeline, req.GetEditor(), fmt.Sprintf("git add failed: %v", err)), nil
	}

	author := strings.TrimSpace(req.GetEditor())
	message := strings.TrimSpace(req.GetCommitMessage())
	if message == "" {
		message = fmt.Sprintf("update pipeline spec: %s", pipeline.Name)
	}
	if err := git.Commit(git.GitCommitRequest{
		Workdir: workdir,
		Message: message,
		Author:  author,
	}); err != nil {
		return s.saveFailed(ctx, pipeline, req.GetEditor(), fmt.Sprintf("git commit failed: %v", err)), nil
	}
	commitSha, err := git.HeadSHA(git.GitHeadSHARequest{Workdir: workdir})
	if err != nil {
		return s.saveFailed(ctx, pipeline, req.GetEditor(), fmt.Sprintf("read commit failed: %v", err)), nil
	}

	mode := pipeline.SaveMode
	targetBranch := pipeline.DefaultBranch
	saveBranch := targetBranch
	prURL := ""
	if mode == model.PipelineSaveModePR {
		saveBranch = fmt.Sprintf("arcentra/pipeline-%s-%d", pipeline.PipelineId, time.Now().Unix())
		if err := git.CheckoutNewBranch(git.GitCheckoutBranchRequest{
			Workdir: workdir,
			Branch:  saveBranch,
		}); err != nil {
			return s.saveFailed(ctx, pipeline, req.GetEditor(), fmt.Sprintf("create branch failed: %v", err)), nil
		}
	}
	if err := git.Push(git.GitPushRequest{
		Workdir: workdir,
		Remote:  "origin",
		Branch:  saveBranch,
		Auth:    git.NewGitAuthFromMap(auth),
	}); err != nil {
		return s.saveFailed(ctx, pipeline, req.GetEditor(), fmt.Sprintf("git push failed: %v", err)), nil
	}
	if mode == model.PipelineSaveModePR {
		base := pipeline.PrTargetBranch
		if strings.TrimSpace(base) == "" {
			base = targetBranch
		}
		prURL, err = git.CreatePullRequest(git.CreatePullRequestRequest{
			RepoType:        project.RepoType,
			AuthType:        project.AuthType,
			Credential:      project.Credential,
			ProjectRepoURL:  project.RepoUrl,
			PipelineRepoURL: pipeline.RepoUrl,
			TargetBranch:    base,
			SourceBranch:    saveBranch,
			Title:           message,
		})
		if err != nil {
			return s.saveFailed(ctx, pipeline, req.GetEditor(), fmt.Sprintf("create pull request failed: %v", err)), nil
		}
	}

	now := time.Now()
	_ = s.pipelineRepo.Update(ctx, pipeline.PipelineId, map[string]any{
		"last_sync_status":     model.PipelineSyncStatusSuccess,
		"last_sync_message":    "spec saved to repository",
		"last_synced_at":       now,
		"last_editor":          author,
		"last_commit_sha":      commitSha,
		"last_save_request_id": strings.TrimSpace(req.GetRequestId()),
	})

	return &pipelinev1.SavePipelineSpecResponse{
		Success:   true,
		Message:   "spec saved",
		CommitSha: commitSha,
		Branch:    saveBranch,
		SaveMode:  mapSaveModeFromModel(mode),
		PrUrl:     prURL,
		PrBranch:  saveBranch,
	}, nil
}

func (s *PipelineServiceImpl) readDefinition(ctx context.Context, pipelineId string) (*model.Pipeline, *model.Project, string, string, error) {
	if strings.TrimSpace(pipelineId) == "" {
		return nil, nil, "", "", fmt.Errorf("pipelineId is required")
	}
	p, err := s.pipelineRepo.Get(ctx, pipelineId)
	if err != nil {
		return nil, nil, "", "", err
	}
	project, err := s.projectRepo.Get(ctx, p.ProjectId)
	if err != nil {
		return nil, nil, "", "", err
	}
	content, headSha, err := s.loadDefinitionFromRepo(ctx, p, project)
	if err != nil {
		return nil, nil, "", "", err
	}
	return p, project, content, headSha, nil
}

func (s *PipelineServiceImpl) loadDefinitionFromRepo(ctx context.Context, pipeline *model.Pipeline, project *model.Project) (string, string, error) {
	auth := scmAuthFromProject(project)
	workdir, err := os.MkdirTemp("", "arcentra-pipeline-read-*")
	if err != nil {
		return "", "", err
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	if err := git.Clone(git.GitCloneRequest{
		Workdir: workdir,
		RepoURL: pipeline.RepoUrl,
		Branch:  pipeline.DefaultBranch,
		Auth:    git.NewGitAuthFromMap(auth),
	}); err != nil {
		return "", "", err
	}
	headSha, err := git.HeadSHA(git.GitHeadSHARequest{Workdir: workdir})
	if err != nil {
		return "", "", err
	}
	content, err := os.ReadFile(filepath.Join(workdir, normalizePipelinePath(pipeline.PipelineFilePath)))
	if err != nil {
		return "", "", err
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	return string(content), headSha, nil
}

func (s *PipelineServiceImpl) saveFailed(ctx context.Context, pipeline *model.Pipeline, editor, message string) *pipelinev1.SavePipelineSpecResponse {
	now := time.Now()
	_ = s.pipelineRepo.Update(ctx, pipeline.PipelineId, map[string]any{
		"last_sync_status":  model.PipelineSyncStatusFailed,
		"last_sync_message": message,
		"last_synced_at":    now,
		"last_editor":       strings.TrimSpace(editor),
	})
	return &pipelinev1.SavePipelineSpecResponse{
		Success: false,
		Message: "save spec failed",
		Error:   s.error(500, message, "internal", nil),
	}
}

func (s *PipelineServiceImpl) error(code int32, message, typ string, details map[string]string) *pipelinev1.Error {
	return &pipelinev1.Error{
		Code:    code,
		Message: message,
		Type:    typ,
		Details: serde.StringMapToStruct(details),
	}
}

func toPipelineDetail(p *model.Pipeline) *pipelinev1.PipelineDetail {
	if p == nil {
		return nil
	}
	return &pipelinev1.PipelineDetail{
		PipelineId:       p.PipelineId,
		ProjectId:        p.ProjectId,
		Name:             p.Name,
		Description:      p.Description,
		RepoUrl:          p.RepoUrl,
		DefaultBranch:    p.DefaultBranch,
		PipelineFilePath: p.PipelineFilePath,
		Status:           pipelinev1.PipelineStatus(p.Status),
		SaveMode:         mapSaveModeFromModel(p.SaveMode),
		PrTargetBranch:   p.PrTargetBranch,
		Metadata:         serde.UnmarshalStringMap(p.Metadata),
		LastSyncMessage:  p.LastSyncMessage,
		LastSyncedAt:     timepkg.ToUnix(p.LastSyncedAt),
		LastEditor:       p.LastEditor,
		LastCommitSha:    p.LastCommitSha,
		TotalRuns:        int32(p.TotalRuns),
		SuccessRuns:      int32(p.SuccessRuns),
		FailedRuns:       int32(p.FailedRuns),
		CreatedBy:        p.CreatedBy,
		IsEnabled:        int32(p.IsEnabled),
		CreatedAt:        p.CreatedAt.Unix(),
		UpdatedAt:        p.UpdatedAt.Unix(),
	}
}

func toPipelineRunDetail(run *model.PipelineRun) *pipelinev1.PipelineRunDetail {
	if run == nil {
		return nil
	}
	return &pipelinev1.PipelineRunDetail{
		RunId:               run.RunId,
		PipelineId:          run.PipelineId,
		PipelineName:        run.PipelineName,
		Branch:              run.Branch,
		CommitSha:           run.CommitSha,
		DefinitionCommitSha: run.DefinitionCommitSha,
		DefinitionPath:      run.DefinitionPath,
		Status:              pipelinev1.PipelineStatus(run.Status),
		TriggerType:         pipelinev1.TriggerType(run.TriggerType),
		TriggeredBy:         run.TriggeredBy,
		Variables:           serde.UnmarshalStringMap(run.Env),
		TotalJobs:           int32(run.TotalJobs),
		CompletedJobs:       int32(run.CompletedJobs),
		FailedJobs:          int32(run.FailedJobs),
		RunningJobs:         int32(run.RunningJobs),
		StartTime:           timepkg.ToUnix(run.StartTime),
		EndTime:             timepkg.ToUnix(run.EndTime),
		Duration:            run.Duration,
	}
}

func mapSaveModeToModel(mode pipelinev1.PipelineSaveMode) int {
	switch mode {
	case pipelinev1.PipelineSaveMode_PIPELINE_SAVE_MODE_PR:
		return model.PipelineSaveModePR
	default:
		return model.PipelineSaveModeDirect
	}
}

func mapSaveModeFromModel(mode int) pipelinev1.PipelineSaveMode {
	if mode == model.PipelineSaveModePR {
		return pipelinev1.PipelineSaveMode_PIPELINE_SAVE_MODE_PR
	}
	return pipelinev1.PipelineSaveMode_PIPELINE_SAVE_MODE_DIRECT
}

func inferSpecFormat(path string, content string) pipelinev1.SpecFormat {
	lowerPath := strings.ToLower(path)
	if strings.HasSuffix(lowerPath, ".yaml") || strings.HasSuffix(lowerPath, ".yml") {
		return pipelinev1.SpecFormat_SPEC_FORMAT_YAML
	}
	if strings.HasPrefix(strings.TrimSpace(content), "{") {
		return pipelinev1.SpecFormat_SPEC_FORMAT_JSON
	}
	return pipelinev1.SpecFormat_SPEC_FORMAT_YAML
}

func normalizePipelinePath(path string) string {
	return strings.TrimLeft(strings.TrimSpace(path), "/")
}

func defaultPageSize(pageSize int) int {
	if pageSize <= 0 {
		return 20
	}
	if pageSize > 100 {
		return 100
	}
	return pageSize
}

func scmAuthFromProject(project *model.Project) map[string]string {
	auth := map[string]string{}
	if project == nil {
		return auth
	}
	switch project.AuthType {
	case model.AuthTypeToken:
		if strings.TrimSpace(project.Credential) != "" {
			auth["token"] = strings.TrimSpace(project.Credential)
		}
	case model.AuthTypePassword:
		if strings.TrimSpace(project.Credential) != "" {
			parts := strings.SplitN(project.Credential, ":", 2)
			if len(parts) == 2 {
				auth["username"] = strings.TrimSpace(parts[0])
				auth["password"] = strings.TrimSpace(parts[1])
			} else {
				auth["password"] = strings.TrimSpace(project.Credential)
			}
		}
	case model.AuthTypeSSHKey:
		if strings.TrimSpace(project.Credential) != "" {
			auth["ssh_key"] = project.Credential
		}
	}
	return auth
}

func isDuplicateEntryError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate entry") || strings.Contains(msg, "unique constraint")
}
