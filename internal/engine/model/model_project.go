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

package model

import (
	"gorm.io/datatypes"
)

// Project 项目表
type Project struct {
	BaseModel
	ProjectId     string         `gorm:"column:project_id" json:"projectId"`               // 项目唯一标识
	OrgId         string         `gorm:"column:org_id" json:"orgId"`                       // 所属组织ID
	Name          string         `gorm:"column:name" json:"name"`                          // 项目名称
	DisplayName   string         `gorm:"column:display_name" json:"displayName"`           // 项目显示名称
	Namespace     string         `gorm:"column:namespace" json:"namespace"`                // 项目命名空间(org_name/project_name)
	Description   string         `gorm:"column:description" json:"description"`            // 项目描述
	RepoUrl       string         `gorm:"column:repo_url" json:"repoUrl"`                   // 代码仓库URL
	RepoType      string         `gorm:"column:repo_type" json:"repoType"`                 // 仓库类型(git/svn/gitlab/github/gitee)
	DefaultBranch string         `gorm:"column:default_branch" json:"defaultBranch"`       // 默认分支
	AuthType      int            `gorm:"column:auth_type" json:"authType"`                 // 认证类型: 0-无, 1-用户名密码, 2-Token, 3-SSH密钥
	Credential    string         `gorm:"column:credential" json:"credential"`              // 认证凭证(加密存储)
	TriggerMode   int            `gorm:"column:trigger_mode" json:"triggerMode"`           // 触发模式: 1-手动, 2-Webhook, 4-定时, 8-Push触发(可组合)
	WebhookSecret string         `gorm:"column:webhook_secret" json:"webhookSecret"`       // Webhook密钥
	CronExpr      string         `gorm:"column:cron_expr" json:"cronExpr"`                 // 定时任务Cron表达式
	BuildConfig   datatypes.JSON `gorm:"column:build_config;type:json" json:"buildConfig"` // 构建配置
	EnvVars       datatypes.JSON `gorm:"column:env_vars;type:json" json:"envVars"`         // 环境变量
	Settings      datatypes.JSON `gorm:"column:settings;type:json" json:"settings"`        // 项目设置
	Tags          string         `gorm:"column:tags" json:"tags"`                          // 项目标签(逗号分隔)
	Language      string         `gorm:"column:language" json:"language"`                  // 主要编程语言
	Framework     string         `gorm:"column:framework" json:"framework"`                // 使用的框架
	Status        int            `gorm:"column:status" json:"status"`                      // 项目状态: 0-未激活, 1-正常, 2-归档, 3-禁用
	Visibility    int            `gorm:"column:visibility" json:"visibility"`              // 可见性: 0-私有, 1-内部, 2-公开
	AccessLevel   string         `gorm:"column:access_level" json:"accessLevel"`           // 默认访问级别(owner/team/org)
	CreatedBy     string         `gorm:"column:created_by" json:"createdBy"`               // 创建者用户ID
	IsEnabled     int            `gorm:"column:is_enabled" json:"isEnabled"`               // 0: disabled, 1: enabled
	Icon          string         `gorm:"column:icon" json:"icon"`                          // 项目图标URL
	Homepage      string         `gorm:"column:homepage" json:"homepage"`                  // 项目主页

	// 统计字段
	TotalPipelines int `gorm:"column:total_pipelines" json:"totalPipelines"` // 流水线总数
	TotalBuilds    int `gorm:"column:total_builds" json:"totalBuilds"`       // 构建总次数
	SuccessBuilds  int `gorm:"column:success_builds" json:"successBuilds"`   // 成功构建次数
	FailedBuilds   int `gorm:"column:failed_builds" json:"failedBuilds"`     // 失败构建次数
}

func (Project) TableName() string {
	return "t_project"
}

// ProjectSettings 项目设置结构
type ProjectSettings struct {
	AutoCancel      bool     `json:"auto_cancel"`       // 自动取消之前的构建
	Timeout         int      `json:"timeout"`           // 全局超时时间(秒)
	MaxConcurrent   int      `json:"max_concurrent"`    // 最大并发构建数
	RetryCount      int      `json:"retry_count"`       // 默认重试次数
	NotifyOnSuccess bool     `json:"notify_on_success"` // 成功时通知
	NotifyOnFailure bool     `json:"notify_on_failure"` // 失败时通知
	CleanWorkspace  bool     `json:"clean_workspace"`   // 清理工作空间
	AllowedBranches []string `json:"allowed_branches"`  // 允许构建的分支
	IgnoredBranches []string `json:"ignored_branches"`  // 忽略的分支
	AllowedPaths    []string `json:"allowed_paths"`     // 触发构建的文件路径
	IgnoredPaths    []string `json:"ignored_paths"`     // 忽略的文件路径
	BadgeEnabled    bool     `json:"badge_enabled"`     // 启用构建状态徽章
}

// BuildConfig 构建配置结构
type BuildConfig struct {
	Dockerfile     string            `json:"dockerfile"`      // Dockerfile路径
	BuildContext   string            `json:"build_context"`   // 构建上下文路径
	BuildArgs      map[string]string `json:"build_args"`      // 构建参数
	CacheEnabled   bool              `json:"cache_enabled"`   // 启用缓存
	TestEnabled    bool              `json:"test_enabled"`    // 启用测试
	LintEnabled    bool              `json:"lint_enabled"`    // 启用代码检查
	ScanEnabled    bool              `json:"scan_enabled"`    // 启用安全扫描
	ArtifactPaths  []string          `json:"artifact_paths"`  // 产物路径
	ArtifactExpire int               `json:"artifact_expire"` // 产物过期天数
}

// ProjectTriggerMode 触发模式枚举（位掩码）
const (
	TriggerModeManual   = 1 << 0 // 手动触发
	TriggerModeWebhook  = 1 << 1 // Webhook触发
	TriggerModeSchedule = 1 << 2 // 定时触发
	TriggerModePush     = 1 << 3 // Push触发
	TriggerModeMR       = 1 << 4 // MR/PR触发
	TriggerModeTag      = 1 << 5 // Tag触发
)

// ProjectAuthType 认证类型枚举
const (
	AuthTypeNone     = 0 // 无认证（公开仓库）
	AuthTypePassword = 1 // 用户名密码
	AuthTypeToken    = 2 // Token/PAT
	AuthTypeSSHKey   = 3 // SSH密钥
)

// ProjectRepoType 仓库类型枚举
const (
	RepoTypeGit    = "git"    // 通用Git
	RepoTypeGitHub = "github" // GitHub
	RepoTypeGitLab = "gitlab" // GitLab
	RepoTypeGitee  = "gitee"  // Gitee
	RepoTypeSVN    = "svn"    // SVN
)

// ProjectStatus 项目状态枚举
const (
	ProjectStatusInactive = 0 // 未激活
	ProjectStatusActive   = 1 // 正常
	ProjectStatusArchived = 2 // 归档
	ProjectStatusDisabled = 3 // 禁用
)

// ProjectVisibility 项目可见性枚举
const (
	VisibilityPrivate  = 0 // 私有
	VisibilityInternal = 1 // 内部
	VisibilityPublic   = 2 // 公开
)

// ProjectWebhook 项目Webhook表
type ProjectWebhook struct {
	BaseModel
	WebhookId   string         `gorm:"column:webhook_id" json:"webhookId"`    // Webhook唯一标识
	ProjectId   string         `gorm:"column:project_id" json:"projectId"`    // 项目ID
	Name        string         `gorm:"column:name" json:"name"`               // Webhook名称
	Url         string         `gorm:"column:url" json:"url"`                 // Webhook URL
	Secret      string         `gorm:"column:secret" json:"secret"`           // 密钥
	Events      datatypes.JSON `gorm:"column:events;type:json" json:"events"` // 触发事件列表
	IsEnabled   int            `gorm:"column:is_enabled" json:"isEnabled"`    // 是否启用
	Description string         `gorm:"column:description" json:"description"` // 描述
}

func (ProjectWebhook) TableName() string {
	return "t_project_webhook"
}

// ProjectVariable 项目变量表
type ProjectVariable struct {
	BaseModel
	VariableId  string `gorm:"column:variable_id" json:"variableId"`  // 变量唯一标识
	ProjectId   string `gorm:"column:project_id" json:"projectId"`    // 项目ID
	Key         string `gorm:"column:key" json:"key"`                 // 变量键
	Value       string `gorm:"column:value" json:"value"`             // 变量值(可能加密)
	Type        string `gorm:"column:type" json:"type"`               // 类型(env/secret/file)
	Protected   int    `gorm:"column:protected" json:"protected"`     // 是否保护(仅在保护分支可用)
	Masked      int    `gorm:"column:masked" json:"masked"`           // 是否掩码(日志中隐藏)
	Description string `gorm:"column:description" json:"description"` // 描述
}

func (ProjectVariable) TableName() string {
	return "t_project_variable"
}

// ProjectVariableType 项目变量类型
const (
	VariableTypeEnv    = "env"    // 环境变量
	VariableTypeSecret = "secret" // 密钥
	VariableTypeFile   = "file"   // 文件
)

// ProjectAccessLevel 项目访问级别
const (
	AccessLevelOwner = "owner" // 仅所有者
	AccessLevelTeam  = "team"  // 团队成员
	AccessLevelOrg   = "org"   // 组织成员
)

// CreateProjectReq 创建项目请求
type CreateProjectReq struct {
	OrgId         string                 `json:"orgId" validate:"required"`
	Name          string                 `json:"name" validate:"required,min=2,max=128"`
	DisplayName   string                 `json:"displayName"`
	Description   string                 `json:"description"`
	RepoUrl       string                 `json:"repoUrl" validate:"required"`
	RepoType      string                 `json:"repoType" validate:"required"`
	DefaultBranch string                 `json:"defaultBranch"`
	AuthType      int                    `json:"authType"`
	Credential    string                 `json:"credential"`
	TriggerMode   int                    `json:"triggerMode"`
	WebhookSecret string                 `json:"webhookSecret"`
	CronExpr      string                 `json:"cronExpr"`
	BuildConfig   map[string]interface{} `json:"buildConfig"`
	EnvVars       map[string]interface{} `json:"envVars"`
	Settings      map[string]interface{} `json:"settings"`
	Tags          string                 `json:"tags"`
	Language      string                 `json:"language"`
	Framework     string                 `json:"framework"`
	Visibility    int                    `json:"visibility"`
	AccessLevel   string                 `json:"accessLevel"`
	Icon          string                 `json:"icon"`
	Homepage      string                 `json:"homepage"`
}

// UpdateProjectReq 更新项目请求
type UpdateProjectReq struct {
	DisplayName   *string                `json:"displayName,omitempty"`
	Description   *string                `json:"description,omitempty"`
	RepoUrl       *string                `json:"repoUrl,omitempty"`
	DefaultBranch *string                `json:"defaultBranch,omitempty"`
	AuthType      *int                   `json:"authType,omitempty"`
	Credential    *string                `json:"credential,omitempty"`
	TriggerMode   *int                   `json:"triggerMode,omitempty"`
	WebhookSecret *string                `json:"webhookSecret,omitempty"`
	CronExpr      *string                `json:"cronExpr,omitempty"`
	BuildConfig   map[string]interface{} `json:"buildConfig,omitempty"`
	EnvVars       map[string]interface{} `json:"envVars,omitempty"`
	Settings      map[string]interface{} `json:"settings,omitempty"`
	Tags          *string                `json:"tags,omitempty"`
	Language      *string                `json:"language,omitempty"`
	Framework     *string                `json:"framework,omitempty"`
	Status        *int                   `json:"status,omitempty"`
	Visibility    *int                   `json:"visibility,omitempty"`
	AccessLevel   *string                `json:"accessLevel,omitempty"`
	Icon          *string                `json:"icon,omitempty"`
	Homepage      *string                `json:"homepage,omitempty"`
	IsEnabled     *int                   `json:"isEnabled,omitempty"`
}

// ProjectQueryReq 查询项目请求
type ProjectQueryReq struct {
	OrgId      string `json:"orgId" form:"orgId"`
	Name       string `json:"name" form:"name"`
	Language   string `json:"language" form:"language"`
	Status     *int   `json:"status" form:"status"`
	Visibility *int   `json:"visibility" form:"visibility"`
	Tags       string `json:"tags" form:"tags"`
	PageNum    int    `json:"pageNum" form:"pageNum"`
	PageSize   int    `json:"pageSize" form:"pageSize"`
}

// ProjectStatisticsReq 更新项目统计信息请求
type ProjectStatisticsReq struct {
	TotalPipelines *int `json:"totalPipelines,omitempty"`
	TotalBuilds    *int `json:"totalBuilds,omitempty"`
	SuccessBuilds  *int `json:"successBuilds,omitempty"`
	FailedBuilds   *int `json:"failedBuilds,omitempty"`
}
