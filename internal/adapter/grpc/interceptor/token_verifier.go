// Copyright 2026 Arcentra Authors.
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

package interceptor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/domain/agent"
	"github.com/arcentrix/arcentra/internal/domain/project"
	"github.com/arcentrix/arcentra/pkg/store/cache"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/v9"
)

type agentSecretConfig struct {
	Salt      string `json:"salt"`
	SecretKey string `json:"secret_key"`
}

type agentTokenVerifier struct {
	agentRepo    agent.IAgentRepository
	settingsRepo project.IGeneralSettingsRepository
	cache        cache.ICache
}

const (
	redisCacheKey = "agent:agent_secret_key"
	redisCacheTTL = 24 * time.Hour
)

func NewAgentTokenVerifier(
	agentRepo agent.IAgentRepository,
	settingsRepo project.IGeneralSettingsRepository,
	c cache.ICache,
) TokenVerifier {
	return &agentTokenVerifier{
		agentRepo:    agentRepo,
		settingsRepo: settingsRepo,
		cache:        c,
	}
}

func (v *agentTokenVerifier) getSecretConfig(ctx context.Context) (*agentSecretConfig, error) {
	if v.cache != nil {
		cached, err := v.cache.Get(ctx, redisCacheKey).Result()
		if err == nil && cached != "" {
			var cfg agentSecretConfig
			if sonic.Unmarshal([]byte(cached), &cfg) == nil && cfg.SecretKey != "" {
				return &cfg, nil
			}
		}
		if err != nil && !isCacheMiss(err) {
			log.Warnw("cache get failed, fallback to DB", "error", err)
		}
	}

	settings, err := v.settingsRepo.GetByName(ctx, "system", "agent_secret_key")
	if err != nil {
		log.Errorw("failed to get agent secret key configuration", "error", err)
		return nil, err
	}

	var config agentSecretConfig
	if err := sonic.Unmarshal(settings.Data, &config); err != nil {
		log.Errorw("failed to unmarshal agent secret key config", "error", err)
		return nil, err
	}

	if config.SecretKey == "" || config.Salt == "" {
		return nil, fmt.Errorf("invalid secret config from DB")
	}

	if v.cache != nil {
		bytes, _ := sonic.Marshal(&config)
		if setErr := v.cache.Set(ctx, redisCacheKey, bytes, redisCacheTTL).Err(); setErr != nil {
			log.Warnw("failed to cache agent secret config", "error", setErr)
		}
	}

	return &config, nil
}

func isCacheMiss(err error) bool {
	return errors.Is(err, redis.Nil) || strings.Contains(err.Error(), "not found")
}

func (v *agentTokenVerifier) generateToken(ctx context.Context, agentID string) (string, error) {
	config, err := v.getSecretConfig(ctx)
	if err != nil {
		return "", err
	}

	h := hmac.New(sha256.New, []byte(config.SecretKey))
	h.Write([]byte(agentID))
	h.Write([]byte(config.Salt))
	signature := h.Sum(nil)

	signatureStr := base64.URLEncoding.EncodeToString(signature)
	return fmt.Sprintf("%s:%s", agentID, signatureStr), nil
}

func (v *agentTokenVerifier) parseTokenParts(token string) (agentID string, signature string, err error) {
	parts := strings.SplitN(token, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid token format: expected agentID:signature")
	}
	return parts[0], parts[1], nil
}

func (v *agentTokenVerifier) VerifyAgentToken(ctx context.Context, token string, agentIDHint string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("token is empty")
	}

	if strings.Contains(token, ":") {
		agentID, _, err := v.parseTokenParts(token)
		if err != nil {
			return "", fmt.Errorf("failed to parse token: %w", err)
		}
		if agentID == "" {
			return "", fmt.Errorf("agentID is empty in token")
		}

		_, err = v.agentRepo.Get(ctx, agentID)
		if err != nil {
			return "", fmt.Errorf("agent not found: %s", agentID)
		}

		expectedToken, err := v.generateToken(ctx, agentID)
		if err != nil {
			return "", fmt.Errorf("failed to generate expected token: %w", err)
		}

		if token != expectedToken {
			return "", fmt.Errorf("invalid token signature")
		}

		return agentID, nil
	}

	if agentIDHint != "" {
		expectedToken, err := v.generateToken(ctx, agentIDHint)
		if err == nil {
			_, expectedSignature, _ := v.parseTokenParts(expectedToken)
			if token == expectedSignature {
				_, err := v.agentRepo.Get(ctx, agentIDHint)
				if err == nil {
					return agentIDHint, nil
				}
			}
		}
	}

	agents, _, err := v.agentRepo.List(ctx, 1, 1000)
	if err != nil {
		return "", fmt.Errorf("failed to list agents: %w", err)
	}

	for _, agent := range agents {
		if agentIDHint != "" && agent.AgentID == agentIDHint {
			continue
		}
		expectedToken, err := v.generateToken(ctx, agent.AgentID)
		if err != nil {
			continue
		}
		_, expectedSignature, _ := v.parseTokenParts(expectedToken)
		if token == expectedSignature {
			return agent.AgentID, nil
		}
	}

	return "", fmt.Errorf("no matching agent found for token")
}
