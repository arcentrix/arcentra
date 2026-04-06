package grpc

import (
	"context"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/case/agent"
)

type AgentServiceImpl struct {
	agentv1.UnimplementedAgentServiceServer
	registerAgent *agent.RegisterAgentUseCase
	getAgent      *agent.GetAgentUseCase
	listAgents    *agent.ListAgentsUseCase
	updateAgent   *agent.UpdateAgentUseCase
	deleteAgent   *agent.DeleteAgentUseCase
}

func NewAgentServiceImpl(
	registerAgent *agent.RegisterAgentUseCase,
	getAgent *agent.GetAgentUseCase,
	listAgents *agent.ListAgentsUseCase,
	updateAgent *agent.UpdateAgentUseCase,
	deleteAgent *agent.DeleteAgentUseCase,
) *AgentServiceImpl {
	return &AgentServiceImpl{
		registerAgent: registerAgent,
		getAgent:      getAgent,
		listAgents:    listAgents,
		updateAgent:   updateAgent,
		deleteAgent:   deleteAgent,
	}
}

func (s *AgentServiceImpl) Heartbeat(ctx context.Context, req *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
	return &agentv1.HeartbeatResponse{Success: true}, nil
}
