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

package agent

import (
	"context"
	"fmt"

	"github.com/arcentrix/arcentra/internal/domain/agent"
)

// ListAgentsUseCase lists agents with pagination.
type ListAgentsUseCase struct {
	agentRepo agent.IAgentRepository
}

func NewListAgentsUseCase(repo agent.IAgentRepository) *ListAgentsUseCase {
	return &ListAgentsUseCase{agentRepo: repo}
}

func (uc *ListAgentsUseCase) Execute(ctx context.Context, input ListAgentsInput) (*ListAgentsOutput, error) {
	agents, total, err := uc.agentRepo.List(ctx, input.Page, input.Size)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	return &ListAgentsOutput{Agents: agents, Total: total}, nil
}

// GetAgentStatisticsUseCase returns aggregate agent counts.
type GetAgentStatisticsUseCase struct {
	agentRepo agent.IAgentRepository
}

func NewGetAgentStatisticsUseCase(repo agent.IAgentRepository) *GetAgentStatisticsUseCase {
	return &GetAgentStatisticsUseCase{agentRepo: repo}
}

func (uc *GetAgentStatisticsUseCase) Execute(ctx context.Context) (*StatisticsOutput, error) {
	total, online, offline, err := uc.agentRepo.Statistics(ctx)
	if err != nil {
		return nil, fmt.Errorf("get agent statistics: %w", err)
	}
	return &StatisticsOutput{Total: total, Online: online, Offline: offline}, nil
}
