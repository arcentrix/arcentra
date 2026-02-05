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

// Role 角色表（支持自定义角色）
type Role struct {
	BaseModel
	RoleId      string `gorm:"column:role_id;not null;uniqueIndex" json:"roleId"`
	Name        string `gorm:"column:name;not null" json:"name"`                      // 角色名称
	DisplayName string `gorm:"column:display_name" json:"displayName"`                // 显示名称
	Description string `gorm:"column:description" json:"description"`                 // 角色描述
	IsEnabled   int    `gorm:"column:is_enabled;not null;default:1" json:"isEnabled"` // 0: disabled, 1: enabled
}

func (r *Role) TableName() string {
	return "t_role"
}

// 内置组织角色 ID
const (
	Owner  = "owner"  // 组织所有者
	Admin  = "admin"  // 组织管理员
	Member = "member" // 组织成员
)

// CreateRoleReq request for creating role
type CreateRoleReq struct {
	RoleId      string `json:"roleId" binding:"required"`
	Name        string `json:"name" binding:"required"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	IsEnabled   *int   `json:"isEnabled"`
}

// UpdateRoleReq request for updating role
type UpdateRoleReq struct {
	Name        *string `json:"name,omitempty"`
	DisplayName *string `json:"displayName,omitempty"`
	Description *string `json:"description,omitempty"`
	IsEnabled   *int    `json:"isEnabled,omitempty"`
}
