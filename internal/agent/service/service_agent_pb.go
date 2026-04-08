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
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/taskqueue"
	"github.com/arcentrix/arcentra/internal/pkg/grpc"
	"github.com/arcentrix/arcentra/pkg/cron"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/metrics"
	"github.com/arcentrix/arcentra/pkg/net"
	"github.com/arcentrix/arcentra/pkg/version"
)

// AgentServiceImpl implements agent.v1.AgentServiceServer
type AgentServiceImpl struct {
	agentv1.UnimplementedAgentServiceServer
	agentConf     *config.AgentConfig
	grpcClient    *grpc.ClientWrapper
	metricsServer *metrics.Server // optional, for heartbeat running count
	storageHolder *agent.StorageHolder
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

// Heartbeat handles heartbeat: when req is nil, starts periodic heartbeat to central server and does initial send;
// otherwise handles incoming heartbeat request from server (returns ack).
func (s *AgentServiceImpl) Heartbeat(_ context.Context, req *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
	if req == nil {
		// Start periodic heartbeat (called from bootstrap after registration)
		if s == nil || s.agentConf == nil || s.grpcClient == nil || s.grpcClient.AgentClient == nil {
			log.Warn("AgentService or grpc client is nil, skipping heartbeat setup")
			return nil, fmt.Errorf("AgentService or grpc client is nil, skipping heartbeat setup")
		}
		interval := s.agentConf.Agent.Interval
		if interval <= 0 {
			interval = 60
		}
		spec := fmt.Sprintf("@every %ds", interval)
		err := cron.AddFunc(spec, func() {
			runningStepRunsCount := getRunningStepRunsCount(s.metricsServer)
			hbReq := &agentv1.HeartbeatRequest{
				AgentId:              s.agentConf.Agent.ID,
				AgentName:            s.agentConf.Agent.Name,
				Status:               agentv1.AgentStatus_AGENT_STATUS_ONLINE,
				RunningStepRunsCount: runningStepRunsCount,
				Timestamp:            time.Now().Unix(),
			}
			ctx, cancel := s.grpcClient.WithTimeoutAndAuth(context.Background())
			defer cancel()
			resp, err := s.grpcClient.AgentClient.Heartbeat(ctx, hbReq)
			if err != nil || !resp.Success {
				log.Warnw("Periodic heartbeat failed", "error", err)
				return
			}
			log.Debugw("Heartbeat Message", "message", resp.Message, "timestamp", resp.Timestamp)
		}, "agent-heartbeat")
		if err != nil {
			log.Errorw("Failed to add heartbeat cron job", "error", err)
			return nil, fmt.Errorf("failed to add heartbeat cron job: %w", err)
		}
		log.Infow("Added periodic heartbeat to crond", "interval", spec)

		runningStepRunsCount := getRunningStepRunsCount(s.metricsServer)
		initReq := &agentv1.HeartbeatRequest{
			AgentId:              s.agentConf.Agent.ID,
			AgentName:            s.agentConf.Agent.Name,
			Status:               agentv1.AgentStatus_AGENT_STATUS_ONLINE,
			RunningStepRunsCount: runningStepRunsCount,
			Timestamp:            time.Now().Unix(),
		}
		ctx, cancel := s.grpcClient.WithTimeoutAndAuth(context.Background())
		defer cancel()
		resp, err := s.grpcClient.AgentClient.Heartbeat(ctx, initReq)
		if err != nil || !resp.Success {
			log.Warnw("Initial heartbeat failed", "error", err)
			return nil, fmt.Errorf("initial heartbeat failed: %w", err)
		}
		log.Infow("Initial heartbeat Message", "message", resp.Message)
		return &agentv1.HeartbeatResponse{Success: true, Timestamp: time.Now().Unix()}, nil
	}
	log.Debugw("Heartbeat received", "agent_id", req.AgentId, "status", req.Status.String())
	return &agentv1.HeartbeatResponse{
		Success:   true,
		Message:   "heartbeat acknowledged",
		Timestamp: time.Now().Unix(),
	}, nil
}

// Register registers this agent with the central server via gRPC.
// Builds request from agentConf, calls server, updates agentConf in-place from response.
func (s *AgentServiceImpl) Register(_ context.Context, _ *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	if s == nil || s.agentConf == nil || s.grpcClient == nil || s.grpcClient.AgentClient == nil {
		return nil, fmt.Errorf("agent grpc client is not ready")
	}
	regReq := &agentv1.RegisterRequest{
		AgentId:               s.agentConf.Agent.ID,
		Token:                 s.agentConf.Grpc.Token,
		Ip:                    net.GetLocalIP(),
		Os:                    runtime.GOOS,
		Arch:                  runtime.GOARCH,
		Version:               version.GetVersion().Version,
		MaxConcurrentStepRuns: int32(s.agentConf.Agent.MaxConcurrentJobs),
		Labels:                s.agentConf.Agent.Labels,
	}
	regCtx, cancel := s.grpcClient.WithTimeoutAndAuth(context.Background())
	defer cancel()
	resp, err := s.grpcClient.AgentClient.Register(regCtx, regReq)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("register rejected: %s", resp.Message)
	}
	if resp.AgentId != "" {
		s.agentConf.Agent.ID = resp.AgentId
	}
	if resp.HeartbeatInterval > 0 {
		s.agentConf.Agent.Interval = int(resp.HeartbeatInterval)
	}
	if len(resp.Labels) > 0 {
		s.agentConf.Agent.Labels = resp.Labels
	}
	if resp.Storage != nil && s.storageHolder != nil {
		s.storageHolder.SetFromProto(resp.Storage)
	}
	return resp, nil
}

// Unregister unregisters this agent from the central server via gRPC.
func (s *AgentServiceImpl) Unregister(_ context.Context, _ *agentv1.UnregisterRequest) (*agentv1.UnregisterResponse, error) {
	if s == nil || s.agentConf == nil || s.grpcClient == nil || s.grpcClient.AgentClient == nil {
		return &agentv1.UnregisterResponse{Success: true}, nil
	}
	if s.agentConf.Agent.ID == "" {
		return &agentv1.UnregisterResponse{Success: true}, nil
	}
	ctx, cancel := s.grpcClient.WithTimeoutAndAuth(context.Background())
	defer cancel()
	resp, err := s.grpcClient.AgentClient.Unregister(ctx, &agentv1.UnregisterRequest{
		AgentId: s.agentConf.Agent.ID,
		Reason:  "agent shutdown",
	})
	if err != nil {
		log.Warnw("agent unregister failed", "agentId", s.agentConf.Agent.ID, "error", err)
		return nil, err
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

// getRunningStepRunsCount returns the current running step runs count from prometheus metrics.
func getRunningStepRunsCount(metricsServer *metrics.Server) int32 {
	if metricsServer == nil {
		return 0
	}
	registry := metricsServer.GetRegistry()
	if registry == nil {
		return 0
	}
	metricFamilies, err := registry.Gather()
	if err != nil {
		log.Debugw("Failed to gather metrics", "error", err)
		return 0
	}
	metricNames := []string{
		"agent_running_step_runs",
		"running_step_runs",
		"agent_step_runs_running",
		"step_runs_running",
		"agent_running_jobs",
		"running_tasks",
		"agent_running_tasks",
		"agent_tasks_running",
		"tasks_running",
	}
	for _, mf := range metricFamilies {
		for _, name := range metricNames {
			if mf.GetName() == name {
				for _, metric := range mf.GetMetric() {
					if metric.Gauge != nil {
						return int32(metric.Gauge.GetValue())
					}
					if metric.Counter != nil {
						return int32(metric.Counter.GetValue())
					}
				}
			}
		}
	}
	return 0
}
