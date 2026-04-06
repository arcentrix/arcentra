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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"github.com/arcentrix/arcentra/internal/domain/agent"
)

// IAgentSecretProvider retrieves the secret key and salt used for agent token generation.
// Implemented by infrastructure (e.g. reading from GeneralSettings).
type IAgentSecretProvider interface {
	GetAgentSecret(ctx context.Context) (secretKey, salt string, err error)
}

// RegisterAgentUseCase handles the registration of a new agent.
type RegisterAgentUseCase struct {
	agentRepo      agent.IAgentRepository
	secretProvider IAgentSecretProvider
	idGenerator    func() string
}

func NewRegisterAgentUseCase(
	repo agent.IAgentRepository,
	secretProvider IAgentSecretProvider,
	idGen func() string,
) *RegisterAgentUseCase {
	return &RegisterAgentUseCase{
		agentRepo:      repo,
		secretProvider: secretProvider,
		idGenerator:    idGen,
	}
}

func (uc *RegisterAgentUseCase) Execute(ctx context.Context, input RegisterAgentInput) (*RegisterAgentOutput, error) {
	agentID := uc.idGenerator()
	agentInfo := &agent.Agent{
		AgentID:   agentID,
		AgentName: input.AgentName,
		Address:   "0.0.0.0",
		Port:      "8080",
		OS:        "Linux",
		Arch:      "amd64",
		Version:   "0.0.0",
		Status:    agent.AgentStatusUnknown,
		Labels:    input.Labels,
		IsEnabled: true,
		Metrics:   "/metrics",
	}

	if err := uc.agentRepo.Create(ctx, agentInfo); err != nil {
		return nil, fmt.Errorf("create agentInfo: %w", err)
	}

	token, err := uc.generateToken(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("generate agentInfo token: %w", err)
	}

	return &RegisterAgentOutput{Agent: *agentInfo, Token: token}, nil
}

func (uc *RegisterAgentUseCase) generateToken(ctx context.Context, agentID string) (string, error) {
	secretKey, salt, err := uc.secretProvider.GetAgentSecret(ctx)
	if err != nil {
		return "", err
	}
	if secretKey == "" {
		return "", fmt.Errorf("agent secret key is empty")
	}
	if salt == "" {
		return "", fmt.Errorf("agent salt is empty")
	}

	h := hmac.New(sha256.New, []byte(secretKey))
	h.Write([]byte(agentID))
	h.Write([]byte(salt))
	signature := base64.URLEncoding.EncodeToString(h.Sum(nil))
	return fmt.Sprintf("%s:%s", agentID, signature), nil
}
