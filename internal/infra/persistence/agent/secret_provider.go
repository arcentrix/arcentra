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

	agentcase "github.com/arcentrix/arcentra/internal/case/agent"
	domainProject "github.com/arcentrix/arcentra/internal/domain/project"
	"github.com/bytedance/sonic"
)

var _ agentcase.IAgentSecretProvider = (*AgentSecretProvider)(nil)

type agentSecretConfig struct {
	Salt      string `json:"salt"`
	SecretKey string `json:"secret_key"`
}

type AgentSecretProvider struct {
	settingsRepo domainProject.IGeneralSettingsRepository
}

func NewAgentSecretProvider(settingsRepo domainProject.IGeneralSettingsRepository) *AgentSecretProvider {
	return &AgentSecretProvider{settingsRepo: settingsRepo}
}

func (p *AgentSecretProvider) GetAgentSecret(ctx context.Context) (secretKey, salt string, err error) {
	settings, err := p.settingsRepo.GetByName(ctx, "system", "agent_secret_key")
	if err != nil {
		return "", "", fmt.Errorf("get agent secret settings: %w", err)
	}

	var cfg agentSecretConfig
	if err := sonic.Unmarshal(settings.Data, &cfg); err != nil {
		return "", "", fmt.Errorf("unmarshal agent secret config: %w", err)
	}

	return cfg.SecretKey, cfg.Salt, nil
}
