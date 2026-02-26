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

package repo

import (
	"context"
	"time"

	"github.com/arcentrix/arcentra/internal/engine/consts"
	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/log"
)

// IAgentRepository defines agent persistence with context support for timeout, tracing and cancellation.
// All methods use business identifier agentId; no ByID/ByXXX duplication.
type IAgentRepository interface {
	Create(ctx context.Context, agent *model.Agent) error
	Get(ctx context.Context, agentId string) (*model.Agent, error)
	GetDetail(ctx context.Context, agentId string) (*model.AgentDetail, error)
	Update(ctx context.Context, agent *model.Agent) error
	Patch(ctx context.Context, agentId string, updates map[string]any) error
	Delete(ctx context.Context, agentId string) error
	List(ctx context.Context, page, size int) ([]model.Agent, int64, error)
	Statistics(ctx context.Context) (total, online, offline int64, err error)
}

type AgentRepo struct {
	database.IDatabase
	cache.ICache
}

// NewAgentRepo creates an agent repository with optional cache.
func NewAgentRepo(db database.IDatabase, cache cache.ICache) IAgentRepository {
	if cache == nil {
		log.Warnw("AgentRepo initialized without cache, caching will be disabled")
	}
	return &AgentRepo{
		IDatabase: db,
		ICache:    cache,
	}
}

// Create creates a new agent.
func (ar *AgentRepo) Create(ctx context.Context, agent *model.Agent) error {
	if err := ar.Database().WithContext(ctx).Table(agent.TableName()).Create(agent).Error; err != nil {
		return err
	}
	return nil
}

// Get returns agent by agentId.
func (ar *AgentRepo) Get(ctx context.Context, agentId string) (*model.Agent, error) {
	detail, err := ar.getAgentDetailCached(ctx, agentId)
	if err != nil {
		return nil, err
	}
	return &detail.Agent, nil
}

var agentSelectFields = []string{
	"id",
	"agent_id",
	"agent_name",
	"address",
	"port",
	"os",
	"arch",
	"version",
	"status",
	"labels",
	"metrics",
	"is_enabled",
	"created_at",
	"updated_at",
}

// GetDetail returns agent detail by agentId.
func (ar *AgentRepo) GetDetail(ctx context.Context, agentId string) (*model.AgentDetail, error) {
	return ar.getAgentDetailCached(ctx, agentId)
}

func (ar *AgentRepo) getAgentDetailCached(ctx context.Context, agentId string) (*model.AgentDetail, error) {
	keyFunc := func(params ...any) string {
		return consts.AgentDetailKey + params[0].(string)
	}

	queryFunc := func(ctx context.Context) (*model.AgentDetail, error) {
		agent, err := ar.queryAgentByAgentID(ctx, agentId)
		if err != nil {
			return nil, err
		}
		return &model.AgentDetail{Agent: *agent}, nil
	}

	cq := cache.NewCachedQuery(
		ar.ICache,
		keyFunc,
		queryFunc,
		cache.WithTTL[*model.AgentDetail](5*time.Minute),
		cache.WithLogPrefix[*model.AgentDetail]("[AgentRepo]"),
	)

	return cq.Get(ctx, agentId)
}

func (ar *AgentRepo) queryAgentByAgentID(ctx context.Context, agentId string) (*model.Agent, error) {
	var agent model.Agent
	if err := ar.Database().
		WithContext(ctx).
		Table(agent.TableName()).
		Select(agentSelectFields).
		Where("agent_id = ?", agentId).
		First(&agent).Error; err != nil {
		return nil, err
	}
	return &agent, nil
}

// Update updates agent by full model.
func (ar *AgentRepo) Update(ctx context.Context, agent *model.Agent) error {
	if err := ar.Database().WithContext(ctx).Model(agent).Updates(agent).Error; err != nil {
		return err
	}
	ar.invalidateAgentCache(ctx, agent.AgentId)
	return nil
}

// Patch patches agent fields by agentId.
func (ar *AgentRepo) Patch(ctx context.Context, agentId string, updates map[string]any) error {
	if err := ar.Database().WithContext(ctx).Table((&model.Agent{}).TableName()).
		Where("agent_id = ?", agentId).Updates(updates).Error; err != nil {
		return err
	}

	// For heartbeat updates (last_heartbeat, status), refresh cache instead of invalidating
	if len(updates) == 2 {
		if _, hasHeartbeat := updates["last_heartbeat"]; hasHeartbeat {
			if _, hasStatus := updates["status"]; hasStatus {
				ar.refreshAgentCache(ctx, agentId)
				return nil
			}
		}
	}
	ar.invalidateAgentCache(ctx, agentId)
	return nil
}

// Delete deletes agent by agentId.
func (ar *AgentRepo) Delete(ctx context.Context, agentId string) error {
	if err := ar.Database().WithContext(ctx).Table((&model.Agent{}).TableName()).
		Where("agent_id = ?", agentId).Delete(&model.Agent{}).Error; err != nil {
		return err
	}
	ar.invalidateAgentCache(ctx, agentId)
	return nil
}

// List lists agents with pagination.
func (ar *AgentRepo) List(ctx context.Context, page, size int) ([]model.Agent, int64, error) {
	var agents []model.Agent
	var agent model.Agent
	var count int64
	offset := (page - 1) * size

	if err := ar.Database().WithContext(ctx).Table(agent.TableName()).Count(&count).Error; err != nil {
		return nil, 0, err
	}

	if err := ar.Database().WithContext(ctx).Select("id, agent_id, agent_name, address, port, os, arch, version, status, labels, metrics, is_enabled").
		Table(agent.TableName()).
		Offset(offset).Limit(size).Find(&agents).Error; err != nil {
		return nil, 0, err
	}
	return agents, count, nil
}

// Statistics returns agent counts: total, online, offline.
func (ar *AgentRepo) Statistics(ctx context.Context) (total, online, offline int64, err error) {
	var agent model.Agent

	if err := ar.Database().WithContext(ctx).Table(agent.TableName()).Count(&total).Error; err != nil {
		return 0, 0, 0, err
	}
	if err := ar.Database().WithContext(ctx).Table(agent.TableName()).Where("status = ?", 1).Count(&online).Error; err != nil {
		return 0, 0, 0, err
	}
	if err := ar.Database().WithContext(ctx).Table(agent.TableName()).Where("status = ?", 2).Count(&offline).Error; err != nil {
		return 0, 0, 0, err
	}
	return total, online, offline, nil
}

// invalidateAgentCache clears agent cache.
func (ar *AgentRepo) invalidateAgentCache(ctx context.Context, agentId string) {
	keyFunc := func(params ...any) string {
		return consts.AgentDetailKey + params[0].(string)
	}
	cq := cache.NewCachedQuery[*model.AgentDetail](ar.ICache, keyFunc, nil)
	_ = cq.Invalidate(ctx, agentId)
}

// refreshAgentCache refreshes agent cache after heartbeat updates.
func (ar *AgentRepo) refreshAgentCache(ctx context.Context, agentId string) {
	if ar.ICache == nil {
		return
	}
	_, err := ar.getAgentDetailCached(ctx, agentId)
	if err == nil {
		log.Debugw("agent cache refreshed after heartbeat update", "agentId", agentId)
	} else {
		log.Warnw("failed to refresh agent cache", "agentId", agentId, "error", err)
		ar.invalidateAgentCache(ctx, agentId)
	}
}
