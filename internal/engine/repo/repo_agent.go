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

type IAgentRepository interface {
	CreateAgent(agent *model.Agent) error
	GetAgentById(id uint64) (*model.Agent, error)
	GetAgentByAgentId(agentId string) (*model.Agent, error)
	GetAgentDetailById(id uint64) (*model.AgentDetail, error)
	GetAgentDetailByAgentId(agentId string) (*model.AgentDetail, error)
	UpdateAgent(a *model.Agent) error
	UpdateAgentById(id uint64, updates map[string]any) error
	UpdateAgentByAgentId(agentId string, updates map[string]any) error
	DeleteAgent(id uint64) error
	DeleteAgentByAgentId(agentId string) error
	ListAgent(pageNum, pageSize int) ([]model.Agent, int64, error)
	GetAgentStatistics() (int64, int64, int64, error) // total, online, offline
}

type AgentRepo struct {
	database.IDatabase
	cache.ICache
}

func NewAgentRepo(db database.IDatabase, cache cache.ICache) IAgentRepository {
	if cache == nil {
		log.Warnw("AgentRepo initialized without cache, caching will be disabled")
	}
	return &AgentRepo{
		IDatabase: db,
		ICache:    cache,
	}
}

// CreateAgent creates a new agent
func (ar *AgentRepo) CreateAgent(agent *model.Agent) error {
	if err := ar.Database().Table(agent.TableName()).Create(agent).Error; err != nil {
		return err
	}
	return nil
}

func (ar *AgentRepo) GetAgentById(id uint64) (*model.Agent, error) {
	var agent model.Agent
	if err := ar.Database().Table(agent.TableName()).
		Select("id", "agent_id", "agent_name", "address", "port", "os", "arch", "version", "status", "labels", "metrics", "is_enabled", "created_at", "updated_at").
		Where("id = ?", id).First(&agent).Error; err != nil {
		return nil, err
	}
	return &agent, nil
}

func (ar *AgentRepo) GetAgentByAgentId(agentId string) (*model.Agent, error) {
	ctx := context.Background()

	keyFunc := func(params ...any) string {
		return consts.AgentDetailKey + params[0].(string)
	}

	queryFunc := func(ctx context.Context) (*model.AgentDetail, error) {
		var agent model.Agent
		if err := ar.Database().Table(agent.TableName()).
			Select("id", "agent_id", "agent_name", "address", "port", "os", "arch", "version", "status", "labels", "metrics", "is_enabled", "created_at", "updated_at").
			Where("agent_id = ?", agentId).First(&agent).Error; err != nil {
			return nil, err
		}
		return &model.AgentDetail{Agent: agent}, nil
	}

	cq := cache.NewCachedQuery(
		ar.ICache,
		keyFunc,
		queryFunc,
		cache.WithTTL[*model.AgentDetail](5*time.Minute),
		cache.WithLogPrefix[*model.AgentDetail]("[AgentRepo]"),
	)

	detail, err := cq.Get(ctx, agentId)
	if err != nil {
		return nil, err
	}
	return &detail.Agent, nil
}

func (ar *AgentRepo) GetAgentDetailById(id uint64) (*model.AgentDetail, error) {
	var agent model.Agent
	if err := ar.Database().Table(agent.TableName()).
		Select("id", "agent_id", "agent_name", "address", "port", "os", "arch", "version", "status", "labels", "metrics", "is_enabled", "created_at", "updated_at").
		Where("id = ?", id).First(&agent).Error; err != nil {
		return nil, err
	}

	// Try to get from cache first
	return ar.getAgentDetailByAgentId(agent.AgentId)
}

func (ar *AgentRepo) getAgentDetailByAgentId(agentId string) (*model.AgentDetail, error) {
	ctx := context.Background()

	keyFunc := func(params ...any) string {
		return consts.AgentDetailKey + params[0].(string)
	}

	queryFunc := func(ctx context.Context) (*model.AgentDetail, error) {
		var agent model.Agent
		if err := ar.Database().Table(agent.TableName()).
			Select("id", "agent_id", "agent_name", "address", "port", "os", "arch", "version", "status", "labels", "metrics", "is_enabled", "created_at", "updated_at").
			Where("agent_id = ?", agentId).First(&agent).Error; err != nil {
			return nil, err
		}
		return &model.AgentDetail{Agent: agent}, nil
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

func (ar *AgentRepo) GetAgentDetailByAgentId(agentId string) (*model.AgentDetail, error) {
	return ar.getAgentDetailByAgentId(agentId)
}

func (ar *AgentRepo) UpdateAgent(a *model.Agent) error {
	if err := ar.Database().Model(a).Updates(a).Error; err != nil {
		return err
	}

	// Invalidate cache
	ar.invalidateAgentCache(a.AgentId)
	return nil
}

func (ar *AgentRepo) UpdateAgentById(id uint64, updates map[string]any) error {
	// Get agent_id before update for cache invalidation
	var agent model.Agent
	if err := ar.Database().Table(agent.TableName()).
		Select("id", "agent_id", "agent_name", "address", "port", "os", "arch", "version", "status", "labels", "metrics", "is_enabled", "created_at", "updated_at").
		Where("id = ?", id).First(&agent).Error; err != nil {
		return err
	}

	if err := ar.Database().Table(agent.TableName()).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}

	// Invalidate cache
	ar.invalidateAgentCache(agent.AgentId)
	return nil
}

func (ar *AgentRepo) UpdateAgentByAgentId(agentId string, updates map[string]any) error {
	var agent model.Agent
	if err := ar.Database().Table(agent.TableName()).
		Where("agent_id = ?", agentId).Updates(updates).Error; err != nil {
		return err
	}

	// For heartbeat updates (last_heartbeat, status), refresh cache instead of invalidating
	// This improves performance for frequent heartbeat calls
	if len(updates) == 2 {
		if _, hasHeartbeat := updates["last_heartbeat"]; hasHeartbeat {
			if _, hasStatus := updates["status"]; hasStatus {
				// This is likely a heartbeat update, refresh cache
				ar.refreshAgentCache(agentId)
				return nil
			}
		}
	}

	// For other updates, invalidate cache to ensure next read gets fresh data
	ar.invalidateAgentCache(agentId)
	return nil
}

func (ar *AgentRepo) DeleteAgent(id uint64) error {
	// Get agent_id before delete for cache invalidation
	var agent model.Agent
	if err := ar.Database().Table(agent.TableName()).
		Select("id", "agent_id", "agent_name", "address", "port", "os", "arch", "version", "status", "labels", "metrics", "is_enabled", "created_at", "updated_at").
		Where("id = ?", id).First(&agent).Error; err != nil {
		return err
	}

	if err := ar.Database().Table(agent.TableName()).Where("id = ?", id).Delete(&model.Agent{}).Error; err != nil {
		return err
	}

	// Invalidate cache
	ar.invalidateAgentCache(agent.AgentId)
	return nil
}

func (ar *AgentRepo) DeleteAgentByAgentId(agentId string) error {
	if err := ar.Database().Table((&model.Agent{}).TableName()).
		Where("agent_id = ?", agentId).Delete(&model.Agent{}).Error; err != nil {
		return err
	}

	// Invalidate cache
	ar.invalidateAgentCache(agentId)
	return nil
}

func (ar *AgentRepo) ListAgent(pageNum, pageSize int) ([]model.Agent, int64, error) {
	var agents []model.Agent
	var agent model.Agent
	var count int64
	offset := (pageNum - 1) * pageSize

	if err := ar.Database().Table(agent.TableName()).Count(&count).Error; err != nil {
		return nil, 0, err
	}

	if err := ar.Database().Select("id, agent_id, agent_name, address, port, os, arch, version, status, labels, metrics, is_enabled").
		Table(agent.TableName()).
		Offset(offset).Limit(pageSize).Find(&agents).Error; err != nil {
		return nil, 0, err
	}
	return agents, count, nil
}

// GetAgentStatistics 获取 agent 统计信息：总数、在线数、离线数
func (ar *AgentRepo) GetAgentStatistics() (int64, int64, int64, error) {
	var agent model.Agent
	var total, online, offline int64

	// 获取总数
	if err := ar.Database().Table(agent.TableName()).Count(&total).Error; err != nil {
		return 0, 0, 0, err
	}

	// 获取在线数（status = 1）
	if err := ar.Database().Table(agent.TableName()).Where("status = ?", 1).Count(&online).Error; err != nil {
		return 0, 0, 0, err
	}

	// 获取离线数（status = 2）
	if err := ar.Database().Table(agent.TableName()).Where("status = ?", 2).Count(&offline).Error; err != nil {
		return 0, 0, 0, err
	}

	return total, online, offline, nil
}

// invalidateAgentCache 清除 agent 缓存
func (ar *AgentRepo) invalidateAgentCache(agentId string) {
	ctx := context.Background()
	keyFunc := func(params ...any) string {
		return consts.AgentDetailKey + params[0].(string)
	}
	cq := cache.NewCachedQuery[*model.AgentDetail](ar.ICache, keyFunc, nil)
	_ = cq.Invalidate(ctx, agentId)
}

// refreshAgentCache 刷新 agent 缓存（重新加载并缓存，用于心跳等频繁更新场景）
func (ar *AgentRepo) refreshAgentCache(agentId string) {
	if ar.ICache == nil {
		return
	}
	// Re-fetch and cache the agent detail (this will update cache with latest data)
	_, err := ar.getAgentDetailByAgentId(agentId)
	if err == nil {
		log.Debugw("agent cache refreshed after heartbeat update", "agentId", agentId)
	} else {
		log.Warnw("failed to refresh agent cache", "agentId", agentId, "error", err)
		ar.invalidateAgentCache(agentId)
	}
}
