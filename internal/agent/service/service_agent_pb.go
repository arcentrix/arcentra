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
	"maps"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	grpcclient "github.com/arcentrix/arcentra/internal/pkg/grpc"
	"github.com/arcentrix/arcentra/pkg/log"
)

// AgentService implements agent.v1.AgentServiceServer
type AgentService struct {
	agentv1.UnimplementedAgentServiceServer
	agentConf  *config.AgentConfig
	grpcClient *grpcclient.ClientWrapper
}

// NewAgentService creates a new AgentService instance
func NewAgentService(agentConf *config.AgentConfig, grpcClient *grpcclient.ClientWrapper) *AgentService {
	return &AgentService{
		agentConf:  agentConf,
		grpcClient: grpcClient,
	}
}

// Heartbeat handles heartbeat requests from server
func (s *AgentService) Heartbeat(ctx context.Context, req *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
	log.Debugw("Heartbeat received", "agent_id", req.AgentId, "status", req.Status.String())

	return &agentv1.HeartbeatResponse{
		Success:   true,
		Message:   "heartbeat acknowledged",
		Timestamp: time.Now().Unix(),
	}, nil
}

// Register handles agent registration requests
func (s *AgentService) Register(ctx context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	log.Infow("Register request received", "agent_id", req.AgentId, "ip", req.Ip, "version", req.Version)

	// TODO: Implement registration logic
	// For now, return success with default heartbeat interval
	return &agentv1.RegisterResponse{
		Success:           true,
		Message:           "registration successful",
		AgentId:           req.AgentId,
		HeartbeatInterval: int64(s.agentConf.Agent.Interval),
	}, nil
}

// Unregister handles agent unregistration requests
func (s *AgentService) Unregister(ctx context.Context, req *agentv1.UnregisterRequest) (*agentv1.UnregisterResponse, error) {
	log.Infow("Unregister request received", "agent_id", req.AgentId, "reason", req.Reason)

	// TODO: Implement unregistration logic
	return &agentv1.UnregisterResponse{
		Success: true,
		Message: "unregistration successful",
	}, nil
}

// FetchStepRun handles step run fetching requests
func (s *AgentService) FetchStepRun(ctx context.Context, req *agentv1.FetchStepRunRequest) (*agentv1.FetchStepRunResponse, error) {
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
func (s *AgentService) ReportStepRunStatus(ctx context.Context, req *agentv1.ReportStepRunStatusRequest) (*agentv1.ReportStepRunStatusResponse, error) {
	log.Debugw("ReportStepRunStatus request received", "agent_id", req.AgentId, "step_run_id", req.StepRunId, "status", req.Status.String())

	// TODO: Implement step run status reporting logic
	return &agentv1.ReportStepRunStatusResponse{
		Success: true,
		Message: "step run status reported successfully",
	}, nil
}

// CancelStepRun handles step run cancellation requests from server
func (s *AgentService) CancelStepRun(ctx context.Context, req *agentv1.CancelStepRunRequest) (*agentv1.CancelStepRunResponse, error) {
	log.Infow("CancelStepRun request received", "agent_id", req.AgentId, "step_run_id", req.StepRunId, "reason", req.Reason)

	// TODO: Implement step run cancellation logic
	// This should cancel the running step run identified by step_run_id
	return &agentv1.CancelStepRunResponse{
		Success: true,
		Message: "step run cancellation request received",
	}, nil
}

// UpdateLabels handles agent labels update requests
func (s *AgentService) UpdateLabels(ctx context.Context, req *agentv1.UpdateLabelsRequest) (*agentv1.UpdateLabelsResponse, error) {
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
