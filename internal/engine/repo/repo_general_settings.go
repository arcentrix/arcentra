// Copyright 2025 Arcentra Team
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

type IGeneralSettingsRepository interface {
	UpdateGeneralSettings(settings *model.GeneralSettings) error
	GetGeneralSettingsByID(settingsId string) (*model.GeneralSettings, error)
	GetGeneralSettingsByName(category, name string) (*model.GeneralSettings, error)
	GetGeneralSettingsList(pageNum, pageSize int, category string) ([]*model.GeneralSettings, int64, error)
	GetCategories() ([]string, error)
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

// UpdateGeneralSettings updates a general settings by settings ID
func (gsr *GeneralSettingsRepo) UpdateGeneralSettings(settings *model.GeneralSettings) error {
	err := gsr.Database().Table(settings.TableName()).
		Omit("id", "settings_id", "category", "name").
		Where("settings_id = ?", settings.SettingsId).
		Updates(settings).Error
	if err != nil {
		return err
	}

	gsr.clearGeneralSettingsCache(settings.Name)
	return nil
}

// GetGeneralSettingsByID gets a general settings by settings ID (with Redis JSON cache)
func (gsr *GeneralSettingsRepo) GetGeneralSettingsByID(settingsId string) (*model.GeneralSettings, error) {
	// First query database to get the name and category
	var tempSettings model.GeneralSettings
	err := gsr.Database().Table(tempSettings.TableName()).
		Select("name", "category").
		Where("settings_id = ?", settingsId).
		First(&tempSettings).Error
	if err != nil {
		return nil, err
	}

	// Use name as cache key (category is used in query but cache key is still by name)
	return gsr.getGeneralSettingsByName(tempSettings.Name, tempSettings.Category, settingsId)
}

// GetGeneralSettingsByName gets a general settings by category and name (with Redis JSON cache)
func (gsr *GeneralSettingsRepo) GetGeneralSettingsByName(category, name string) (*model.GeneralSettings, error) {
	return gsr.getGeneralSettingsByName(name, category, "")
}

// getGeneralSettingsByName gets general settings by name using CachedQuery (stores as JSON string in Redis)
func (gsr *GeneralSettingsRepo) getGeneralSettingsByName(name string, category string, settingsId string) (*model.GeneralSettings, error) {
	ctx := context.Background()

	keyFunc := func(params ...any) string {
		return consts.GeneralSettingsKeyByName + params[0].(string)
	}

	queryFunc := func(ctx context.Context) (*model.GeneralSettings, error) {
		var settings model.GeneralSettings
		query := gsr.Database().Table(settings.TableName()).
			Select("id", "settings_id", "category", "name", "display_name", "data", "schema", "description", "created_at", "updated_at")

		if settingsId != "" {
			query = query.Where("settings_id = ?", settingsId)
		} else {
			// Query by category and name
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

// GetGeneralSettingsList gets general settings list with pagination and filters
// Note: List queries are not cached because they return multiple records and cannot use a single name as cache key
func (gsr *GeneralSettingsRepo) GetGeneralSettingsList(pageNum, pageSize int, category string) ([]*model.GeneralSettings, int64, error) {
	var settingsList []*model.GeneralSettings
	var settings model.GeneralSettings
	var total int64

	query := gsr.Database().Table(settings.TableName())

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

// GetCategories gets all distinct categories
func (gsr *GeneralSettingsRepo) GetCategories() ([]string, error) {
	var categories []string
	var settings model.GeneralSettings
	err := gsr.Database().Table(settings.TableName()).
		Distinct("category").
		Pluck("category", &categories).Error
	return categories, err
}

// clearGeneralSettingsCache 清除通用设置的缓存（删除 Redis JSON 字符串）
func (gsr *GeneralSettingsRepo) clearGeneralSettingsCache(name string) {
	if gsr.ICache == nil {
		return
	}
	ctx := context.Background()

	keyFunc := func(params ...any) string {
		return consts.GeneralSettingsKeyByName + params[0].(string)
	}
	cq := cache.NewCachedQuery[*model.GeneralSettings](gsr.ICache, keyFunc, nil)
	_ = cq.Invalidate(ctx, name)
}
