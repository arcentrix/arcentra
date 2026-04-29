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

package model

// Role 角色表（系统内置与组织自定义统一模型）
type Role struct {
	BaseModel
	RoleID      string `gorm:"column:role_id;not null;uniqueIndex" json:"roleId"`
	Name        string `gorm:"column:name;not null" json:"name"`
	DisplayName string `gorm:"column:display_name;not null;default:''" json:"displayName"`
	Description string `gorm:"column:description;not null;default:''" json:"description"`
	ScopeType   string `gorm:"column:scope_type;not null;default:'platform';index" json:"scopeType"`
	IsSystem    int    `gorm:"column:is_system;not null;default:0" json:"isSystem"`
	OrgID       string `gorm:"column:org_id;index" json:"orgId"`
	IsEnabled   int    `gorm:"column:is_enabled;not null;default:1" json:"isEnabled"`
}

// TableName 返回数据库表名
func (r *Role) TableName() string {
	return "role"
}

// CreateRoleReq 表示创建角色请求
type CreateRoleReq struct {
	RoleID      string `json:"roleId" binding:"required"`
	Name        string `json:"name" binding:"required"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	ScopeType   string `json:"scopeType"`
	IsSystem    *int   `json:"isSystem"`
	OrgID       string `json:"orgId"`
	IsEnabled   *int   `json:"isEnabled"`
}

// UpdateRoleReq 表示更新角色请求
type UpdateRoleReq struct {
	Name        *string `json:"name,omitempty"`
	DisplayName *string `json:"displayName,omitempty"`
	Description *string `json:"description,omitempty"`
	ScopeType   *string `json:"scopeType,omitempty"`
	IsSystem    *int    `json:"isSystem,omitempty"`
	OrgID       *string `json:"orgId,omitempty"`
	IsEnabled   *int    `json:"isEnabled,omitempty"`
}
