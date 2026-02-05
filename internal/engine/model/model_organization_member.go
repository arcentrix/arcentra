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

// OrganizationMember 组织成员表
type OrganizationMember struct {
	BaseModel
	OrgId     string `gorm:"column:org_id" json:"orgId"`         // 组织ID
	UserId    string `gorm:"column:user_id" json:"userId"`       // 用户ID
	RoleId    string `gorm:"column:role_id;index" json:"roleId"` // 角色ID（引用 t_role 表）
	Username  string `gorm:"column:username" json:"username"`    // 用户名(冗余)
	Email     string `gorm:"column:email" json:"email"`          // 邮箱(冗余)
	InvitedBy string `gorm:"column:invited_by" json:"invitedBy"` // 邀请人用户ID
	Status    int    `gorm:"column:status" json:"status"`        // 状态: 0-待接受, 1-正常, 2-禁用
}

func (OrganizationMember) TableName() string {
	return "t_organization_member"
}

// OrganizationMemberRole 组织成员角色
const (
	OrgRoleOwner  = "owner"  // 所有者(最高权限)
	OrgRoleAdmin  = "admin"  // 管理员(管理组织、成员、团队)
	OrgRoleMember = "member" // 普通成员
)

// OrganizationMemberStatus 组织成员状态
const (
	OrgMemberStatusPending  = 0 // 待接受
	OrgMemberStatusActive   = 1 // 正常
	OrgMemberStatusDisabled = 2 // 禁用
)
