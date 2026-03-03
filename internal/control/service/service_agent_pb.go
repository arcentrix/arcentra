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
	"errors"
	"fmt"
	"strings"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
)

type AgentServiceImpl struct {
	agentv1.UnimplementedAgentServiceServer
	agentService *AgentService
}

func NewAgentServiceImpl(agentService *AgentService) *AgentServiceImpl {
	return &AgentServiceImpl{
		agentService: agentService,
	}
}

func (a *AgentServiceImpl) Heartbeat(ctx context.Context, req *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
	if strings.TrimSpace(req.AgentId) != "" && a.agentService != nil && a.agentService.agentRepo != nil {
		updates := map[string]any{
			"status":         int(req.Status),
			"metrics":        fmt.Sprintf(`{"runningStepRunsCount":%d}`, req.RunningStepRunsCount),
			"updated_at":     time.Now(),
			"last_heartbeat": time.Now(),
		}
		if err := a.agentService.agentRepo.Patch(ctx, strings.TrimSpace(req.AgentId), updates); err != nil {
			log.Warnw("failed to update agent from heartbeat", "agentId", req.AgentId, "error", err)
		}
	}

	resp := &agentv1.HeartbeatResponse{
		Success:   true,
		Message:   "pong",
		Timestamp: time.Now().Unix(),
	}

	return resp, nil
}

func (a *AgentServiceImpl) Register(ctx context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	// Validate token
	if req.Token == "" {
		return nil, status.Errorf(codes.InvalidArgument, "token is required")
	}

	agentRepo := a.agentService.agentRepo
	var agentId string
	var err error

	// Extract agentId from token (token format: agentId:signature)
	// If request provides agentId, use it for validation; otherwise extract from token
	tokenParts := strings.SplitN(req.Token, ":", 2)
	if len(tokenParts) != 2 {
		return nil, status.Errorf(codes.InvalidArgument, "invalid token format: expected agentId:signature")
	}
	tokenAgentId := tokenParts[0]

	// Use agentId from request if provided, otherwise use agentId from token
	if req.AgentId != "" {
		agentId = req.AgentId
		// Validate that request agentId matches token agentId
		if agentId != tokenAgentId {
			log.Warnw("agentId mismatch", "requestAgentId", agentId, "tokenAgentId", tokenAgentId)
			return nil, status.Errorf(codes.InvalidArgument, "agentId mismatch: request agentId does not match token")
		}
	} else {
		agentId = tokenAgentId
	}

	// Verify token by regenerating it and comparing
	expectedToken, err := a.agentService.GenerateAgentToken(ctx, agentId)
	if err != nil {
		log.Errorw("failed to generate token for verification", "agentId", agentId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to verify token")
	}

	if req.Token != expectedToken {
		log.Warnw("token verification failed", "agentId", agentId)
		return nil, status.Errorf(codes.Unauthenticated, "invalid token")
	}

	// Check if agent exists
	_, err = agentRepo.Get(ctx, agentId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Errorf(codes.NotFound, "agent not found: %s", agentId)
		}
		log.Errorw("failed to get agent", "agentId", agentId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to get agent")
	}

	// Update agent information from registration request
	updates := make(map[string]any)

	if req.Ip != "" {
		updates["address"] = req.Ip
	}
	if req.Os != "" {
		updates["os"] = req.Os
	}
	if req.Arch != "" {
		updates["arch"] = req.Arch
	}
	if req.Version != "" {
		updates["version"] = req.Version
	}
	if len(req.Labels) > 0 {
		encoded, marshalErr := sonic.Marshal(req.Labels)
		if marshalErr == nil {
			updates["labels"] = string(encoded)
		}
	}
	updates["status"] = 1 // Set status to online
	updates["last_heartbeat"] = time.Now()

	if len(updates) > 0 {
		if err = agentRepo.Patch(ctx, agentId, updates); err != nil {
			log.Errorw("failed to update agent during registration", "agentId", agentId, "error", err)
			return nil, status.Errorf(codes.Internal, "failed to update agent")
		}
	}

	// Get agent detail to return heartbeat interval
	detail, err := agentRepo.GetDetail(ctx, agentId)
	if err != nil {
		log.Errorw("failed to get agent detail", "agentId", agentId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to get agent detail")
	}

	heartbeatInterval := int64(60) // default

	// Parse labels from JSON
	labels := make(map[string]string)
	if len(detail.Labels) > 0 {
		if unmarshalErr := sonic.Unmarshal(detail.Labels, &labels); unmarshalErr != nil {
			log.Warnw("failed to parse labels", "agentId", agentId, "error", unmarshalErr)
			// Continue with empty labels if parsing fails
		}
	}

	return &agentv1.RegisterResponse{
		Success:           true,
		Message:           "registration successful",
		AgentId:           agentId,
		HeartbeatInterval: heartbeatInterval,
		Labels:            labels,
	}, nil
}

func (a *AgentServiceImpl) Unregister(ctx context.Context, req *agentv1.UnregisterRequest) (*agentv1.UnregisterResponse, error) {
	agentId := strings.TrimSpace(req.AgentId)
	if agentId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "agentId is required")
	}
	if a.agentService == nil || a.agentService.agentRepo == nil {
		return &agentv1.UnregisterResponse{Success: false, Message: "agent repository unavailable"}, nil
	}
	updates := map[string]any{
		"status":         int(agentv1.AgentStatus_AGENT_STATUS_OFFLINE),
		"updated_at":     time.Now(),
		"last_heartbeat": time.Now(),
	}
	if err := a.agentService.agentRepo.Patch(ctx, agentId, updates); err != nil {
		log.Errorw("failed to unregister agent", "agentId", agentId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to unregister agent")
	}
	return &agentv1.UnregisterResponse{
		Success: true,
		Message: "unregistered",
	}, nil
}

func (a *AgentServiceImpl) FetchStepRun(ctx context.Context, req *agentv1.FetchStepRunRequest) (*agentv1.FetchStepRunResponse, error) {
	if a.agentService == nil || a.agentService.stepRunRepo == nil {
		return &agentv1.FetchStepRunResponse{Success: false, Message: "step run repository unavailable"}, nil
	}
	agentId := strings.TrimSpace(req.AgentId)
	if agentId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "agentId is required")
	}
	maxRuns := int(req.MaxStepRuns)
	if maxRuns <= 0 {
		maxRuns = 1
	}
	filter := repo.StepRunFilter{
		AgentId:  agentId,
		Page:     1,
		PageSize: maxRuns,
		SortBy:   "created_at",
		SortDesc: false,
	}
	stepRuns, _, err := a.agentService.stepRunRepo.List(ctx, filter)
	if err != nil {
		log.Errorw("fetch step runs failed", "agentId", agentId, "error", err)
		return nil, status.Errorf(codes.Internal, "fetch step runs failed")
	}

	respStepRuns := make([]*agentv1.StepRun, 0, len(stepRuns))
	for i := range stepRuns {
		item := stepRuns[i]
		if item.Status != 1 && item.Status != 2 {
			// only dispatch pending/queued records for now
			continue
		}
		_ = a.agentService.stepRunRepo.PatchByStepRunId(ctx, item.StepRunID, map[string]any{
			"status": int(2),
		})
		respStepRuns = append(respStepRuns, convertStepRunModelToAgentStepRun(&item))
	}

	return &agentv1.FetchStepRunResponse{
		Success:  true,
		Message:  "ok",
		StepRuns: respStepRuns,
	}, nil
}

func (a *AgentServiceImpl) ReportStepRunStatus(
	ctx context.Context,
	req *agentv1.ReportStepRunStatusRequest,
) (*agentv1.ReportStepRunStatusResponse, error) {
	if a.agentService == nil || a.agentService.stepRunRepo == nil {
		return &agentv1.ReportStepRunStatusResponse{Success: false, Message: "step run repository unavailable"}, nil
	}
	stepRunId := strings.TrimSpace(req.StepRunId)
	if stepRunId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "stepRunId is required")
	}
	updates := map[string]any{
		"status":        int(req.Status),
		"exit_code":     req.ExitCode,
		"error_message": strings.TrimSpace(req.ErrorMessage),
	}
	if req.StartTime > 0 {
		startAt := time.Unix(req.StartTime, 0)
		updates["start_time"] = startAt
	}
	if req.EndTime > 0 {
		endAt := time.Unix(req.EndTime, 0)
		updates["end_time"] = endAt
	}
	if len(req.Metrics) > 0 {
		encoded, err := sonic.Marshal(req.Metrics)
		if err == nil {
			updates["secrets"] = string(encoded)
		}
	}
	if err := a.agentService.stepRunRepo.PatchByStepRunId(ctx, stepRunId, updates); err != nil {
		log.Errorw("report step run status failed", "stepRunId", stepRunId, "error", err)
		return nil, status.Errorf(codes.Internal, "report step run status failed")
	}
	return &agentv1.ReportStepRunStatusResponse{Success: true, Message: "ok"}, nil
}

func (a *AgentServiceImpl) CancelStepRun(ctx context.Context, req *agentv1.CancelStepRunRequest) (*agentv1.CancelStepRunResponse, error) {
	if a.agentService == nil || a.agentService.stepRunRepo == nil {
		return &agentv1.CancelStepRunResponse{Success: false, Message: "step run repository unavailable"}, nil
	}
	stepRunId := strings.TrimSpace(req.StepRunId)
	if stepRunId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "stepRunId is required")
	}
	updates := map[string]any{
		"status":        6,
		"error_message": strings.TrimSpace(req.Reason),
		"end_time":      time.Now(),
	}
	if err := a.agentService.stepRunRepo.PatchByStepRunId(ctx, stepRunId, updates); err != nil {
		log.Errorw("cancel step run failed", "stepRunId", stepRunId, "error", err)
		return nil, status.Errorf(codes.Internal, "cancel step run failed")
	}
	return &agentv1.CancelStepRunResponse{Success: true, Message: "cancelled"}, nil
}

func (a *AgentServiceImpl) UpdateLabels(ctx context.Context, req *agentv1.UpdateLabelsRequest) (*agentv1.UpdateLabelsResponse, error) {
	if a.agentService == nil || a.agentService.agentRepo == nil {
		return &agentv1.UpdateLabelsResponse{Success: false, Message: "agent repository unavailable"}, nil
	}
	agentId := strings.TrimSpace(req.AgentId)
	if agentId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "agentId is required")
	}
	detail, err := a.agentService.agentRepo.GetDetail(ctx, agentId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load agent detail failed")
	}
	current := map[string]string{}
	if detail != nil && len(detail.Labels) > 0 {
		_ = sonic.Unmarshal(detail.Labels, &current)
	}
	updated := map[string]string{}
	if req.Merge {
		for k, v := range current {
			updated[k] = v
		}
	}
	for k, v := range req.Labels {
		updated[k] = v
	}
	encoded, err := sonic.Marshal(updated)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal labels failed")
	}
	if err := a.agentService.agentRepo.Patch(ctx, agentId, map[string]any{"labels": string(encoded)}); err != nil {
		return nil, status.Errorf(codes.Internal, "update labels failed")
	}
	return &agentv1.UpdateLabelsResponse{Success: true, Message: "updated", Labels: updated}, nil
}

func convertStepRunModelToAgentStepRun(item *model.StepRun) *agentv1.StepRun {
	if item == nil {
		return nil
	}
	return &agentv1.StepRun{
		StepRunId:     item.StepRunID,
		PipelineId:    item.PipelineID,
		PipelineRunId: item.PipelineRunID,
		JobId:         item.JobID,
		JobName:       deriveJobName(item.PipelineID, item.JobID),
		StepName:      item.Name,
		StepIndex:     int32(item.StepIndex),
		Uses:          item.Uses,
		Action:        item.Action,
		Args:          parseAnyStruct(item.Args),
		Env:           parseStringMap(item.Env),
		Workspace:     item.Workspace,
		Timeout:       item.Timeout,
		Secrets:       parseStringMap(item.Secrets),
	}
}

func parseAnyStruct(raw string) *structpb.Struct {
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

func parseStringMap(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}
	}
	result := map[string]string{}
	if err := sonic.Unmarshal([]byte(raw), &result); err != nil {
		return map[string]string{}
	}
	return result
}
