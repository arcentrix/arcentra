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
	"fmt"
	"sync"
)

// 全局插件注册表
var reg = &registry{
	plugins: make(map[string]Plugin),
}

// registry 是插件注册表
type registry struct {
	mu      sync.RWMutex
	plugins map[string]Plugin
}

// Register 注册一个插件
// 插件应该在init函数中调用此函数进行注册
// 如果注册失败（插件为nil、名称为空或同名插件已存在），会panic
func Register(plugin Plugin) {
	if plugin == nil {
		panic("plugin cannot be nil")
	}

	name := plugin.Name()
	if name == "" {
		panic("plugin name cannot be empty")
	}

	reg.mu.Lock()
	defer reg.mu.Unlock()

	if _, exists := reg.plugins[name]; exists {
		panic(fmt.Sprintf("plugin %s already registered", name))
	}

	reg.plugins[name] = plugin
}

// ListPlugins 列出所有已注册的插件实例
// 供 Manager 从全局注册表加载插件使用
func ListPlugins() map[string]Plugin {
	reg.mu.RLock()
	defer reg.mu.RUnlock()

	plugins := make(map[string]Plugin, len(reg.plugins))
	for name, plugin := range reg.plugins {
		plugins[name] = plugin
	}
	return plugins
}
