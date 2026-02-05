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
	"fmt"
	"maps"
	"sync"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

var (
	pluginConfigs    map[string]any
	pluginConfigMu   sync.RWMutex
	pluginConfigOnce sync.Once
)

// LoadPluginConfig 加载插件配置文件
// configPath 是配置文件路径，如果为空则使用默认路径
func LoadPluginConfig(configPath string) (map[string]any, error) {
	var err error
	pluginConfigOnce.Do(func() {
		pluginConfigs, err = loadPluginConfigFile(configPath)
		if err != nil {
			log.Warnw("failed to load plugin config file, using empty config", "error", err, "path", configPath)
			pluginConfigs = make(map[string]any)
			err = nil // 不返回错误，使用空配置
		}
	})
	return pluginConfigs, err
}

// loadPluginConfigFile 从文件加载插件配置
func loadPluginConfigFile(configPath string) (map[string]any, error) {

	if configPath == "" {
		return make(map[string]any), nil // 文件不存在，返回空配置
	}

	config := viper.New()
	config.SetConfigFile(configPath)
	config.SetConfigType("toml")

	if err := config.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read plugin config file: %w", err)
	}

	// 监听配置文件变化
	config.WatchConfig()
	config.OnConfigChange(func(e fsnotify.Event) {
		log.Infow("plugin configuration file changed, reloading", "file", e.Name)
		if err := config.ReadInConfig(); err != nil {
			log.Errorw("failed to re-read plugin config file", "error", err, "file", e.Name)
			return
		}

		var newConfigs map[string]any
		if err := config.UnmarshalKey("plugins", &newConfigs); err != nil {
			log.Errorw("failed to unmarshal plugin config file", "error", err, "file", e.Name)
			return
		}

		pluginConfigMu.Lock()
		pluginConfigs = newConfigs
		pluginConfigMu.Unlock()
		log.Infow("plugin configuration reloaded successfully", "file", e.Name)
	})

	var configs map[string]any
	if err := config.UnmarshalKey("plugins", &configs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal plugin config file: %w", err)
	}

	if configs == nil {
		configs = make(map[string]any)
	}

	log.Infow("plugin config file loaded", "path", configPath, "plugin_count", len(configs))
	return configs, nil
}

// GetPluginConfig 获取插件配置（线程安全）
func GetPluginConfig() map[string]any {
	pluginConfigMu.RLock()
	defer pluginConfigMu.RUnlock()

	if pluginConfigs == nil {
		return make(map[string]any)
	}

	// 返回副本，避免外部修改
	result := make(map[string]any, len(pluginConfigs))
	maps.Copy(result, pluginConfigs)
	return result
}
