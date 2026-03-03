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

package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
	"google.golang.org/protobuf/types/known/structpb"
)

type StepRunServiceImpl struct {
	steprunv1.UnimplementedStepRunServiceServer
	stepRunRepo repo.IStepRunRepository
}

func NewStepRunServiceImpl(stepRunRepo repo.IStepRunRepository) *StepRunServiceImpl {
	return &StepRunServiceImpl{stepRunRepo: stepRunRepo}
}

func (s *StepRunServiceImpl) CreateStepRun(
	ctx context.Context,
	req *steprunv1.CreateStepRunRequest,
) (*steprunv1.CreateStepRunResponse, error) {
	if s.stepRunRepo == nil {
		return &steprunv1.CreateStepRunResponse{Success: false, Message: "step run repository unavailable"}, nil
	}
	if strings.TrimSpace(req.PipelineId) == "" || strings.TrimSpace(req.JobId) == "" || strings.TrimSpace(req.StepName) == "" {
		return &steprunv1.CreateStepRunResponse{Success: false, Message: "pipelineId/jobId/stepName are required"}, nil
	}
	stepRunId := strings.TrimSpace(req.PipelineId) + "-" + strings.TrimSpace(req.JobName) + "-" + strings.TrimSpace(req.StepName)
	if strings.Trim(stepRunId, "-") == "" {
		stepRunId = id.ShortID()
	}
	entity := &model.StepRun{
		StepRunID:       stepRunId,
		Name:            strings.TrimSpace(req.StepName),
		PipelineID:      strings.TrimSpace(req.PipelineId),
		PipelineRunID:   strings.TrimSpace(req.PipelineRunId),
		JobID:           strings.TrimSpace(req.JobId),
		StepIndex:       int(req.StepIndex),
		Status:          int(steprunv1.StepRunStatus_STEP_RUN_STATUS_PENDING),
		Uses:            strings.TrimSpace(req.Uses),
		Action:          strings.TrimSpace(req.Action),
		Args:            mustJSONStruct(req.Args),
		Workspace:       strings.TrimSpace(req.Workspace),
		Env:             mustJSON(req.Env),
		Secrets:         mustJSON(req.Secrets),
		Timeout:         strings.TrimSpace(req.Timeout),
		ContinueOnError: boolToInt(req.ContinueOnError),
		When:            strings.TrimSpace(req.When),
		LabelSelector:   mustJSONSelector(req.AgentSelector),
		CreatedBy:       "system",
	}
	if err := s.stepRunRepo.Create(ctx, entity); err != nil {
		log.Errorw("create step run failed", "stepRunId", stepRunId, "error", err)
		return &steprunv1.CreateStepRunResponse{Success: false, Message: fmt.Sprintf("create step run failed: %v", err)}, nil
	}
	return &steprunv1.CreateStepRunResponse{Success: true, Message: "created", StepRunId: stepRunId}, nil
}

func (s *StepRunServiceImpl) GetStepRun(ctx context.Context, req *steprunv1.GetStepRunRequest) (*steprunv1.GetStepRunResponse, error) {
	if s.stepRunRepo == nil {
		return &steprunv1.GetStepRunResponse{Success: false, Message: "step run repository unavailable"}, nil
	}
	stepRunId := strings.TrimSpace(req.StepRunId)
	if stepRunId == "" {
		return &steprunv1.GetStepRunResponse{Success: false, Message: "stepRunId is required"}, nil
	}
	entity, err := s.stepRunRepo.GetByStepRunId(ctx, stepRunId)
	if err != nil {
		return &steprunv1.GetStepRunResponse{Success: false, Message: fmt.Sprintf("query step run failed: %v", err)}, nil
	}
	if entity == nil {
		return &steprunv1.GetStepRunResponse{Success: false, Message: "step run not found"}, nil
	}
	return &steprunv1.GetStepRunResponse{
		Success: true,
		Message: "ok",
		StepRun: convertStepRunModel(entity),
	}, nil
}

func (s *StepRunServiceImpl) ListStepRuns(
	ctx context.Context,
	req *steprunv1.ListStepRunsRequest,
) (*steprunv1.ListStepRunsResponse, error) {
	if s.stepRunRepo == nil {
		return &steprunv1.ListStepRunsResponse{Success: false, Message: "step run repository unavailable"}, nil
	}
	filter := repo.StepRunFilter{
		PipelineId:    strings.TrimSpace(req.PipelineId),
		PipelineRunId: strings.TrimSpace(req.PipelineRunId),
		JobId:         strings.TrimSpace(req.JobId),
		StepName:      strings.TrimSpace(req.StepName),
		AgentId:       strings.TrimSpace(req.AgentId),
		Status:        int(req.Status),
		Page:          int(req.Page),
		PageSize:      int(req.PageSize),
		SortBy:        strings.TrimSpace(req.SortBy),
		SortDesc:      req.SortDesc,
	}
	stepRuns, total, err := s.stepRunRepo.List(ctx, filter)
	if err != nil {
		return &steprunv1.ListStepRunsResponse{Success: false, Message: fmt.Sprintf("list step runs failed: %v", err)}, nil
	}
	respRuns := make([]*steprunv1.StepRunDetail, 0, len(stepRuns))
	for i := range stepRuns {
		respRuns = append(respRuns, convertStepRunModel(&stepRuns[i]))
	}
	page := req.Page
	if page <= 0 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	return &steprunv1.ListStepRunsResponse{
		Success:  true,
		Message:  "ok",
		StepRuns: respRuns,
		Total:    int32(total),
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (s *StepRunServiceImpl) UpdateStepRun(
	ctx context.Context,
	req *steprunv1.UpdateStepRunRequest,
) (*steprunv1.UpdateStepRunResponse, error) {
	if s.stepRunRepo == nil {
		return &steprunv1.UpdateStepRunResponse{Success: false, Message: "step run repository unavailable"}, nil
	}
	stepRunId := strings.TrimSpace(req.StepRunId)
	if stepRunId == "" {
		return &steprunv1.UpdateStepRunResponse{Success: false, Message: "stepRunId is required"}, nil
	}
	updates := map[string]any{}
	if req.Env != nil {
		updates["env"] = mustJSON(req.Env)
	}
	if strings.TrimSpace(req.Timeout) != "" {
		updates["timeout"] = strings.TrimSpace(req.Timeout)
	}
	if len(updates) == 0 {
		return &steprunv1.UpdateStepRunResponse{Success: true, Message: "no changes"}, nil
	}
	if err := s.stepRunRepo.PatchByStepRunId(ctx, stepRunId, updates); err != nil {
		return &steprunv1.UpdateStepRunResponse{Success: false, Message: fmt.Sprintf("update step run failed: %v", err)}, nil
	}
	return &steprunv1.UpdateStepRunResponse{Success: true, Message: "updated"}, nil
}

func (s *StepRunServiceImpl) DeleteStepRun(
	ctx context.Context,
	req *steprunv1.DeleteStepRunRequest,
) (*steprunv1.DeleteStepRunResponse, error) {
	if s.stepRunRepo == nil {
		return &steprunv1.DeleteStepRunResponse{Success: false, Message: "step run repository unavailable"}, nil
	}
	stepRunId := strings.TrimSpace(req.StepRunId)
	if stepRunId == "" {
		return &steprunv1.DeleteStepRunResponse{Success: false, Message: "stepRunId is required"}, nil
	}
	if err := s.stepRunRepo.DeleteByStepRunId(ctx, stepRunId); err != nil {
		return &steprunv1.DeleteStepRunResponse{Success: false, Message: fmt.Sprintf("delete step run failed: %v", err)}, nil
	}
	return &steprunv1.DeleteStepRunResponse{Success: true, Message: "deleted"}, nil
}

func (s *StepRunServiceImpl) CancelStepRun(
	ctx context.Context,
	req *steprunv1.CancelStepRunRequest,
) (*steprunv1.CancelStepRunResponse, error) {
	if s.stepRunRepo == nil {
		return &steprunv1.CancelStepRunResponse{Success: false, Message: "step run repository unavailable"}, nil
	}
	stepRunId := strings.TrimSpace(req.StepRunId)
	if stepRunId == "" {
		return &steprunv1.CancelStepRunResponse{Success: false, Message: "stepRunId is required"}, nil
	}
	updates := map[string]any{
		"status":        int(steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED),
		"error_message": strings.TrimSpace(req.Reason),
	}
	now := time.Now()
	updates["end_time"] = now
	if err := s.stepRunRepo.PatchByStepRunId(ctx, stepRunId, updates); err != nil {
		return &steprunv1.CancelStepRunResponse{Success: false, Message: fmt.Sprintf("cancel step run failed: %v", err)}, nil
	}
	return &steprunv1.CancelStepRunResponse{Success: true, Message: "cancelled"}, nil
}

func (s *StepRunServiceImpl) RetryStepRun(
	ctx context.Context,
	req *steprunv1.RetryStepRunRequest,
) (*steprunv1.RetryStepRunResponse, error) {
	if s.stepRunRepo == nil {
		return &steprunv1.RetryStepRunResponse{Success: false, Message: "step run repository unavailable"}, nil
	}
	oldStepRunId := strings.TrimSpace(req.StepRunId)
	if oldStepRunId == "" {
		return &steprunv1.RetryStepRunResponse{Success: false, Message: "stepRunId is required"}, nil
	}
	oldRun, err := s.stepRunRepo.GetByStepRunId(ctx, oldStepRunId)
	if err != nil {
		return &steprunv1.RetryStepRunResponse{Success: false, Message: fmt.Sprintf("load step run failed: %v", err)}, nil
	}
	if oldRun == nil {
		return &steprunv1.RetryStepRunResponse{Success: false, Message: "step run not found"}, nil
	}
	newStepRunId := oldStepRunId + "-retry-" + id.ShortID()
	newRun := *oldRun
	newRun.ID = 0
	newRun.StepRunID = newStepRunId
	newRun.Status = int(steprunv1.StepRunStatus_STEP_RUN_STATUS_PENDING)
	newRun.CurrentRetry = oldRun.CurrentRetry + 1
	newRun.ExitCode = nil
	newRun.ErrorMessage = ""
	newRun.StartTime = nil
	newRun.EndTime = nil
	newRun.Duration = 0
	if err := s.stepRunRepo.Create(ctx, &newRun); err != nil {
		return &steprunv1.RetryStepRunResponse{Success: false, Message: fmt.Sprintf("create retry step run failed: %v", err)}, nil
	}
	return &steprunv1.RetryStepRunResponse{Success: true, Message: "retry created", NewStepRunId: newStepRunId}, nil
}

func (s *StepRunServiceImpl) ListStepRunArtifacts(
	ctx context.Context,
	req *steprunv1.ListStepRunArtifactsRequest,
) (*steprunv1.ListStepRunArtifactsResponse, error) {
	if s.stepRunRepo == nil {
		return &steprunv1.ListStepRunArtifactsResponse{Success: false, Message: "step run repository unavailable"}, nil
	}
	stepRunId := strings.TrimSpace(req.StepRunId)
	if stepRunId == "" {
		return &steprunv1.ListStepRunArtifactsResponse{Success: false, Message: "stepRunId is required"}, nil
	}
	artifacts, err := s.stepRunRepo.ListArtifactsByStepRunId(ctx, stepRunId)
	if err != nil {
		return &steprunv1.ListStepRunArtifactsResponse{Success: false, Message: fmt.Sprintf("list artifacts failed: %v", err)}, nil
	}
	resp := make([]*steprunv1.Artifact, 0, len(artifacts))
	for i := range artifacts {
		item := artifacts[i]
		expireAt := int64(0)
		if item.ExpiredAt != nil {
			expireAt = item.ExpiredAt.Unix()
		}
		resp = append(resp, &steprunv1.Artifact{
			ArtifactId:  item.ArtifactID,
			Name:        item.Name,
			Path:        item.Path,
			Size:        item.Size,
			DownloadUrl: item.StoragePath,
			CreatedAt:   item.CreatedAt.Unix(),
			ExpireAt:    expireAt,
		})
	}
	return &steprunv1.ListStepRunArtifactsResponse{Success: true, Message: "ok", Artifacts: resp}, nil
}

func convertStepRunModel(item *model.StepRun) *steprunv1.StepRunDetail {
	if item == nil {
		return nil
	}
	detail := &steprunv1.StepRunDetail{
		StepRunId:       item.StepRunID,
		PipelineId:      item.PipelineID,
		PipelineRunId:   item.PipelineRunID,
		JobId:           item.JobID,
		JobName:         deriveJobName(item.PipelineID, item.JobID),
		StepName:        item.Name,
		StepIndex:       int32(item.StepIndex),
		Uses:            item.Uses,
		Action:          item.Action,
		Args:            parseJSONStringToStruct(item.Args),
		Status:          steprunv1.StepRunStatus(item.Status),
		Env:             parseJSONStringMap(item.Env),
		Workspace:       item.Workspace,
		Timeout:         item.Timeout,
		Artifacts:       nil,
		ContinueOnError: item.ContinueOnError == 1,
		When:            item.When,
		AgentSelector:   parseJSONStringToSelector(item.LabelSelector),
		RunOnAgent:      strings.TrimSpace(item.AgentID) != "",
		AgentId:         item.AgentID,
		ErrorMessage:    item.ErrorMessage,
		CreatedAt:       item.CreatedAt.UnixMilli(),
		Duration:        item.Duration,
		CreatedBy:       item.CreatedBy,
		Metrics:         parseJSONStringMap(item.Secrets),
	}
	if item.ExitCode != nil {
		detail.ExitCode = int32(*item.ExitCode)
	}
	if item.StartTime != nil {
		detail.StartedAt = item.StartTime.UnixMilli()
	}
	if item.EndTime != nil {
		detail.FinishedAt = item.EndTime.UnixMilli()
	}
	return detail
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func mustJSONStruct(data *structpb.Struct) string {
	if data == nil {
		return ""
	}
	encoded, err := sonic.Marshal(data.AsMap())
	if err != nil {
		return ""
	}
	return string(encoded)
}

func mustJSON(data map[string]string) string {
	if len(data) == 0 {
		return ""
	}
	encoded, err := sonic.Marshal(data)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func mustJSONSelector(selector *steprunv1.AgentSelector) string {
	if selector == nil {
		return ""
	}
	encoded, err := sonic.Marshal(selector)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func parseJSONStringMap(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}
	}
	result := map[string]string{}
	if err := sonic.Unmarshal([]byte(raw), &result); err != nil {
		return map[string]string{}
	}
	return result
}

func parseJSONStringToStruct(raw string) *structpb.Struct {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	values := map[string]any{}
	if err := sonic.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	st, err := structpb.NewStruct(values)
	if err != nil {
		return nil
	}
	return st
}

func parseJSONStringToSelector(raw string) *steprunv1.AgentSelector {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var selector steprunv1.AgentSelector
	if err := sonic.Unmarshal([]byte(raw), &selector); err != nil {
		return nil
	}
	return &selector
}
