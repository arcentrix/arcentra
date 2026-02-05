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

// Plugin 插件模型
type Plugin struct {
	BaseModel
	PluginId    string `gorm:"column:plugin_id;not null;size:64" json:"pluginId"`
	Name        string `gorm:"column:name;not null;size:128" json:"name"`
	Version     string `gorm:"column:version;not null;size:32" json:"version"`
	Description string `gorm:"column:description;type:text" json:"description"`
	Author      string `gorm:"column:author;size:128" json:"author"`
	PluginType  string `gorm:"column:plugin_type;not null;size:32" json:"pluginType"`
	Repository  string `gorm:"column:repository;size:512" json:"repository"`
}

func (p *Plugin) TableName() string {
	return "t_plugin"
}
