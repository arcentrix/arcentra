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

// Menu 菜单表
type Menu struct {
	BaseModel
	MenuID       string `gorm:"column:menu_id;not null;uniqueIndex" json:"menuId"`
	ParentID     string `gorm:"column:parent_id;index" json:"parentId"`
	Name         string `gorm:"column:name;not null;uniqueIndex" json:"name"`
	Title        string `gorm:"column:title;not null;default:''" json:"title"`
	Path         string `gorm:"column:path" json:"path"`
	Component    string `gorm:"column:component" json:"component"`
	Redirect     string `gorm:"column:redirect" json:"redirect"`
	IsLayout     int    `gorm:"column:is_layout;default:0" json:"isLayout"`
	IsIndex      int    `gorm:"column:is_index;default:0" json:"isIndex"`
	Icon         string `gorm:"column:icon" json:"icon"`
	Order        int    `gorm:"column:order;default:0" json:"order"`
	Meta         string `gorm:"column:meta_json;type:json" json:"meta"`
	PermissionID string `gorm:"column:permission_id;index" json:"permissionId"`
	ScopeType    string `gorm:"column:scope_type;not null;default:'platform';index" json:"scopeType"`
	IsVisible    int    `gorm:"column:is_visible;default:1" json:"isVisible"`
	IsEnabled    int    `gorm:"column:is_enabled;default:1" json:"isEnabled"`
	Description  string `gorm:"column:description" json:"description"`
}

// TableName 返回数据库表名
func (Menu) TableName() string {
	return "menu"
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
