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

package plugin

import (
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/google/wire"
)

// ProviderSet provides plugin layer related dependencies
var ProviderSet = wire.NewSet(
	ProvidePluginManager,
)

// ProvidePluginManager provides plugin manager instance
// pluginConfigs 是从配置文件加载的插件配置
func ProvidePluginManager(pluginConfigs map[string]any) *Manager {
	// Create plugin manager configuration
	config := &ManagerConfig{
		Timeout:    30 * time.Second,
		MaxRetries: 3,
	}

	// Create plugin manager
	m := NewManager(config)

	// 如果配置为空，使用空配置
	if pluginConfigs == nil {
		return nil
	}

	// 从全局注册表加载所有已注册的插件，并传入配置文件中的插件配置
	if err := m.RegisterPluginsFromRegistry(pluginConfigs); err != nil {
		log.Warnw("failed to register plugins from registry", "error", err)
	}

	log.Infow("plugin manager initialized", "plugin_count", m.Count())
	return m
}
