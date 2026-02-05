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

// Organization 组织表
type Organization struct {
	BaseModel
	OrgId       string         `gorm:"column:org_id" json:"orgId"`                // 组织唯一标识
	Name        string         `gorm:"column:name" json:"name"`                   // 组织名称
	DisplayName string         `gorm:"column:display_name" json:"displayName"`    // 组织显示名称
	Description string         `gorm:"column:description" json:"description"`     // 组织描述
	Logo        string         `gorm:"column:logo" json:"logo"`                   // 组织Logo URL
	Website     string         `gorm:"column:website" json:"website"`             // 组织官网
	Email       string         `gorm:"column:email" json:"email"`                 // 组织联系邮箱
	Phone       string         `gorm:"column:phone" json:"phone"`                 // 组织联系电话
	Address     string         `gorm:"column:address" json:"address"`             // 组织地址
	Settings    datatypes.JSON `gorm:"column:settings;type:json" json:"settings"` // 组织设置
	Plan        string         `gorm:"column:plan" json:"plan"`                   // 订阅计划(free/pro/enterprise)
	Status      int            `gorm:"column:status" json:"status"`               // 状态: 0-未激活, 1-正常, 2-冻结, 3-已删除
	OwnerUserId string         `gorm:"column:owner_user_id" json:"ownerUserId"`   // 组织所有者用户ID
	IsEnabled   int            `gorm:"column:is_enabled" json:"isEnabled"`        // 0: disabled, 1: enabled

	// 统计字段
	TotalMembers  int `gorm:"column:total_members" json:"totalMembers"`   // 成员总数
	TotalTeams    int `gorm:"column:total_teams" json:"totalTeams"`       // 团队总数
	TotalProjects int `gorm:"column:total_projects" json:"totalProjects"` // 项目总数
}

func (Organization) TableName() string {
	return "t_organization"
}

// OrganizationSettings 组织设置结构
type OrganizationSettings struct {
	AllowPublicProjects bool     `json:"allow_public_projects"` // 允许公开项目
	RequireTwoFactor    bool     `json:"require_two_factor"`    // 要求双因素认证
	AllowedDomains      []string `json:"allowed_domains"`       // 允许的邮箱域名
	MaxMembers          int      `json:"max_members"`           // 最大成员数
	MaxProjects         int      `json:"max_projects"`          // 最大项目数
	MaxTeams            int      `json:"max_teams"`             // 最大团队数
	MaxAgents           int      `json:"max_agents"`            // 最大Agent数
	DefaultVisibility   string   `json:"default_visibility"`    // 默认项目可见性
	EnableSAML          bool     `json:"enable_saml"`           // 启用SAML认证
	EnableLDAP          bool     `json:"enable_ldap"`           // 启用LDAP认证
}

// OrganizationStatus 组织状态枚举
const (
	OrgStatusInactive = 0 // 未激活
	OrgStatusActive   = 1 // 正常
	OrgStatusFrozen   = 2 // 冻结
	OrgStatusDeleted  = 3 // 已删除
)

// OrganizationPlan 订阅计划
const (
	PlanFree       = "free"       // 免费版
	PlanPro        = "pro"        // 专业版
	PlanEnterprise = "enterprise" // 企业版
)
