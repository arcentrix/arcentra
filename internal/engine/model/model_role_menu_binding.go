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

// RoleMenuBinding 角色菜单关联表（定义角色对菜单的访问权限）
// 注意：同一角色在同一资源下对同一菜单只能有一条记录
type RoleMenuBinding struct {
	BaseModel
	RoleMenuId   string `gorm:"column:role_menu_id;not null;uniqueIndex" json:"roleMenuId"` // 关联唯一标识
	RoleId       string `gorm:"column:role_id;not null;index" json:"roleId"`                // 角色ID（引用 t_role 表）
	MenuId       string `gorm:"column:menu_id;not null;index" json:"menuId"`                // 菜单ID（引用 t_menu 表）
	ResourceId   string `gorm:"column:resource_id;index" json:"resourceId"`                 // 资源ID（组织ID/团队ID/项目ID，平台级为空）
	IsVisible    int    `gorm:"column:is_visible;default:1" json:"isVisible"`               // 是否可见：0-隐藏，1-显示
	IsAccessible int    `gorm:"column:is_accessible;default:1" json:"isAccessible"`         // 是否可访问：0-不可访问，1-可访问
}

func (RoleMenuBinding) TableName() string {
	return "t_role_menu_binding"
}

// 角色菜单访问权限常量
const (
	RoleMenuAccessible   = 1 // 可访问
	RoleMenuInaccessible = 0 // 不可访问
)
