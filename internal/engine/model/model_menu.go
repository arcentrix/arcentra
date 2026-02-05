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

// Menu 菜单表
type Menu struct {
	BaseModel
	MenuId      string `gorm:"column:menu_id;not null;uniqueIndex" json:"menuId"` // 菜单唯一标识
	ParentId    string `gorm:"column:parent_id;index" json:"parentId"`            // 父菜单ID（为空表示顶级菜单）
	Name        string `gorm:"column:name;not null" json:"name"`                  // 菜单名称
	Path        string `gorm:"column:path" json:"path"`                           // 菜单路径（路由路径）
	Component   string `gorm:"column:component" json:"component"`                 // 组件路径（前端组件）
	Icon        string `gorm:"column:icon" json:"icon"`                           // 图标（图标名称或URL）
	Order       int    `gorm:"column:order;default:0" json:"order"`               // 排序（数值越小越靠前）
	IsVisible   int    `gorm:"column:is_visible;default:1" json:"isVisible"`      // 是否可见：0-隐藏，1-显示
	IsEnabled   int    `gorm:"column:is_enabled;default:1" json:"isEnabled"`      // 是否启用：0-禁用，1-启用
	Description string `gorm:"column:description" json:"description"`             // 菜单描述
	Meta        string `gorm:"column:meta;type:text" json:"meta"`                 // 扩展元数据（JSON格式）
}

func (Menu) TableName() string {
	return "t_menu"
}

// 菜单可见性常量
const (
	MenuVisible   = 1 // 可见
	MenuInvisible = 0 // 不可见
)

// 菜单启用状态常量
const (
	MenuEnabled  = 1 // 启用
	MenuDisabled = 0 // 禁用
)
