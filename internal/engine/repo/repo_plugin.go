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
	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/log"
)

type IPluginRepository interface {
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
