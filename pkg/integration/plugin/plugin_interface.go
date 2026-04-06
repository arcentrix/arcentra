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
)

// Type 插件类型枚举
type Type int32

const (
	// TypeUnspecified 未指定的插件类型
	TypeUnspecified Type = 0
	// TypeSource Source插件类型
	TypeSource Type = 1
	// TypeBuild Build插件类型
	TypeBuild Type = 2
	// TypeTest Test插件类型
	TypeTest Type = 3
	// TypeDeploy Deploy插件类型
	TypeDeploy Type = 4
	// TypeSecurity Security插件类型
	TypeSecurity Type = 5
	// TypeNotify Notify插件类型
	TypeNotify Type = 6
	// TypeApproval Approval插件类型
	TypeApproval Type = 7
	// TypeStorage Storage插件类型
	TypeStorage Type = 8
	// TypeAnalytics Analytics插件类型
	TypeAnalytics Type = 9
	// TypeIntegration Integration插件类型
	TypeIntegration Type = 10
	// TypeCustom Custom插件类型
	TypeCustom Type = 11
)

// Plugin 是插件必须实现的接口
// 所有插件都需要实现这个接口才能被注册和使用
type Plugin interface {
	// Name 返回插件名称
	Name() string

	// Description 返回插件描述
	Description() string

	// Version 返回插件版本
	Version() string

	// Type 返回插件类型
	Type() Type

	// Author 返回插件作者
	Author() string

	// Repository 返回插件仓库地址
	Repository() string

	// Init 初始化插件，config 是插件的配置信息（JSON格式）
	Init(config json.RawMessage) error

	// Execute 执行插件操作
	// action: 操作名称（如 "send", "build", "clone"）
	// params: 操作参数（JSON格式）
	// opts: 可选参数（JSON格式，如超时、工作目录、环境变量等）
	// 返回: 操作结果（JSON格式）和错误
	Execute(action string, params json.RawMessage, opts json.RawMessage) (json.RawMessage, error)

	// Cleanup 清理插件资源（可选实现）
	Cleanup() error
}

// Info 包含插件的元信息
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Type        Type   `json:"type"`
	Author      string `json:"author,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
}

// RuntimePluginConfig 表示插件的运行时配置
type RuntimePluginConfig struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Type        string            `json:"type"`
	Config      json.RawMessage   `json:"config"`
	Environment map[string]string `json:"environment,omitempty"`
	TaskID      string            `json:"task_id,omitempty"`
}
