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
	"crypto/hmac"
	"errors"
	"fmt"
	"strings"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
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
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "heartbeat request is required")
	}
	agentID, err := authenticatedAgent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	if a.agentService == nil || a.agentService.agentRepo == nil {
		return nil, status.Error(codes.Unavailable, "agent repository unavailable")
	}
	agent, err := a.agentService.agentRepo.Get(ctx, agentID)
	if err != nil {
		return nil, status.Error(codes.Internal, "load agent failed")
	}
	if agent == nil || agent.AgentID != agentID || agent.IsEnabled != 1 {
		return nil, status.Error(codes.PermissionDenied, "enabled agent required")
	}
	now := time.Now()
	updates := map[string]any{"status": int(req.Status), "metrics": fmt.Sprintf(`{"runningStepRunsCount":%d}`, req.RunningStepRunsCount), "updated_at": now, "last_heartbeat": now, "runtime_capabilities": string(agentRuntimeCapabilities(req.RuntimeCapabilities, agent.OS))}
	if err := a.agentService.agentRepo.Patch(ctx, agentID, updates); err != nil {
		return nil, status.Error(codes.Internal, "persist agent heartbeat failed")
	}
	return &agentv1.HeartbeatResponse{Success: true, Message: "pong", Timestamp: now.Unix()}, nil
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
		log.Warnw("registration token validation failed")
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
		AgentID:             agentID,
		AgentName:           agentName,
		Address:             req.Ip,
		OS:                  req.Os,
		Arch:                req.Arch,
		Version:             req.Version,
		Status:              1, // online
		Labels:              []byte(labelsJSON),
		Metrics:             "/metrics",
		RuntimeCapabilities: agentRuntimeCapabilities(req.RuntimeCapabilities, req.Os),
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

	if !pendingApproval && a.storageRepo != nil {
		if sc := a.buildStorageConfig(ctx); sc != nil {
			resp.Storage = sc
		}
	}

	// 共享注册令牌可能在创建记录、读取配置期间被撤销，拒绝交付凭证
	if err := a.agentService.ValidateRegistrationToken(ctx, req.RegistrationToken); err != nil {
		// 保留待审批记录供管理员核对，不自动删除或重建身份
		if err := a.agentService.agentRepo.Patch(ctx, agentID, map[string]any{"is_enabled": 0}); err != nil {
			log.Errorw("failed to hold incomplete registration for approval", "agentID", agentID)
		}
		return nil, status.Error(codes.Unauthenticated, "registration authorization is no longer valid")
	}

	// 配置读取期间可能发生停用、删除或密钥轮换；响应凭证前再次检查
	if err := a.checkRegistrationAuthorization(ctx, agentID, authToken, pendingApproval); err != nil {
		return nil, err
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
	if !hmac.Equal([]byte(req.Token), []byte(expectedToken)) {
		return nil, status.Errorf(codes.Unauthenticated, "invalid token")
	}

	// Check agent exists
	registered, err := agentRepo.Get(ctx, agentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Error(codes.NotFound, "agent not found")
		}
		return nil, status.Errorf(codes.Internal, "failed to get agent")
	}

	if registered == nil || registered.AgentID != agentID || registered.IsEnabled != 1 {
		return nil, status.Error(codes.PermissionDenied, "agent is disabled or pending approval")
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
	reportedOS := registered.OS
	if req.Os != "" {
		reportedOS = req.Os
	}
	updates["runtime_capabilities"] = string(agentRuntimeCapabilities(req.RuntimeCapabilities, reportedOS))
	updates["status"] = 1
	updates["last_heartbeat"] = time.Now()

	if len(updates) > 0 {
		if err = agentRepo.Patch(ctx, agentID, updates); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to update agent")
		}
	}

	detail, err := agentRepo.Get(ctx, agentID)
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

	if err := a.checkRegistrationAuthorization(ctx, agentID, req.Token, false); err != nil {
		return nil, err
	}

	return resp, nil
}

// checkRegistrationAuthorization checks current credentials and authorization immediately
// before a registration response is returned. Only newly pending enrollment may remain disabled.
func (a *AgentServiceImpl) checkRegistrationAuthorization(ctx context.Context, agentID, token string, allowPending bool) error {
	expectedToken, err := a.agentService.GenerateAgentToken(ctx, agentID)
	if err != nil {
		return status.Error(codes.Internal, "failed to verify current credentials")
	}
	if !hmac.Equal([]byte(token), []byte(expectedToken)) {
		return status.Error(codes.Unauthenticated, "agent credentials changed during registration")
	}
	current, err := a.agentService.agentRepo.Get(ctx, agentID)
	if err != nil {
		return status.Error(codes.PermissionDenied, "agent authorization is unavailable")
	}
	if current == nil || current.AgentID != agentID || (current.IsEnabled != 1 && !(allowPending && current.IsEnabled == 0)) {
		return status.Error(codes.PermissionDenied, "agent is disabled or pending approval")
	}
	return nil
}

// buildStorageConfig reads the default storage configuration from DB and
// converts it to the proto StorageConfig delivered to agents.
func (a *AgentServiceImpl) buildStorageConfig(ctx context.Context) *agentv1.StorageConfig {
	if a.storageRepo == nil {
		return nil
	}
	sc, err := a.storageRepo.GetDefault(ctx)
	if err != nil || sc == nil {
		return nil
	}
	var detail model.StorageConfigDetail
	if err := sonic.Unmarshal(sc.Config, &detail); err != nil {
		log.Warn("failed to parse default storage config for agent")
		return nil
	}
	return &agentv1.StorageConfig{
		StorageId:          sc.StorageID,
		StorageFingerprint: artifactutil.StorageFingerprint(sc.StorageType, detail.Endpoint, detail.Bucket, detail.Region, detail.BasePath, detail.UseTLS),
		Provider:           sc.StorageType,
		Endpoint:           detail.Endpoint,
		Bucket:             detail.Bucket,
		Region:             detail.Region,
		AccessKey:          detail.AccessKey,
		SecretKey:          detail.SecretKey,
		BasePath:           detail.BasePath,
		UseSsl:             detail.UseTLS,
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
		ExcludeRuntime: true,
		AgentID:        agentID,
		Page:           1,
		PageSize:       maxRuns,
		SortBy:         "created_at",
		SortDesc:       false,
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
	if stepRunID == "" || stepRunID != req.StepRunId {
		return nil, status.Errorf(codes.InvalidArgument, "exact stepRunID is required")
	}
	agentID, err := authenticatedAgent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	entity, err := a.agentService.stepRunRepo.GetByStepRunID(ctx, stepRunID)
	if err != nil {
		return nil, status.Error(codes.Internal, "load step run failed")
	}
	if entity == nil {
		return nil, status.Error(codes.NotFound, "step run not found")
	}
	if entity.AgentID != agentID {
		return nil, status.Error(codes.PermissionDenied, "step run belongs to another agent")
	}
	claimID, alreadyClaimed, err := reportClaim(ctx, entity.Status, int(req.Status), entity.ClaimID)
	if err != nil {
		return nil, err
	}
	ctx = agentidentity.WithGenerationReport(ctx, req.GenerationId, req.GenerationBindingDigest)
	ctx = agentidentity.WithStepAttemptReport(ctx, req.StepAttemptAdmissionDigest)
	if err := a.validateRuntimeStepReport(ctx, entity, claimID, int(req.Status)); err != nil {
		return nil, err
	}
	if alreadyClaimed {
		if entity.RetryMode != "" || entity.Attempt > 1 {
			guard, ok := a.agentService.stepRunRepo.(interface {
				ValidateRetryActivation(context.Context, *model.StepRun) error
			})
			if !ok {
				return nil, status.Error(codes.Unimplemented, "retry activation repository unavailable")
			}
			if err := guard.ValidateRetryActivation(ctx, entity); err != nil {
				return nil, status.Error(codes.FailedPrecondition, "retry activation is no longer authorized")
			}
		}
		return &agentv1.ReportStepRunStatusResponse{Success: true, Message: "claim confirmed"}, nil
	}
	if entity.Status == model.JobRunStatusCancelled && entity.CancellationClaimID != "" && int(req.Status) == 3 {
		want := entity.ClaimID
		if want == "" {
			want = agentidentity.StepClaimID(entity.CancellationClaimID, entity.StepRunID)
		}
		if claimID != want {
			return nil, status.Error(codes.PermissionDenied, "original cancelled step claim required")
		}
		return nil, agentidentity.CancelledStepClaimError(entity.StepRunID, claimID)
	}
	if err := validateReportedStatus(0, int(req.Status), true); err != nil {
		return nil, err
	}
	if int(req.Status) == 3 {
		if err := validateReportedStatus(entity.Status, int(req.Status), true); err != nil {
			return nil, err
		}
	}
	digest := ""
	if int(req.Status) >= 4 {
		digest, err = terminalReportDigest(req, claimID, entity.PipelineID, entity.PipelineRunID, entity.JobRunID)
		if err != nil {
			return nil, err
		}
	}
	if entity.Status == model.JobRunStatusCancelled && entity.CancellationClaimID != "" && int(req.Status) == model.JobRunStatusCancelled && (entity.TerminalReportDigest == "" || entity.TerminalReportDigest == digest) {
		if err := confirmCancelledStepReceipt(ctx, a.agentService.stepRunRepo, entity, claimID, digest); err != nil {
			return nil, err
		}
		return &agentv1.ReportStepRunStatusResponse{Success: true, Message: "cancellation receipt confirmed"}, nil
	}
	if entity.Status >= 4 {
		if entity.Status != int(req.Status) || entity.TerminalReportDigest == "" || entity.TerminalReportDigest != digest {
			return nil, terminalStepConflict(ctx, a.agentService.stepRunRepo, entity, req, claimID)
		}
		return &agentv1.ReportStepRunStatusResponse{Success: true, Message: "terminal report confirmed"}, nil
	}
	updates := executionStatusUpdates(int(req.Status), req.ErrorMessage, req.StartTime, req.EndTime, entity.StartTime)
	updates["claim_id"] = claimID
	if digest != "" {
		updates["terminal_report_digest"] = digest
	}
	updates["exit_code"] = req.ExitCode
	updates["generation_id"] = req.GenerationId
	updates["generation_binding_digest"] = req.GenerationBindingDigest
	updates["retryable_shell_exit"] = req.RetryableShellExit
	if workspace := strings.TrimSpace(req.Metrics["workspace"]); workspace != "" {
		updates["workspace"] = workspace
	}
	if err := patchAgentStepStatus(ctx, a.agentService.stepRunRepo, entity, updates); err != nil {
		return nil, status.Error(codes.Internal, "update step run failed")
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
	if jobRunID == "" || jobRunID != req.JobRunId {
		return nil, status.Errorf(codes.InvalidArgument, "exact jobRunID is required")
	}
	agentID, err := authenticatedAgent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	entity, err := a.agentService.jobRunRepo.GetByJobRunID(ctx, jobRunID)
	if err != nil {
		return nil, status.Error(codes.Internal, "load job run failed")
	}
	if entity == nil {
		return nil, status.Error(codes.NotFound, "job run not found")
	}
	if entity.JobRunID != jobRunID {
		return nil, status.Error(codes.PermissionDenied, "exact job identity is required")
	}
	if entity.AgentID != agentID {
		return nil, status.Error(codes.PermissionDenied, "job run belongs to another agent")
	}
	if err := validateJobRuntimeReport(entity, req.RuntimeBindingDigest); err != nil {
		return nil, err
	}
	claimID, alreadyClaimed, err := reportClaim(ctx, entity.Status, int(req.Status), entity.ClaimID)
	if err != nil {
		return nil, err
	}
	ctx = agentidentity.WithGenerationReport(ctx, req.GenerationId, req.GenerationBindingDigest)
	sourceUpdates, err := prepareJobSource(entity, req, claimID)
	if err != nil {
		return nil, err
	}
	if alreadyClaimed {
		if req.ArtifactStorageId != entity.ArtifactStorageID || req.ArtifactStorageFingerprint != entity.ArtifactStorageFingerprint {
			return nil, status.Error(codes.FailedPrecondition, "artifact storage identity changed after execution claim")
		}
		return &agentv1.ReportJobRunStatusResponse{Success: true, Message: "claim confirmed"}, nil
	}
	if err := validateReportedStatus(0, int(req.Status), false); err != nil {
		return nil, err
	}
	if int(req.Status) == 3 {
		if err := validateReportedStatus(entity.Status, int(req.Status), false); err != nil {
			return nil, err
		}
	}
	digest := ""
	if int(req.Status) >= 4 {
		digest, err = terminalReportDigest(req, claimID, entity.PipelineID, entity.PipelineRunID, entity.JobRunID)
		if err != nil {
			return nil, err
		}
	}
	if entity.Status == model.JobRunStatusCancelled && entity.CancellationClaimID != "" && int(req.Status) == model.JobRunStatusCancelled && (entity.TerminalReportDigest == "" || entity.TerminalReportDigest == digest) {
		if err := confirmCancelledJobReceipt(ctx, a.agentService.jobRunRepo, entity, claimID, digest, sourceUpdates); err != nil {
			return nil, err
		}
		return &agentv1.ReportJobRunStatusResponse{Success: true, Message: "cancellation receipt confirmed"}, nil
	}
	if model.IsJobRunTerminal(entity.Status) {
		if entity.Status != int(req.Status) || entity.TerminalReportDigest == "" || entity.TerminalReportDigest != digest {
			return nil, terminalJobConflict(ctx, a.agentService.jobRunRepo, entity, req, claimID)
		}
		if err := validateSourceReceiptReplay(entity, sourceUpdates); err != nil {
			return nil, err
		}
		if req.SourceCheckout != nil {
			if err := verifySourceReplay(ctx, a.agentService.jobRunRepo, entity, claimID); err != nil {
				return nil, err
			}
		}
		// 回执与制品在同一事务中提交，重放无需重新写入或复活已清理的制品
		return &agentv1.ReportJobRunStatusResponse{Success: true, Message: "terminal report confirmed"}, nil
	}
	updates := executionStatusUpdates(int(req.Status), req.ErrorMessage, req.StartTime, req.EndTime, entity.StartTime)
	for key, value := range sourceUpdates {
		updates[key] = value
	}
	updates["claim_id"] = claimID
	if digest != "" {
		updates["terminal_report_digest"] = digest
	}
	artifacts, err := a.prepareJobArtifacts(ctx, entity, req, updates)
	if err != nil {
		return nil, err
	}
	if err := patchAgentJobStatus(ctx, a.agentService.jobRunRepo, entity, updates, artifacts); err != nil {
		return nil, status.Error(codes.Internal, "update job run failed")
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
