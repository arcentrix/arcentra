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
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/util"
	"github.com/bytedance/sonic"
)

type AgentService struct {
	agentRepo              repo.IAgentRepository
	generalSettingsService *GeneralSettingsService
}

func NewAgentService(agentRepo repo.IAgentRepository, generalSettingsService *GeneralSettingsService) *AgentService {
	return &AgentService{
		agentRepo:              agentRepo,
		generalSettingsService: generalSettingsService,
	}
}

func (al *AgentService) CreateAgent(ctx context.Context, createAgentReq *model.CreateAgentReq) (*model.CreateAgentResp, error) {
	agentId := id.ShortId()
	agent := &model.Agent{
		AgentId:   agentId,
		AgentName: createAgentReq.AgentName,
		Address:   "0.0.0.0",
		Port:      "8080",
		OS:        "Linux",
		Arch:      "amd64",
		Version:   "0.0.0",
		Status:    0,
		Labels:    createAgentReq.Labels,
		IsEnabled: 1,
		Metrics:   "/metrics",
	}

	// Create Agent
	if err := al.agentRepo.Create(ctx, agent); err != nil {
		log.Errorw("create agent failed", "error", err)
		return nil, err
	}

	// Generate token for agent communication based on agentId
	token, err := al.GenerateAgentToken(ctx, agentId)
	if err != nil {
		log.Errorw("generate agent token failed", "error", err)
		return nil, err
	}

	// Return created agent with token
	resp := &model.CreateAgentResp{
		Agent: *agent,
		Token: token,
	}
	return resp, nil
}

// agentSecretConfig represents the structure of agent secret key configuration
type agentSecretConfig struct {
	Salt      string `json:"salt"`
	SecretKey string `json:"secret_key"`
}

// GenerateAgentToken generates a token based on agentId for agent communication.
func (al *AgentService) GenerateAgentToken(ctx context.Context, agentId string) (string, error) {
	settings, err := al.generalSettingsService.GetGeneralSettingsByName(ctx, "system", "agent_secret_key")
	if err != nil {
		log.Errorw("failed to get agent secret key configuration", "error", err)
		return "", err
	}

	// Parse JSON data
	var config agentSecretConfig
	if err := sonic.Unmarshal(settings.Data, &config); err != nil {
		log.Errorw("failed to parse agent secret key configuration", "error", err)
		return "", err
	}

	// Validate configuration
	if config.SecretKey == "" {
		log.Errorw("agent secret key is empty")
		return "", fmt.Errorf("agent secret key is empty")
	}
	if config.Salt == "" {
		log.Errorw("agent salt is empty")
		return "", fmt.Errorf("agent salt is empty")
	}

	// Generate token using HMAC-SHA256
	// Format: agentId:base64(signature)
	h := hmac.New(sha256.New, []byte(config.SecretKey))
	h.Write([]byte(agentId))
	h.Write([]byte(config.Salt))
	signature := h.Sum(nil)

	signatureStr := base64.URLEncoding.EncodeToString(signature)
	token := fmt.Sprintf("%s:%s", agentId, signatureStr)
	return token, nil
}

func (al *AgentService) GetAgentByAgentId(ctx context.Context, agentId string) (*model.AgentDetail, error) {
	detail, err := al.agentRepo.GetDetail(ctx, agentId)
	if err != nil {
		log.Errorw("get agent detail by agentId failed", "agentId", agentId, "error", err)
		return nil, err
	}
	return detail, nil
}

func (al *AgentService) UpdateAgentByAgentId(ctx context.Context, agentId string, updateReq *model.UpdateAgentReq) error {
	// Check if agent exists
	_, err := al.agentRepo.Get(ctx, agentId)
	if err != nil {
		log.Errorw("get agent by agentId failed", "agentId", agentId, "error", err)
		return err
	}

	// Build and update Agent fields
	updates := buildAgentUpdateMap(updateReq)
	if len(updates) > 0 {
		updates["updated_at"] = time.Now()
		if err := al.agentRepo.Patch(ctx, agentId, updates); err != nil {
			log.Errorw("update agent failed", "agentId", agentId, "error", err)
			return err
		}
	}

	return nil
}

// buildAgentUpdateMap builds update map for Agent fields
// Only allows updating agent_name and labels
func buildAgentUpdateMap(req *model.UpdateAgentReq) map[string]any {
	updates := make(map[string]any)
	util.SetIfNotNil(updates, "agent_name", req.AgentName)
	if req.Labels != nil {
		updates["labels"] = req.Labels
	}
	return updates
}

func (al *AgentService) DeleteAgentByAgentId(ctx context.Context, agentId string) error {
	if err := al.agentRepo.Delete(ctx, agentId); err != nil {
		log.Errorw("delete agent failed", "agentId", agentId, "error", err)
		return err
	}
	return nil
}

func (al *AgentService) ListAgent(ctx context.Context, pageNum, pageSize int) ([]model.Agent, int64, error) {
		agents, count, err := al.agentRepo.List(ctx, pageNum, pageSize)
	if err != nil {
		log.Errorw("list agent failed", "error", err)
		return nil, 0, err
	}
	return agents, count, err
}

func (al *AgentService) GetAgentStatistics(ctx context.Context) (int64, int64, int64, error) {
	total, online, offline, err := al.agentRepo.Statistics(ctx)
	if err != nil {
		log.Errorw("get agent statistics failed", "error", err)
		return 0, 0, 0, err
	}
	return total, online, offline, nil
}
