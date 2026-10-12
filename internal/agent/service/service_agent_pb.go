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
	"maps"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/identitystore"
	"github.com/arcentrix/arcentra/internal/agent/taskqueue"
	"github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/cron"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/metrics"
	"github.com/arcentrix/arcentra/pkg/net"
	"github.com/arcentrix/arcentra/pkg/version"
	"google.golang.org/grpc/status"
)

// AgentServiceImpl implements agent.v1.AgentServiceServer
type AgentServiceImpl struct {
	agentv1.UnimplementedAgentServiceServer
	agentConf             *config.AgentConfig
	grpcClient            *grpc.ClientWrapper
	metricsServer         *metrics.Server // optional, for heartbeat running count
	storageHolder         *agent.StorageHolder
	lifecycleMu           sync.Mutex
	identityStore         *identitystore.Store
	identityPrepared      bool
	registrationAttempted bool
	registered            bool
	heartbeatStarted      bool
	closed                bool
	authenticated         atomic.Bool
}

// NewAgentServiceImpl creates a new AgentService instance. metricsServer may be nil (used for heartbeat running count).
func NewAgentServiceImpl(agentConf *config.AgentConfig, grpcClient *grpc.ClientWrapper, metricsServer *metrics.Server) *AgentServiceImpl {
	return &AgentServiceImpl{
		agentConf:     agentConf,
		grpcClient:    grpcClient,
		metricsServer: metricsServer,
		storageHolder: agent.NewStorageHolder(),
	}
}

// StorageHolder returns the holder that provides the IStorage instance
// initialized from the control-plane's RegisterResponse.
func (s *AgentServiceImpl) StorageHolder() *agent.StorageHolder {
	return s.storageHolder
}

// PrepareIdentity 在任何注册和终态日志检查之前恢复持久身份，并持有存储锁至关闭
func (s *AgentServiceImpl) PrepareIdentity(ctx context.Context) error {
	if s == nil || s.agentConf == nil {
		return fmt.Errorf("agent configuration is not ready")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.prepareIdentityLocked(ctx)
}

func (s *AgentServiceImpl) prepareIdentityLocked(ctx context.Context) error {
	if s.closed {
		return fmt.Errorf("agent identity lifecycle is closed")
	}
	if s.identityPrepared {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	conf := s.agentConf
	hasID := strings.TrimSpace(conf.Agent.ID) != ""
	hasToken := strings.TrimSpace(conf.Grpc.Token) != ""
	if hasID != hasToken || (conf.Agent.ID != "" && !hasID) || (conf.Grpc.Token != "" && !hasToken) {
		return fmt.Errorf("configure the original agent.id and grpc.token together; partial explicit identity is not allowed")
	}
	if hasID {
		if err := identitystore.ValidateIdentity(conf.Agent.ID, conf.Grpc.Token); err != nil {
			return err
		}
	}
	if conf.Agent.IdentityPersistence.Enabled {
		if s.grpcClient == nil {
			return fmt.Errorf("verified controller transport is required for identity persistence")
		}
		binding, err := s.grpcClient.ControllerIdentityBinding()
		if err != nil {
			return err
		}
		workspace := conf.Agent.WorkspaceDir
		if workspace == "" {
			workspace = "."
		}
		store := s.identityStore
		if store == nil {
			store, err = identitystore.Open(ctx, identitystore.Config{
				Directory: conf.Agent.IdentityPersistence.Directory, Workspace: workspace, ControllerBinding: binding,
			})
			if err != nil {
				return err
			}
			s.identityStore = store
		}
		if saved, ok := store.Identity(); ok {
			if hasID && conf.Agent.ID != saved.AgentID {
				_ = store.Close()
				s.identityStore = nil
				return fmt.Errorf("explicit agent identity does not match the persisted identity")
			}
			if !hasID {
				conf.Agent.ID, conf.Grpc.Token = saved.AgentID, saved.PerAgentToken
				s.grpcClient.UpdateToken(saved.PerAgentToken)
				hasID, hasToken = true, true
			}
		}
	}
	if !hasID && !hasToken {
		if strings.TrimSpace(conf.Grpc.RegistrationToken) == "" {
			return fmt.Errorf("configure grpc.registrationToken or the original agent.id and grpc.token")
		}
		if err := taskqueue.CheckTerminalOutboxForEnrollment(ctx, conf.Agent.WorkspaceDir); err != nil {
			return err
		}
	}
	s.identityPrepared = true
	return nil
}

// CloseIdentity 串行结束身份生命周期，阻止迟到的注册响应在存储解锁后写入
func (s *AgentServiceImpl) CloseIdentity() error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.authenticated.Store(false)
	if s.heartbeatStarted {
		_ = cron.Remove("agent-heartbeat")
	}
	if s.identityStore != nil {
		return s.identityStore.Close()
	}
	return nil
}

// Authenticated 返回当前心跳认证状态，供就绪检查使用
func (s *AgentServiceImpl) Authenticated() bool {
	return s != nil && s.authenticated.Load()
}

// Heartbeat 先完成初始认证心跳，再启动周期心跳；认证失败不得触发重新注册
func (s *AgentServiceImpl) Heartbeat(ctx context.Context, req *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
	if req != nil {
		return &agentv1.HeartbeatResponse{Success: true, Message: "heartbeat acknowledged", Timestamp: time.Now().Unix()}, nil
	}
	if s == nil || s.agentConf == nil || s.grpcClient == nil || s.grpcClient.AgentClient == nil {
		return nil, fmt.Errorf("agent grpc client is not ready")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed || !s.registered {
		return nil, fmt.Errorf("agent must register before heartbeat startup")
	}
	if s.heartbeatStarted {
		return nil, fmt.Errorf("agent heartbeat is already started")
	}
	if err := s.sendHeartbeatLocked(ctx); err != nil {
		return nil, err
	}
	interval := s.agentConf.Agent.Interval
	if interval <= 0 {
		interval = 60
	}
	spec := fmt.Sprintf("@every %ds", interval)
	if err := cron.AddFunc(spec, func() {
		s.lifecycleMu.Lock()
		defer s.lifecycleMu.Unlock()
		if s.closed || !s.registered {
			return
		}
		if err := s.sendHeartbeatLocked(context.Background()); err != nil {
			log.Warnw("Periodic heartbeat failed", "error", err)
		}
	}, "agent-heartbeat"); err != nil {
		s.authenticated.Store(false)
		return nil, fmt.Errorf("failed to add heartbeat cron job")
	}
	s.heartbeatStarted = true
	return &agentv1.HeartbeatResponse{Success: true, Timestamp: time.Now().Unix()}, nil
}

func (s *AgentServiceImpl) sendHeartbeatLocked(ctx context.Context) error {
	hbCtx, cancel := s.grpcClient.WithTimeoutAndAuth(ctx)
	defer cancel()
	resp, err := s.grpcClient.AgentClient.Heartbeat(hbCtx, &agentv1.HeartbeatRequest{
		AgentId: s.agentConf.Agent.ID, AgentName: s.agentConf.Agent.Name,
		Status:               agentv1.AgentStatus_AGENT_STATUS_ONLINE,
		RunningStepRunsCount: getRunningStepRunsCount(s.metricsServer), Timestamp: time.Now().Unix(),
	})
	if err != nil {
		s.authenticated.Store(false)
		return status.Error(status.Code(err), "agent heartbeat RPC failed")
	}
	if resp == nil || !resp.Success {
		s.authenticated.Store(false)
		return fmt.Errorf("agent heartbeat was rejected")
	}
	s.authenticated.Store(true)
	return nil
}

// Register 注册当前进程，动态身份必须先持久化再写入运行时配置
func (s *AgentServiceImpl) Register(ctx context.Context, _ *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	if s == nil || s.agentConf == nil || s.grpcClient == nil || s.grpcClient.AgentClient == nil {
		return nil, fmt.Errorf("agent grpc client is not ready")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	capacity, err := s.agentConf.Agent.ExecutionCapacity()
	if err != nil {
		return nil, err
	}
	if err := s.prepareIdentityLocked(ctx); err != nil {
		return nil, err
	}
	if s.registrationAttempted {
		return nil, fmt.Errorf("agent registration already attempted; operator reconciliation or restart is required")
	}
	registrationToken := s.agentConf.Grpc.RegistrationToken
	dynamic := s.agentConf.Agent.ID == ""
	if !dynamic {
		registrationToken = ""
	} else if err := taskqueue.CheckTerminalOutboxForEnrollment(ctx, s.agentConf.Agent.WorkspaceDir); err != nil {
		// Preparing identity may precede a long connection wait; check again at the RPC boundary.
		return nil, err
	}
	regReq := &agentv1.RegisterRequest{
		AgentId: s.agentConf.Agent.ID, Token: s.agentConf.Grpc.Token, RegistrationToken: registrationToken,
		AgentName: s.agentConf.Agent.Name, Ip: net.GetLocalIP(), Os: runtime.GOOS, Arch: runtime.GOARCH,
		Version: version.GetVersion().Version, MaxConcurrentStepRuns: capacity, Labels: maps.Clone(s.agentConf.Agent.Labels),
	}
	if dynamic && s.identityStore != nil {
		if err := s.identityStore.BeginEnrollment(ctx); err != nil {
			return nil, err
		}
	}
	s.registrationAttempted = true
	regCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := s.grpcClient.AgentClient.Register(regCtx, regReq)
	if err != nil {
		return nil, status.Error(status.Code(err), "agent registration RPC failed")
	}
	if resp == nil || !resp.Success {
		return nil, fmt.Errorf("agent registration was rejected or returned no response")
	}
	if dynamic {
		if err := identitystore.ValidateIdentity(resp.AgentId, resp.AuthToken); err != nil {
			return nil, fmt.Errorf("agent registration returned an invalid identity")
		}
		if s.identityStore != nil {
			if err := s.identityStore.CommitIdentity(ctx, resp.AgentId, resp.AuthToken); err != nil {
				return nil, err
			}
		}
	} else if resp.AgentId != s.agentConf.Agent.ID || (resp.AuthToken != "" && resp.AuthToken != s.agentConf.Grpc.Token) {
		return nil, fmt.Errorf("agent registration response does not match the selected identity")
	}
	if dynamic {
		s.agentConf.Agent.ID = resp.AgentId
		s.agentConf.Grpc.Token = resp.AuthToken
	}
	s.grpcClient.UpdateToken(s.agentConf.Grpc.Token)
	if resp.HeartbeatInterval > 0 {
		s.agentConf.Agent.Interval = int(resp.HeartbeatInterval)
	}
	if len(resp.Labels) > 0 {
		s.agentConf.Agent.Labels = maps.Clone(resp.Labels)
	}
	if !resp.PendingApproval && resp.Storage != nil && s.storageHolder != nil {
		s.storageHolder.SetFromProto(resp.Storage)
	}
	s.registered = !resp.PendingApproval
	return resp, nil
}

// Unregister unregisters this agent from the central server via gRPC.
func (s *AgentServiceImpl) Unregister(parentCtx context.Context, _ *agentv1.UnregisterRequest) (*agentv1.UnregisterResponse, error) {
	if s == nil || s.agentConf == nil || s.grpcClient == nil || s.grpcClient.AgentClient == nil {
		return &agentv1.UnregisterResponse{Success: true}, nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.authenticated.Store(false)
	if s.closed || !s.registered {
		return &agentv1.UnregisterResponse{Success: true}, nil
	}
	s.registered = false
	if s.agentConf.Agent.ID == "" {
		return &agentv1.UnregisterResponse{Success: true}, nil
	}
	ctx, cancel := s.grpcClient.WithTimeoutAndAuth(parentCtx)
	defer cancel()
	resp, err := s.grpcClient.AgentClient.Unregister(ctx, &agentv1.UnregisterRequest{
		AgentId: s.agentConf.Agent.ID,
		Reason:  "agent shutdown",
	})
	if err != nil {
		return nil, status.Error(status.Code(err), "agent unregistration RPC failed")
	}
	return resp, nil
}

// FetchStepRun handles step run fetching requests
func (s *AgentServiceImpl) FetchStepRun(_ context.Context, req *agentv1.FetchStepRunRequest) (*agentv1.FetchStepRunResponse, error) {
	log.Debugw("FetchStepRun request received", "agent_id", req.AgentId, "max_step_runs", req.MaxStepRuns)

	// TODO: Implement step run fetching logic
	// For now, return empty step run list
	return &agentv1.FetchStepRunResponse{
		Success:  true,
		Message:  "no step runs available",
		StepRuns: []*agentv1.StepRun{},
	}, nil
}

// ReportStepRunStatus handles step run status reporting requests
func (s *AgentServiceImpl) ReportStepRunStatus(
	_ context.Context,
	req *agentv1.ReportStepRunStatusRequest,
) (*agentv1.ReportStepRunStatusResponse, error) {
	log.Debugw("ReportStepRunStatus request received", "agent_id", req.AgentId, "step_run_id", req.StepRunId, "status", req.Status.String())

	// TODO: Implement step run status reporting logic
	return &agentv1.ReportStepRunStatusResponse{
		Success: true,
		Message: "step run status reported successfully",
	}, nil
}

// CancelStepRun handles step run cancellation requests from server
func (s *AgentServiceImpl) CancelStepRun(_ context.Context, req *agentv1.CancelStepRunRequest) (*agentv1.CancelStepRunResponse, error) {
	log.Infow("CancelStepRun request received", "agent_id", req.AgentId, "step_run_id", req.StepRunId, "reason", req.Reason)

	if taskqueue.CancelStepRun(req.StepRunId) {
		log.Infow("step run cancel signal sent", "step_run_id", req.StepRunId)
	}

	return &agentv1.CancelStepRunResponse{
		Success: true,
		Message: "step run cancellation request received",
	}, nil
}

// CancelJobRun handles job run cancellation requests from the control plane.
func (s *AgentServiceImpl) CancelJobRun(_ context.Context, req *agentv1.CancelJobRunRequest) (*agentv1.CancelJobRunResponse, error) {
	log.Infow("CancelJobRun request received", "job_run_id", req.JobRunId, "reason", req.Reason)

	if taskqueue.CancelJobRun(req.JobRunId) {
		log.Infow("job run cancel signal sent", "job_run_id", req.JobRunId)
	}

	return &agentv1.CancelJobRunResponse{
		Success: true,
		Message: "job run cancellation request received",
	}, nil
}

// ReportJobRunStatus is a no-op on the Agent side; the Agent uses the
// control plane client to report status, not this server handler.
func (s *AgentServiceImpl) ReportJobRunStatus(
	_ context.Context,
	req *agentv1.ReportJobRunStatusRequest,
) (*agentv1.ReportJobRunStatusResponse, error) {
	log.Debugw("ReportJobRunStatus received (agent-side stub)", "job_run_id", req.JobRunId)
	return &agentv1.ReportJobRunStatusResponse{Success: true, Message: "ok"}, nil
}

// UpdateLabels handles agent labels update requests
func (s *AgentServiceImpl) UpdateLabels(_ context.Context, req *agentv1.UpdateLabelsRequest) (*agentv1.UpdateLabelsResponse, error) {
	log.Infow("UpdateLabels request received", "agent_id", req.AgentId, "merge", req.Merge, "labels", req.Labels)

	// TODO: Implement labels update logic
	// Update agent labels based on merge flag
	updatedLabels := make(map[string]string)
	if req.Merge {
		// Merge with existing labels
		maps.Copy(updatedLabels, s.agentConf.Agent.Labels)
	}
	maps.Copy(updatedLabels, req.Labels)

	return &agentv1.UpdateLabelsResponse{
		Success: true,
		Message: "labels updated successfully",
		Labels:  updatedLabels,
	}, nil
}

// getRunningStepRunsCount 获取真实运行步骤数量，避免依赖可选的指标服务
func getRunningStepRunsCount(_ *metrics.Server) int32 { return taskqueue.RunningStepRunsCount() }
