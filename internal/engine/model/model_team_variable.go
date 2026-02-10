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

// TeamVariable 团队变量表
type TeamVariable struct {
	BaseModel
	VariableId  string `gorm:"column:variable_id" json:"variableId"`  // 变量唯一标识
	TeamId      string `gorm:"column:team_id" json:"teamId"`          // 团队ID
	Key         string `gorm:"column:key" json:"key"`                 // 变量键
	Value       string `gorm:"column:value" json:"value"`             // 变量值(敏感信息加密存储)
	Type        string `gorm:"column:type" json:"type"`               // 类型(env/secret/file)
	Protected   int    `gorm:"column:protected" json:"protected"`     // 是否保护(仅在保护分支可用): 0-否, 1-是
	Masked      int    `gorm:"column:masked" json:"masked"`           // 是否掩码(日志中隐藏): 0-否, 1-是
	Description string `gorm:"column:description" json:"description"` // 描述
}

func (TeamVariable) TableName() string {
	return "t_team_variable"
}

// TeamVariableType 团队变量类型枚举
const (
	TeamVariableTypeEnv    = "env"    // 环境变量
	TeamVariableTypeSecret = "secret" // 密钥
	TeamVariableTypeFile   = "file"   // 文件
)
