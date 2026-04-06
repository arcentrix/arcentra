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
	"time"

	"github.com/arcentrix/arcentra/internal/domain/agent"
)

// UpdateAgentUseCase handles partial updates to an agent.
type UpdateAgentUseCase struct {
	agentRepo agent.IAgentRepository
}

func NewUpdateAgentUseCase(repo agent.IAgentRepository) *UpdateAgentUseCase {
	return &UpdateAgentUseCase{agentRepo: repo}
}

func (uc *UpdateAgentUseCase) Execute(ctx context.Context, agentID string, input UpdateAgentInput) error {
	if _, err := uc.agentRepo.Get(ctx, agentID); err != nil {
		return fmt.Errorf("agent %s not found: %w", agentID, err)
	}

	updates := make(map[string]any)
	if input.AgentName != nil {
		updates["agent_name"] = *input.AgentName
	}
	if input.Labels != nil {
		updates["labels"] = input.Labels
	}

	if len(updates) == 0 {
		return nil
	}

	updates["updated_at"] = time.Now()
	if err := uc.agentRepo.Patch(ctx, agentID, updates); err != nil {
		return fmt.Errorf("update agent %s: %w", agentID, err)
	}
	return nil
}

// DeleteAgentUseCase handles deletion of an agent.
type DeleteAgentUseCase struct {
	agentRepo agent.IAgentRepository
}

func NewDeleteAgentUseCase(repo agent.IAgentRepository) *DeleteAgentUseCase {
	return &DeleteAgentUseCase{agentRepo: repo}
}

func (uc *DeleteAgentUseCase) Execute(ctx context.Context, agentID string) error {
	if err := uc.agentRepo.Delete(ctx, agentID); err != nil {
		return fmt.Errorf("delete agent %s: %w", agentID, err)
	}
	return nil
}
