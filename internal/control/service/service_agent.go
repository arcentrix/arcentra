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
	"encoding/json"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/util"
	"github.com/bytedance/sonic"
)

type AgentService struct {
	agentRepo            repo.IAgentRepository
	stepRunRepo          repo.IStepRunRepository
	jobRunRepo           repo.IJobRunRepository
	settingService       *SettingService
	registrationTokenSvc *RegistrationTokenService
}

// NewAgentService creates a new AgentService.
func NewAgentService(
	agentRepo repo.IAgentRepository,
	stepRunRepo repo.IStepRunRepository,
	settingService *SettingService,
	jobRunRepo repo.IJobRunRepository,
) *AgentService {
	return &AgentService{
		agentRepo:      agentRepo,
		stepRunRepo:    stepRunRepo,
		jobRunRepo:     jobRunRepo,
		settingService: settingService,
	}
}

// SetRegistrationTokenService sets the registration token service for dynamic registration.
func (al *AgentService) SetRegistrationTokenService(svc *RegistrationTokenService) {
	al.registrationTokenSvc = svc
}

// ValidateRegistrationToken validates a plain-text registration token against the database.
func (al *AgentService) ValidateRegistrationToken(ctx context.Context, token string) error {
	if al.registrationTokenSvc == nil {
		return fmt.Errorf("registration token service not configured")
	}
	_, err := al.registrationTokenSvc.ValidateToken(ctx, token)
	return err
}

// DynamicRegisterAgent creates a new agent record from dynamic registration.
func (al *AgentService) DynamicRegisterAgent(ctx context.Context, agentID, agentName string, req *model.Agent) error {
	req.AgentID = agentID
	req.AgentName = agentName
	req.RegisteredBy = "dynamic"

	autoApprove, err := al.getAutoApproveSetting(ctx)
	if err != nil {
		log.Warnw("failed to read auto-approve setting, defaulting to true", "error", err)
		autoApprove = true
	}
	if autoApprove {
		req.IsEnabled = 1
	} else {
		req.IsEnabled = 0
	}

	if err := al.agentRepo.Create(ctx, req); err != nil {
		log.Errorw("dynamic register agent failed", "error", err)
		return err
	}
	return nil
}

// ApproveAgent enables an agent.
func (al *AgentService) ApproveAgent(ctx context.Context, agentID string) error {
	return al.agentRepo.Approve(ctx, agentID)
}

// getAutoApproveSetting reads AGENT_AUTO_APPROVE from t_setting and returns the boolean value.
func (al *AgentService) getAutoApproveSetting(ctx context.Context) (bool, error) {
	setting, err := al.settingService.GetSetting(ctx, consts.SettingNameAgentAutoApprove)
	if err != nil {
		return true, err // default to true if setting not found
	}

	var config struct {
		AutoApprove bool `json:"auto_approve"`
	}
	if err := json.Unmarshal(setting.Value, &config); err != nil {
		return true, err
	}
	return config.AutoApprove, nil
}

// IncrementRegistrationTokenUseCount increments the use count for a registration token.
func (al *AgentService) IncrementRegistrationTokenUseCount(ctx context.Context, id uint64) error {
	if al.registrationTokenSvc == nil || al.registrationTokenSvc.tokenRepo == nil {
		return fmt.Errorf("registration token service not configured")
	}
	return al.registrationTokenSvc.tokenRepo.IncrementUseCount(ctx, id)
}

// agentSecretConfig represents the structure of agent secret key configuration
type agentSecretConfig struct {
	Salt      string `json:"salt"`
	SecretKey string `json:"secret_key"`
}

// GenerateAgentToken generates a token based on agentID for agent communication.
func (al *AgentService) GenerateAgentToken(ctx context.Context, agentID string) (string, error) {
	setting, err := al.settingService.GetSetting(ctx, consts.SettingNameAgentSecretKey)
	if err != nil {
		log.Errorw("failed to get agent secret key configuration", "error", err)
		return "", err
	}

	var config agentSecretConfig
	if err := sonic.Unmarshal(setting.Value, &config); err != nil {
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
	// Format: agentID:base64(signature)
	h := hmac.New(sha256.New, []byte(config.SecretKey))
	h.Write([]byte(agentID))
	h.Write([]byte(config.Salt))
	signature := h.Sum(nil)

	signatureStr := base64.URLEncoding.EncodeToString(signature)
	token := fmt.Sprintf("%s:%s", agentID, signatureStr)
	return token, nil
}

func (al *AgentService) GetAgentByagentID(ctx context.Context, agentID string) (*model.AgentDetail, error) {
	detail, err := al.agentRepo.GetDetail(ctx, agentID)
	if err != nil {
		log.Errorw("get agent detail by agentID failed", "agentID", agentID, "error", err)
		return nil, err
	}
	return detail, nil
}

func (al *AgentService) UpdateAgentByagentID(ctx context.Context, agentID string, updateReq *model.UpdateAgentReq) error {
	// Check if agent exists
	_, err := al.agentRepo.Get(ctx, agentID)
	if err != nil {
		log.Errorw("get agent by agentID failed", "agentID", agentID, "error", err)
		return err
	}

	// Build and update Agent fields
	updates := buildAgentUpdateMap(updateReq)
	if len(updates) > 0 {
		updates["updated_at"] = time.Now()
		if err := al.agentRepo.Patch(ctx, agentID, updates); err != nil {
			log.Errorw("update agent failed", "agentID", agentID, "error", err)
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

func (al *AgentService) DeleteAgentByagentID(ctx context.Context, agentID string) error {
	if err := al.agentRepo.Delete(ctx, agentID); err != nil {
		log.Errorw("delete agent failed", "agentID", agentID, "error", err)
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

// MarkTimeoutAgentsOffline 检查所有心跳超时的 Agent 并标记为离线。
// 超时阈值从 AGENT_HEARTBEAT_EXPIRE_SECONDS 设置中读取，默认 180 秒。
func (al *AgentService) MarkTimeoutAgentsOffline(ctx context.Context) {
	expireSeconds := 180 // 默认：3 倍标准心跳间隔

	setting, err := al.settingService.GetSetting(ctx, consts.SettingNameAgentHeartbeatExpireSeconds)
	if err == nil && setting != nil {
		var cfg struct {
			ExpireAfterSeconds int `json:"expireAfterSeconds"`
		}
		if jsonErr := json.Unmarshal(setting.Value, &cfg); jsonErr == nil && cfg.ExpireAfterSeconds > 0 {
			expireSeconds = cfg.ExpireAfterSeconds
		}
	}

	before := time.Now().Add(-time.Duration(expireSeconds) * time.Second)
	count, err := al.agentRepo.MarkOfflineByHeartbeatTimeout(ctx, before)
	if err != nil {
		log.Errorw("mark timeout agents offline failed", "error", err)
		return
	}
	if count > 0 {
		log.Infow("marked timeout agents as offline", "count", count, "expireSeconds", expireSeconds)
	}
}
