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

// ProjectMemberRole 项目成员角色（兼容旧版，保留用于快速角色名称引用）
type ProjectMemberRole string

const (
	ProjectRoleOwner      ProjectMemberRole = "owner"      // 所有者(完全控制)
	ProjectRoleMaintainer ProjectMemberRole = "maintainer" // 维护者(管理项目、成员)
	ProjectRoleDeveloper  ProjectMemberRole = "developer"  // 开发者(创建分支、提交代码)
	ProjectRoleReporter   ProjectMemberRole = "reporter"   // 报告者(创建问题、查看)
	ProjectRoleGuest      ProjectMemberRole = "guest"      // 访客(仅查看)
)

// ProjectMember 项目成员表
type ProjectMember struct {
	BaseModel
	ProjectId string `gorm:"column:project_id;not null;index:idx_project_user,unique" json:"projectId"`
	UserId    string `gorm:"column:user_id;not null;index:idx_project_user,unique;index:idx_user" json:"userId"`
	RoleId    string `gorm:"column:role_id;not null;index" json:"roleId"` // 角色ID（引用 t_role 表）
}

func (pm *ProjectMember) TableName() string {
	return "t_project_member"
}
