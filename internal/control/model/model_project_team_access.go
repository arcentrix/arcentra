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

// ProjectTeamAccess 项目团队访问权限表
type ProjectTeamAccess struct {
	BaseModel
	ProjectId   string `gorm:"column:project_id;not null;index:idx_project_team,unique" json:"projectId"`
	TeamId      string `gorm:"column:team_id;not null;index:idx_project_team,unique;index:idx_team" json:"teamId"`
	AccessLevel string `gorm:"column:access_level;not null;type:varchar(32)" json:"accessLevel"` // read/write/admin
}

func (ProjectTeamAccess) TableName() string {
	return "t_project_team_access"
}

// ProjectTeamAccessLevel 项目团队访问权限级别
const (
	AccessLevelRead  = "read"  // 只读(查看代码和构建)
	AccessLevelWrite = "write" // 读写(提交代码、触发构建)
	AccessLevelAdmin = "admin" // 管理员(完全控制项目)
)
