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
)

// IGeneralSettingsRepository defines general settings persistence with context support.
type IGeneralSettingsRepository interface {
	Update(ctx context.Context, settings *model.GeneralSettings) error
	Get(ctx context.Context, settingsId string) (*model.GeneralSettings, error)
	GetByName(ctx context.Context, category, name string) (*model.GeneralSettings, error)
	List(ctx context.Context, pageNum, pageSize int, category string) ([]*model.GeneralSettings, int64, error)
	GetCategories(ctx context.Context) ([]string, error)
}

const (
	// 缓存过期时间（1小时）
	generalSettingsCacheTTL = 1 * time.Hour
)

type GeneralSettingsRepo struct {
	database.IDatabase
	cache.ICache
}

func NewGeneralSettingsRepo(db database.IDatabase, cache cache.ICache) IGeneralSettingsRepository {
	return &GeneralSettingsRepo{
		IDatabase: db,
		ICache:    cache,
	}
}

// Update updates general settings by settingsId.
func (gsr *GeneralSettingsRepo) Update(ctx context.Context, settings *model.GeneralSettings) error {
	err := gsr.Database().WithContext(ctx).Table(settings.TableName()).
		Omit("id", "settings_id", "category", "name").
		Where("settings_id = ?", settings.SettingsId).
		Updates(settings).Error
	if err != nil {
		return err
	}
	gsr.clearGeneralSettingsCache(ctx, settings.Name)
	return nil
}

// Get returns general settings by settingsId.
func (gsr *GeneralSettingsRepo) Get(ctx context.Context, settingsId string) (*model.GeneralSettings, error) {
	var tempSettings model.GeneralSettings
	err := gsr.Database().WithContext(ctx).Table(tempSettings.TableName()).
		Select("name", "category").
		Where("settings_id = ?", settingsId).
		First(&tempSettings).Error
	if err != nil {
		return nil, err
	}
	return gsr.getGeneralSettingsByName(ctx, tempSettings.Name, tempSettings.Category, settingsId)
}

// GetByName returns general settings by category and name.
func (gsr *GeneralSettingsRepo) GetByName(ctx context.Context, category, name string) (*model.GeneralSettings, error) {
	return gsr.getGeneralSettingsByName(ctx, name, category, "")
}

func (gsr *GeneralSettingsRepo) getGeneralSettingsByName(ctx context.Context, name string, category string, settingsId string) (*model.GeneralSettings, error) {
	keyFunc := func(params ...any) string {
		return consts.GeneralSettingsKeyByName + params[0].(string)
	}

	queryFunc := func(ctx context.Context) (*model.GeneralSettings, error) {
		var settings model.GeneralSettings
		query := gsr.Database().WithContext(ctx).Table(settings.TableName()).
			Select("id", "settings_id", "category", "name", "display_name", "data", "schema", "description", "created_at", "updated_at")

		if settingsId != "" {
			query = query.Where("settings_id = ?", settingsId)
		} else {
			query = query.Where("category = ? AND name = ?", category, name)
		}

		err := query.First(&settings).Error
		if err != nil {
			return nil, err
		}
		return &settings, nil
	}

	cq := cache.NewCachedQuery(
		gsr.ICache,
		keyFunc,
		queryFunc,
		cache.WithTTL[*model.GeneralSettings](generalSettingsCacheTTL),
		cache.WithLogPrefix[*model.GeneralSettings]("[GeneralSettingsRepo]"),
	)

	return cq.Get(ctx, name)
}

// List lists general settings with pagination and filters.
func (gsr *GeneralSettingsRepo) List(ctx context.Context, pageNum, pageSize int, category string) ([]*model.GeneralSettings, int64, error) {
	var settingsList []*model.GeneralSettings
	var settings model.GeneralSettings
	var total int64

	query := gsr.Database().WithContext(ctx).Table(settings.TableName())

	// apply filters
	if category != "" {
		query = query.Where("category = ?", category)
	}

	// get total count
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// get paginated list (specify fields, exclude create_time and update_time)
	offset := (pageNum - 1) * pageSize
	err := query.Select("id", "settings_id", "category", "name", "display_name", "data", "schema", "description").
		Order("id DESC").
		Limit(pageSize).
		Offset(offset).
		Find(&settingsList).Error

	return settingsList, total, err
}

// GetCategories returns all distinct categories.
func (gsr *GeneralSettingsRepo) GetCategories(ctx context.Context) ([]string, error) {
	var categories []string
	var settings model.GeneralSettings
	err := gsr.Database().WithContext(ctx).Table(settings.TableName()).
		Distinct("category").
		Pluck("category", &categories).Error
	return categories, err
}

func (gsr *GeneralSettingsRepo) clearGeneralSettingsCache(ctx context.Context, name string) {
	if gsr.ICache == nil {
		return
	}

	keyFunc := func(params ...any) string {
		return consts.GeneralSettingsKeyByName + params[0].(string)
	}
	cq := cache.NewCachedQuery[*model.GeneralSettings](gsr.ICache, keyFunc, nil)
	_ = cq.Invalidate(ctx, name)
}
