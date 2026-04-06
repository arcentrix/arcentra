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
	"time"

	domain "github.com/arcentrix/arcentra/internal/domain/agent"
	"github.com/arcentrix/arcentra/pkg/store/cache"
	"github.com/arcentrix/arcentra/pkg/store/database"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
)

var _ domain.IAgentRepository = (*AgentRepo)(nil)

const (
	agentDetailCacheKeyPrefix = "agent:detail:"
	agentCacheTTL             = 5 * time.Minute
)

var agentSelectFields = []string{
	"id", "agent_id", "agent_name", "address", "port",
	"os", "arch", "version", "status", "labels",
	"metrics", "is_enabled", "created_at", "updated_at",
}

// AgentRepo implements domain.IAgentRepository using GORM and an optional cache.
type AgentRepo struct {
	db    database.IDatabase
	cache cache.ICache
}

func NewAgentRepo(db database.IDatabase, ch cache.ICache) *AgentRepo {
	if ch == nil {
		log.Warnw("AgentRepo initialized without cache, caching will be disabled")
	}
	return &AgentRepo{db: db, cache: ch}
}

func (r *AgentRepo) Create(ctx context.Context, agent *domain.Agent) error {
	po := AgentPOFromDomain(agent)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	agent.ID = po.ID
	agent.CreatedAt = po.CreatedAt
	agent.UpdatedAt = po.UpdatedAt
	return nil
}

func (r *AgentRepo) Get(ctx context.Context, agentID string) (*domain.Agent, error) {
	po, err := r.getAgentCached(ctx, agentID)
	if err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

func (r *AgentRepo) Update(ctx context.Context, agent *domain.Agent) error {
	po := AgentPOFromDomain(agent)
	if err := r.db.Database().WithContext(ctx).Model(po).Updates(po).Error; err != nil {
		return err
	}
	r.invalidateAgentCache(ctx, agent.AgentID)
	return nil
}

func (r *AgentRepo) Patch(ctx context.Context, agentID string, updates map[string]any) error {
	if err := r.db.Database().WithContext(ctx).
		Table(AgentPO{}.TableName()).
		Where("agent_id = ?", agentID).
		Updates(updates).Error; err != nil {
		return err
	}

	if len(updates) == 2 {
		if _, hasHeartbeat := updates["last_heartbeat"]; hasHeartbeat {
			if _, hasStatus := updates["status"]; hasStatus {
				r.refreshAgentCache(ctx, agentID)
				return nil
			}
		}
	}
	r.invalidateAgentCache(ctx, agentID)
	return nil
}

func (r *AgentRepo) Delete(ctx context.Context, agentID string) error {
	if err := r.db.Database().WithContext(ctx).
		Table(AgentPO{}.TableName()).
		Where("agent_id = ?", agentID).
		Delete(&AgentPO{}).Error; err != nil {
		return err
	}
	r.invalidateAgentCache(ctx, agentID)
	return nil
}

func (r *AgentRepo) List(ctx context.Context, page, size int) ([]domain.Agent, int64, error) {
	var pos []AgentPO
	var count int64
	tbl := AgentPO{}.TableName()
	offset := (page - 1) * size

	if err := r.db.Database().WithContext(ctx).Table(tbl).Count(&count).Error; err != nil {
		return nil, 0, err
	}
	if err := r.db.Database().WithContext(ctx).
		Select(agentSelectFields).
		Table(tbl).
		Offset(offset).Limit(size).
		Find(&pos).Error; err != nil {
		return nil, 0, err
	}

	agents := make([]domain.Agent, len(pos))
	for i := range pos {
		agents[i] = *pos[i].ToDomain()
	}
	return agents, count, nil
}

func (r *AgentRepo) Statistics(ctx context.Context) (total, online, offline int64, err error) {
	tbl := AgentPO{}.TableName()

	if err = r.db.Database().WithContext(ctx).Table(tbl).Count(&total).Error; err != nil {
		return
	}
	if err = r.db.Database().WithContext(ctx).Table(tbl).Where("status = ?", int(domain.AgentStatusOnline)).Count(&online).Error; err != nil {
		return
	}
	if err = r.db.Database().WithContext(ctx).Table(tbl).
		Where("status = ?", int(domain.AgentStatusOffline)).
		Count(&offline).Error; err != nil {
		return
	}
	return
}

// --- cache helpers ---

func (r *AgentRepo) getAgentCached(ctx context.Context, agentID string) (*AgentPO, error) {
	keyFunc := func(params ...any) string {
		return agentDetailCacheKeyPrefix + params[0].(string)
	}
	queryFunc := func(ctx context.Context) (*AgentPO, error) {
		return r.queryAgentByAgentID(ctx, agentID)
	}
	cq := cache.NewCachedQuery(
		r.cache, keyFunc, queryFunc,
		cache.WithTTL[*AgentPO](agentCacheTTL),
		cache.WithLogPrefix[*AgentPO]("[AgentRepo]"),
	)
	return cq.Get(ctx, agentID)
}

func (r *AgentRepo) queryAgentByAgentID(ctx context.Context, agentID string) (*AgentPO, error) {
	var po AgentPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Select(agentSelectFields).
		Where("agent_id = ?", agentID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return &po, nil
}

func (r *AgentRepo) invalidateAgentCache(ctx context.Context, agentID string) {
	keyFunc := func(params ...any) string {
		return agentDetailCacheKeyPrefix + params[0].(string)
	}
	cq := cache.NewCachedQuery[*AgentPO](r.cache, keyFunc, nil)
	_ = cq.Invalidate(ctx, agentID)
}

func (r *AgentRepo) refreshAgentCache(ctx context.Context, agentID string) {
	if r.cache == nil {
		return
	}
	if _, err := r.getAgentCached(ctx, agentID); err != nil {
		r.invalidateAgentCache(ctx, agentID)
	}
}
