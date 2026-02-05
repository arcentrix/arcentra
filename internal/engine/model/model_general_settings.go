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

// GeneralSettings 通用配置表
type GeneralSettings struct {
	BaseModel
	SettingsId  string         `gorm:"column:settings_id;type:varchar(128);not null" json:"settingsId"`   // 设置ID
	Category    string         `gorm:"column:category;type:varchar(64);not null" json:"category"`         // 配置类别，如 system、security、pipeline
	Name        string         `gorm:"column:name;type:varchar(128);not null" json:"name"`                // 配置名称（业务语义字段）
	DisplayName string         `gorm:"column:display_name;type:varchar(128);not null" json:"displayName"` // 展示名，如 JWT 密钥
	Data        datatypes.JSON `gorm:"column:data;type:json;not null" json:"data"`                        // 配置内容（结构化 JSON）
	Schema      datatypes.JSON `gorm:"column:schema;type:json" json:"schema"`                             // 配置结构定义（JSON Schema）
	Description string         `gorm:"column:description;type:varchar(255)" json:"description"`           // 配置说明
}

func (GeneralSettings) TableName() string {
	return "t_general_settings"
}
