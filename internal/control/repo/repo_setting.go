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

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm/clause"
)

// ISettingRepository defines setting persistence with context support.
type ISettingRepository interface {
	// Get returns a single setting by workspace and name.
	Get(ctx context.Context, workspace, name string) (*model.Setting, error)
	// Upsert creates or updates a setting (ON CONFLICT DO UPDATE).
	Upsert(ctx context.Context, setting *model.Setting) error
	// List returns all settings for a given workspace.
	List(ctx context.Context, workspace string) ([]*model.Setting, error)
	// Delete removes a setting by workspace and name.
	Delete(ctx context.Context, workspace, name string) error
}

const (
	settingCacheTTL = 1 * time.Hour
)

// SettingRepo implements ISettingRepository using GORM and cache.
type SettingRepo struct {
	database.IDatabase
	cache.ICache
}

// NewSettingRepo creates a new SettingRepo.
func NewSettingRepo(db database.IDatabase, ch cache.ICache) ISettingRepository {
	return &SettingRepo{
		IDatabase: db,
		ICache:    ch,
	}
}

// Get returns a single setting by workspace and name, with cache support.
func (sr *SettingRepo) Get(ctx context.Context, workspace, name string) (*model.Setting, error) {
	keyFunc := func(params ...any) string {
		return consts.SettingKeyPrefix + params[0].(string) + ":" + params[1].(string)
	}

	queryFunc := func(ctx context.Context) (*model.Setting, error) {
		var setting model.Setting
		err := sr.Database().WithContext(ctx).
			Where("workspace = ? AND name = ?", workspace, name).
			First(&setting).Error
		if err != nil {
			return nil, err
		}
		return &setting, nil
	}

	cq := cache.NewCachedQuery(
		sr.ICache,
		keyFunc,
		queryFunc,
		cache.WithTTL[*model.Setting](settingCacheTTL),
		cache.WithLogPrefix[*model.Setting]("[SettingRepo]"),
	)

	return cq.Get(ctx, workspace, name)
}

// Upsert creates or updates a setting using ON CONFLICT (workspace, name) DO UPDATE.
func (sr *SettingRepo) Upsert(ctx context.Context, setting *model.Setting) error {
	err := sr.Database().WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "workspace"}, {Name: "name"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
		}).
		Create(setting).Error
	if err != nil {
		return err
	}

	sr.clearSettingCache(ctx, setting.Workspace, setting.Name)
	return nil
}

// List returns all settings for the given workspace.
func (sr *SettingRepo) List(ctx context.Context, workspace string) ([]*model.Setting, error) {
	var settings []*model.Setting
	err := sr.Database().WithContext(ctx).
		Where("workspace = ?", workspace).
		Order("name ASC").
		Find(&settings).Error
	return settings, err
}

// Delete removes a setting by workspace and name.
func (sr *SettingRepo) Delete(ctx context.Context, workspace, name string) error {
	err := sr.Database().WithContext(ctx).
		Where("workspace = ? AND name = ?", workspace, name).
		Delete(&model.Setting{}).Error
	if err != nil {
		return err
	}

	sr.clearSettingCache(ctx, workspace, name)
	return nil
}

// clearSettingCache invalidates the cache entry for a given workspace+name.
func (sr *SettingRepo) clearSettingCache(ctx context.Context, workspace, name string) {
	if sr.ICache == nil {
		return
	}

	keyFunc := func(params ...any) string {
		return consts.SettingKeyPrefix + params[0].(string) + ":" + params[1].(string)
	}
	cq := cache.NewCachedQuery[*model.Setting](sr.ICache, keyFunc, nil)
	_ = cq.Invalidate(ctx, workspace, name)
}
