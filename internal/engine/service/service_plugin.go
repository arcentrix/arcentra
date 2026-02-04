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

package service

import (
	pluginmodel "github.com/arcentrix/arcade/internal/engine/model"
	pluginrepo "github.com/arcentrix/arcade/internal/engine/repo"
)

type PluginService struct {
	pluginRepo pluginrepo.IPluginRepository
}

func NewPluginService(pluginRepo pluginrepo.IPluginRepository) *PluginService {
	return &PluginService{
		pluginRepo: pluginRepo,
	}
}

// ListPlugins 获取插件列表
// pluginId: 可选，如果提供则只返回该插件的所有版本
func (ps *PluginService) ListPlugins(pluginId string) ([]pluginmodel.Plugin, error) {
	if pluginId != "" {
		// 获取指定插件的所有版本
		return ps.pluginRepo.ListPluginsByPluginId(pluginId)
	}
	// 获取所有插件
	return ps.pluginRepo.ListAllPlugins()
}

// GetPlugin 根据 pluginId 和 version 获取插件详情
func (ps *PluginService) GetPlugin(pluginId, version string) (*pluginmodel.Plugin, error) {
	return ps.pluginRepo.GetPluginByPluginIdAndVersion(pluginId, version)
}
