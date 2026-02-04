// Copyright 2025 Arcade Team
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
	"github.com/arcentrix/arcade/internal/engine/model"
	"github.com/arcentrix/arcade/pkg/database"
	"github.com/arcentrix/arcade/pkg/log"
	"gorm.io/gorm"
)

type IPluginRepository interface {
	// CreateOrUpdatePlugin 创建或更新插件信息（基于 plugin_id 和 version）
	CreateOrUpdatePlugin(plugin *model.Plugin) error
	// GetPluginByPluginIdAndVersion 根据 plugin_id 和 version 获取插件
	GetPluginByPluginIdAndVersion(pluginId, version string) (*model.Plugin, error)
	// ListPluginsByPluginId 根据 plugin_id 列出所有版本
	ListPluginsByPluginId(pluginId string) ([]model.Plugin, error)
	// ListAllPlugins 列出所有插件
	ListAllPlugins() ([]model.Plugin, error)
	// DeletePlugin 删除插件
	DeletePlugin(pluginId, version string) error
}

type PluginRepo struct {
	database.IDatabase
}

func NewPluginRepo(db database.IDatabase) IPluginRepository {
	return &PluginRepo{
		IDatabase: db,
	}
}

// CreateOrUpdatePlugin 创建或更新插件信息
// 如果插件已存在（基于 plugin_id 和 version），则更新；否则创建
func (pr *PluginRepo) CreateOrUpdatePlugin(plugin *model.Plugin) error {
	var existing model.Plugin
	err := pr.Database().Table(plugin.TableName()).
		Where("plugin_id = ? AND version = ?", plugin.PluginId, plugin.Version).
		First(&existing).Error

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			// 不存在，创建新记录
			if err := pr.Database().Table(plugin.TableName()).Create(plugin).Error; err != nil {
				return err
			}
			log.Infow("plugin created", "plugin_id", plugin.PluginId, "version", plugin.Version)
			return nil
		}
		return err
	}

	// 存在，更新记录
	updates := map[string]any{
		"name":        plugin.Name,
		"description": plugin.Description,
		"author":      plugin.Author,
		"plugin_type": plugin.PluginType,
		"repository":  plugin.Repository,
	}

	if err := pr.Database().Table(plugin.TableName()).
		Where("plugin_id = ? AND version = ?", plugin.PluginId, plugin.Version).
		Updates(updates).Error; err != nil {
		return err
	}

	log.Infow("plugin updated", "plugin_id", plugin.PluginId, "version", plugin.Version)
	return nil
}

// GetPluginByPluginIdAndVersion 根据 plugin_id 和 version 获取插件
func (pr *PluginRepo) GetPluginByPluginIdAndVersion(pluginId, version string) (*model.Plugin, error) {
	var plugin model.Plugin
	if err := pr.Database().Table(plugin.TableName()).
		Where("plugin_id = ? AND version = ?", pluginId, version).
		First(&plugin).Error; err != nil {
		return nil, err
	}
	return &plugin, nil
}

// ListPluginsByPluginId 根据 plugin_id 列出所有版本
func (pr *PluginRepo) ListPluginsByPluginId(pluginId string) ([]model.Plugin, error) {
	var plugins []model.Plugin
	if err := pr.Database().Table((&model.Plugin{}).TableName()).
		Where("plugin_id = ?", pluginId).
		Order("version DESC").
		Find(&plugins).Error; err != nil {
		return nil, err
	}
	return plugins, nil
}

// ListAllPlugins 列出所有插件
func (pr *PluginRepo) ListAllPlugins() ([]model.Plugin, error) {
	var plugins []model.Plugin
	if err := pr.Database().Table((&model.Plugin{}).TableName()).
		Order("plugin_id ASC, version DESC").
		Find(&plugins).Error; err != nil {
		return nil, err
	}
	return plugins, nil
}

// DeletePlugin 删除插件
func (pr *PluginRepo) DeletePlugin(pluginId, version string) error {
	if err := pr.Database().Table((&model.Plugin{}).TableName()).
		Where("plugin_id = ? AND version = ?", pluginId, version).
		Delete(&model.Plugin{}).Error; err != nil {
		return err
	}
	log.Infow("plugin deleted", "plugin_id", pluginId, "version", version)
	return nil
}
