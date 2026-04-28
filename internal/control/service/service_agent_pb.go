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
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
)

// AgentServiceImpl implements the gRPC AgentService for control plane.
type AgentServiceImpl struct {
	agentv1.UnimplementedAgentServiceServer
	agentService *AgentService
	storageRepo  repo.IStorageRepository
}

// NewAgentServiceImpl creates a new agent gRPC service.
func NewAgentServiceImpl(agentService *AgentService, storageRepo repo.IStorageRepository) *AgentServiceImpl {
	return &AgentServiceImpl{
		agentService: agentService,
		storageRepo:  storageRepo,
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
			log.Warnw("failed to update agent from heartbeat", "agentID", req.AgentId, "error", err)
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
	registrationToken := strings.TrimSpace(req.RegistrationToken)

	// Dynamic registration path
	if registrationToken != "" {
		return a.dynamicRegister(ctx, req)
	}

	// Legacy path: if token is provided, validate as HMAC-SHA256 per-agent token
	if req.Token != "" {
		return a.legacyRegister(ctx, req)
	}

	return nil, status.Errorf(codes.InvalidArgument, "registration_token or token is required")
}

// dynamicRegister handles agent registration via a shared registration token.
func (a *AgentServiceImpl) dynamicRegister(ctx context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	// Validate registration token
	if err := a.agentService.ValidateRegistrationToken(ctx, req.RegistrationToken); err != nil {
		log.Warnw("registration token validation failed", "error", err)
		return nil, status.Errorf(codes.Unauthenticated, "invalid registration token")
	}

	// Generate new agent ID
	agentID := id.ShortID()

	// Determine agent name
	agentName := strings.TrimSpace(req.AgentName)
	if agentName == "" {
		agentName = agentID
	}

	// Encode labels
	labelsJSON := "{}"
	if len(req.Labels) > 0 {
		if encoded, err := sonic.Marshal(req.Labels); err == nil {
			labelsJSON = string(encoded)
		}
	}

	agent := &model.Agent{
		AgentID:   agentID,
		AgentName: agentName,
		Address:   req.Ip,
		OS:        req.Os,
		Arch:      req.Arch,
		Version:   req.Version,
		Status:    1, // online
		Labels:    []byte(labelsJSON),
		Metrics:   "/metrics",
	}

	if err := a.agentService.DynamicRegisterAgent(ctx, agentID, agentName, agent); err != nil {
		log.Errorw("failed to create agent via dynamic registration", "error", err)
		return nil, status.Errorf(codes.Internal, "failed to register agent")
	}

	// Generate per-agent auth token
	authToken, err := a.agentService.GenerateAgentToken(ctx, agentID)
	if err != nil {
		log.Errorw("failed to generate auth token", "agentID", agentID, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to generate auth token")
	}

	// Check if pending approval
	pendingApproval := agent.IsEnabled == 0

	// Parse labels for response
	respLabels := req.Labels
	if respLabels == nil {
		respLabels = make(map[string]string)
	}

	resp := &agentv1.RegisterResponse{
		Success:           true,
		Message:           "registration successful",
		AgentId:           agentID,
		HeartbeatInterval: 60,
		Labels:            respLabels,
		AuthToken:         authToken,
		PendingApproval:   pendingApproval,
	}

	if a.storageRepo != nil {
		if sc := a.buildStorageConfig(ctx); sc != nil {
			resp.Storage = sc
		}
	}

	return resp, nil
}

// legacyRegister handles the existing HMAC-SHA256 per-agent token registration.
func (a *AgentServiceImpl) legacyRegister(ctx context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	agentRepo := a.agentService.agentRepo

	// Extract agentID from token (token format: agentID:signature)
	tokenParts := strings.SplitN(req.Token, ":", 2)
	if len(tokenParts) != 2 {
		return nil, status.Errorf(codes.InvalidArgument, "invalid token format: expected agentID:signature")
	}
	tokenAgentID := tokenParts[0]

	agentID := req.AgentId
	if agentID == "" {
		agentID = tokenAgentID
	}
	if agentID != tokenAgentID {
		return nil, status.Errorf(codes.InvalidArgument, "agentID mismatch: request agentID does not match token")
	}

	// Verify token
	expectedToken, err := a.agentService.GenerateAgentToken(ctx, agentID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to verify token")
	}
	if req.Token != expectedToken {
		return nil, status.Errorf(codes.Unauthenticated, "invalid token")
	}

	// Check agent exists
	_, err = agentRepo.Get(ctx, agentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Errorf(codes.NotFound, "agent not found: %s", agentID)
		}
		return nil, status.Errorf(codes.Internal, "failed to get agent")
	}

	// Update agent info
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
		if encoded, marshalErr := sonic.Marshal(req.Labels); marshalErr == nil {
			updates["labels"] = string(encoded)
		}
	}
	updates["status"] = 1
	updates["last_heartbeat"] = time.Now()

	if len(updates) > 0 {
		if err = agentRepo.Patch(ctx, agentID, updates); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to update agent")
		}
	}

	detail, err := agentRepo.GetDetail(ctx, agentID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get agent detail")
	}

	labels := make(map[string]string)
	if len(detail.Labels) > 0 {
		_ = sonic.Unmarshal(detail.Labels, &labels)
	}

	resp := &agentv1.RegisterResponse{
		Success:           true,
		Message:           "registration successful",
		AgentId:           agentID,
		HeartbeatInterval: 60,
		Labels:            labels,
	}

	if a.storageRepo != nil {
		if sc := a.buildStorageConfig(ctx); sc != nil {
			resp.Storage = sc
		}
	}

	return resp, nil
}

// buildStorageConfig reads the default storage configuration from DB and
// converts it to the proto StorageConfig delivered to agents.
func (a *AgentServiceImpl) buildStorageConfig(ctx context.Context) *agentv1.StorageConfig {
	sc, err := a.storageRepo.GetDefault(ctx)
	if err != nil || sc == nil {
		return nil
	}
	var detail model.StorageConfigDetail
	if err := sonic.Unmarshal(sc.Config, &detail); err != nil {
		log.Warnw("failed to parse default storage config for agent", "error", err)
		return nil
	}
	return &agentv1.StorageConfig{
		Provider:  sc.StorageType,
		Endpoint:  detail.Endpoint,
		Bucket:    detail.Bucket,
		Region:    detail.Region,
		AccessKey: detail.AccessKey,
		SecretKey: detail.SecretKey,
		BasePath:  detail.BasePath,
		UseSsl:    detail.UseTLS,
	}
}

func (a *AgentServiceImpl) Unregister(ctx context.Context, req *agentv1.UnregisterRequest) (*agentv1.UnregisterResponse, error) {
	agentID := strings.TrimSpace(req.AgentId)
	if agentID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "agentID is required")
	}
	if a.agentService == nil || a.agentService.agentRepo == nil {
		return &agentv1.UnregisterResponse{Success: false, Message: "agent repository unavailable"}, nil
	}
	updates := map[string]any{
		"status":         int(agentv1.AgentStatus_AGENT_STATUS_OFFLINE),
		"updated_at":     time.Now(),
		"last_heartbeat": time.Now(),
	}
	if err := a.agentService.agentRepo.Patch(ctx, agentID, updates); err != nil {
		log.Errorw("failed to unregister agent", "agentID", agentID, "error", err)
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
	agentID := strings.TrimSpace(req.AgentId)
	if agentID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "agentID is required")
	}
	maxRuns := int(req.MaxStepRuns)
	if maxRuns <= 0 {
		maxRuns = 1
	}
	filter := repo.StepRunFilter{
		AgentID:  agentID,
		Page:     1,
		PageSize: maxRuns,
		SortBy:   "created_at",
		SortDesc: false,
	}
	stepRuns, _, err := a.agentService.stepRunRepo.List(ctx, filter)
	if err != nil {
		log.Errorw("fetch step runs failed", "agentID", agentID, "error", err)
		return nil, status.Errorf(codes.Internal, "fetch step runs failed")
	}

	respStepRuns := make([]*agentv1.StepRun, 0, len(stepRuns))
	for i := range stepRuns {
		item := stepRuns[i]
		if item.Status != 1 && item.Status != 2 {
			// only dispatch pending/queued records for now
			continue
		}
		_ = a.agentService.stepRunRepo.PatchByStepRunID(ctx, item.StepRunID, map[string]any{
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
	stepRunID := strings.TrimSpace(req.StepRunId)
	if stepRunID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "stepRunID is required")
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
	if err := a.agentService.stepRunRepo.PatchByStepRunID(ctx, stepRunID, updates); err != nil {
		log.Debugw("step run patch miss, trying job run", "id", stepRunID, "error", err)
	}

	// Also update JobRun table if the ID belongs to a job run.
	if a.agentService.jobRunRepo != nil {
		_ = a.agentService.jobRunRepo.UpdateByJobRunID(ctx, stepRunID, updates)
	}

	return &agentv1.ReportStepRunStatusResponse{Success: true, Message: "ok"}, nil
}

// ReportJobRunStatus handles dedicated job-level status reports from Agent.
func (a *AgentServiceImpl) ReportJobRunStatus(
	ctx context.Context,
	req *agentv1.ReportJobRunStatusRequest,
) (*agentv1.ReportJobRunStatusResponse, error) {
	if a.agentService == nil || a.agentService.jobRunRepo == nil {
		return &agentv1.ReportJobRunStatusResponse{Success: false, Message: "job run repository unavailable"}, nil
	}
	jobRunID := strings.TrimSpace(req.JobRunId)
	if jobRunID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "jobRunID is required")
	}
	updates := map[string]any{
		"status":        int(req.Status),
		"error_message": strings.TrimSpace(req.ErrorMessage),
	}
	if req.StartTime > 0 {
		updates["start_time"] = time.Unix(req.StartTime, 0)
	}
	if req.EndTime > 0 {
		updates["end_time"] = time.Unix(req.EndTime, 0)
	}
	if len(req.ArtifactUris) > 0 {
		if encoded, err := sonic.Marshal(req.ArtifactUris); err == nil {
			updates["artifact_uris"] = string(encoded)
		}
	}
	if req.AgentId != "" {
		updates["agent_id"] = strings.TrimSpace(req.AgentId)
	}
	if err := a.agentService.jobRunRepo.UpdateByJobRunID(ctx, jobRunID, updates); err != nil {
		log.Errorw("report job run status failed", "jobRunId", jobRunID, "error", err)
		return nil, status.Errorf(codes.Internal, "update job run failed")
	}
	return &agentv1.ReportJobRunStatusResponse{Success: true, Message: "ok"}, nil
}

// CancelJobRun cancels a running job on the Agent side. The control plane
// calls this RPC on the Agent gRPC server to propagate cancellation.
func (a *AgentServiceImpl) CancelJobRun(_ context.Context, req *agentv1.CancelJobRunRequest) (*agentv1.CancelJobRunResponse, error) {
	jobRunID := strings.TrimSpace(req.JobRunId)
	if jobRunID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "jobRunID is required")
	}
	if a.agentService == nil || a.agentService.jobRunRepo == nil {
		return &agentv1.CancelJobRunResponse{Success: false, Message: "job run repository unavailable"}, nil
	}
	updates := map[string]any{
		"status":        6,
		"error_message": strings.TrimSpace(req.Reason),
		"end_time":      time.Now(),
	}
	if err := a.agentService.jobRunRepo.UpdateByJobRunID(context.Background(), jobRunID, updates); err != nil {
		log.Warnw("cancel job run DB update failed", "jobRunId", jobRunID, "error", err)
	}
	return &agentv1.CancelJobRunResponse{Success: true, Message: "cancelled"}, nil
}

func (a *AgentServiceImpl) CancelStepRun(ctx context.Context, req *agentv1.CancelStepRunRequest) (*agentv1.CancelStepRunResponse, error) {
	if a.agentService == nil || a.agentService.stepRunRepo == nil {
		return &agentv1.CancelStepRunResponse{Success: false, Message: "step run repository unavailable"}, nil
	}
	stepRunID := strings.TrimSpace(req.StepRunId)
	if stepRunID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "stepRunID is required")
	}
	updates := map[string]any{
		"status":        6,
		"error_message": strings.TrimSpace(req.Reason),
		"end_time":      time.Now(),
	}
	if err := a.agentService.stepRunRepo.PatchByStepRunID(ctx, stepRunID, updates); err != nil {
		log.Errorw("cancel step run failed", "stepRunID", stepRunID, "error", err)
		return nil, status.Errorf(codes.Internal, "cancel step run failed")
	}
	return &agentv1.CancelStepRunResponse{Success: true, Message: "cancelled"}, nil
}

func (a *AgentServiceImpl) UpdateLabels(ctx context.Context, req *agentv1.UpdateLabelsRequest) (*agentv1.UpdateLabelsResponse, error) {
	if a.agentService == nil || a.agentService.agentRepo == nil {
		return &agentv1.UpdateLabelsResponse{Success: false, Message: "agent repository unavailable"}, nil
	}
	agentID := strings.TrimSpace(req.AgentId)
	if agentID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "agentID is required")
	}
	detail, err := a.agentService.agentRepo.GetDetail(ctx, agentID)
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
	if err := a.agentService.agentRepo.Patch(ctx, agentID, map[string]any{"labels": string(encoded)}); err != nil {
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
