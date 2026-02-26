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

package plugin

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
)

// Manager 是插件管理器
// 负责管理所有插件的生命周期，包括注册、初始化、健康检查等
type Manager struct {
	// 读写锁保护并发访问
	mu sync.RWMutex
	// 插件实例映射（name -> Plugin）
	plugins map[string]Plugin
	// 插件配置映射（name -> RuntimePluginConfig）
	configs map[string]*RuntimePluginConfig
	// 插件信息映射（name -> Info）
	infos map[string]*Info
	// 管理器配置
	config *ManagerConfig
}

// ManagerConfig 是插件管理器的配置
type ManagerConfig struct {
	// 超时时间
	Timeout time.Duration
	// 最大重试次数
	MaxRetries int
}

// NewManager 创建新的插件管理器
func NewManager(config *ManagerConfig) *Manager {
	if config == nil {
		config = &ManagerConfig{
			Timeout:    30 * time.Second,
			MaxRetries: 3,
		}
	}
	return &Manager{
		plugins: make(map[string]Plugin),
		configs: make(map[string]*RuntimePluginConfig),
		infos:   make(map[string]*Info),
		config:  config,
	}
}

// RegisterPlugin 注册一个插件
// name: 插件名称（必须与插件自己返回的Name()一致）
// plugin: 插件实例
// config: 插件运行时配置
func (m *Manager) RegisterPlugin(name string, plugin Plugin, config *RuntimePluginConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.registerPluginLocked(name, plugin, config)
}

// registerPluginLocked 注册插件
func (m *Manager) registerPluginLocked(name string, plugin Plugin, config *RuntimePluginConfig) error {
	// 检查插件是否已存在
	if _, exists := m.plugins[name]; exists {
		return fmt.Errorf("plugin %s already registered", name)
	}

	// 验证插件名称一致性
	if plugin.Name() != name {
		return fmt.Errorf("plugin name mismatch: expected %s, got %s", name, plugin.Name())
	}

	// 创建插件信息
	info := &Info{
		Name:        plugin.Name(),
		Description: plugin.Description(),
		Version:     plugin.Version(),
		Type:        plugin.Type(),
	}

	// 初始化插件
	if config != nil && len(config.Config) > 0 {
		if err := plugin.Init(config.Config); err != nil {
			return fmt.Errorf("initialize plugin %s: %w", name, err)
		}
	} else {
		// 使用空配置初始化
		if err := plugin.Init(json.RawMessage("{}")); err != nil {
			return fmt.Errorf("initialize plugin %s: %w", name, err)
		}
	}

	// 注册插件
	m.plugins[name] = plugin
	if config != nil {
		m.configs[name] = config
	}
	m.infos[name] = info

	log.Infow("plugin registered successfully", "plugin", name, "version", info.Version, "type", info.Type)
	return nil
}

// RegisterPluginsFromRegistry 从全局注册表注册所有插件
// 这个方法会从全局注册表中获取所有已注册的插件并初始化它们
// pluginConfigs 是从配置文件读取的插件配置，格式为 map[插件名称]配置对象
func (m *Manager) RegisterPluginsFromRegistry(pluginConfigs map[string]any) error {
	plugins := ListPlugins()
	var errors []error

	for name, plugin := range plugins {
		// 从配置文件中读取该插件的配置
		var configJSON json.RawMessage
		if pluginConfigs != nil {
			if pluginConfig, exists := pluginConfigs[name]; exists {
				// 将配置对象转换为 JSON
				configBytes, err := sonic.Marshal(pluginConfig)
				if err != nil {
					log.Warnw("failed to marshal plugin config", "plugin", name, "error", err)
					configJSON = []byte("{}")
				} else {
					configJSON = configBytes
				}
			} else {
				// 配置文件中没有该插件的配置，使用空配置
				configJSON = []byte("{}")
			}
		} else {
			// 没有提供配置文件，使用空配置
			configJSON = []byte("{}")
		}

		// 创建运行时配置
		config := &RuntimePluginConfig{
			Name:   name,
			Config: configJSON,
		}

		if err := m.RegisterPlugin(name, plugin, config); err != nil {
			log.Errorw("failed to register plugin from registry", "plugin", name, "error", err)
			errors = append(errors, fmt.Errorf("plugin %s: %w", name, err))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to register %d plugin(s): %v", len(errors), errors)
	}

	log.Infow("registered plugins from registry", "count", len(plugins))
	return nil
}

// UnregisterPlugin 取消注册一个插件
func (m *Manager) UnregisterPlugin(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 检查插件是否存在
	plugin, exists := m.plugins[name]
	if !exists {
		return fmt.Errorf("plugin %s not found", name)
	}

	// 调用插件的清理方法
	if err := plugin.Cleanup(); err != nil {
		log.Warnw("plugin cleanup failed", "plugin", name, "error", err)
	}

	// 删除插件
	delete(m.plugins, name)
	delete(m.configs, name)
	delete(m.infos, name)

	log.Infow("plugin unregistered", "plugin", name)
	return nil
}

// GetPlugin 获取一个插件
func (m *Manager) GetPlugin(name string) (Plugin, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	plugin, exists := m.plugins[name]
	if !exists {
		return nil, fmt.Errorf("plugin %s not found", name)
	}

	return plugin, nil
}

// GetPluginInfo 获取插件信息
func (m *Manager) GetPluginInfo(name string) (*Info, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	info, exists := m.infos[name]
	if !exists {
		return nil, fmt.Errorf("plugin %s not found", name)
	}

	return info, nil
}

// ListPlugins 列出所有插件的信息
func (m *Manager) ListPlugins() map[string]*Info {
	m.mu.RLock()
	defer m.mu.RUnlock()

	plugins := make(map[string]*Info, len(m.infos))
	for name, info := range m.infos {
		plugins[name] = info
	}

	return plugins
}

// ListPluginNames 列出所有插件名称
func (m *Manager) ListPluginNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.plugins))
	for name := range m.plugins {
		names = append(names, name)
	}
	return names
}

// Count 返回已注册插件的数量
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.plugins)
}

// SafeExecute 执行插件操作
func (m *Manager) SafeExecute(
	pluginName string,
	action string,
	params json.RawMessage,
	opts json.RawMessage,
) (result json.RawMessage, err error) {
	plugin, err := m.GetPlugin(pluginName)
	if err != nil {
		return nil, err
	}

	// 捕获 panic
	defer func() {
		if r := recover(); r != nil {
			log.Errorw("plugin panic recovered",
				"plugin", pluginName,
				"action", action,
				"panic", r,
			)
			err = fmt.Errorf("plugin %s panicked during execution of action %s: %v", pluginName, action, r)
			result = nil
		}
	}()

	result, err = plugin.Execute(action, params, opts)
	return result, err
}

// Clear Close 关闭管理器
func (m *Manager) Clear() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 清理所有插件
	for name, plugin := range m.plugins {
		if err := plugin.Cleanup(); err != nil {
			log.Warnw("cleanup plugin failed", "plugin", name, "error", err)
		}
	}

	// 清空映射
	m.plugins = make(map[string]Plugin)
	m.configs = make(map[string]*RuntimePluginConfig)
	m.infos = make(map[string]*Info)

	log.Info("plugin manager closed")
	return nil
}
