// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package model

import "time"

// OrganizationInput 仅包含允许更新的组织元数据
// 创建不接受组织ID、所有者、状态、计划或权限字段
type OrganizationInput struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	Website     string `json:"website"`
	Email       string `json:"email"`
	Version     uint64 `json:"version"`
}

// OrganizationDetail 返回真实组织及实时权限，不根据授权作用域推断成员关系
type OrganizationDetail struct {
	OrgID            string    `json:"orgId"`
	Name             string    `json:"name"`
	DisplayName      string    `json:"displayName"`
	Description      string    `json:"description"`
	Website          string    `json:"website"`
	Email            string    `json:"email"`
	OwnerUserID      string    `json:"ownerUserId"`
	Status           int       `json:"status"`
	IsEnabled        int       `json:"isEnabled"`
	Version          uint64    `json:"version"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
	IsMember         bool      `json:"isMember"`
	CanUpdate        bool      `json:"canUpdate"`
	CanManageMembers bool      `json:"canManageMembers"`
	CanArchive       bool      `json:"canArchive"`
}

// OrganizationList 返回过滤后的分页组织列表
type OrganizationList struct {
	List      []OrganizationDetail `json:"list"`
	Total     int                  `json:"total"`
	Page      int                  `json:"page"`
	PageSize  int                  `json:"pageSize"`
	CanCreate bool                 `json:"canCreate"`
}

// ScopedMemberInput 描述显式成员关联和内置角色变更
type ScopedMemberInput struct {
	UserID  string `json:"userId"`
	RoleID  string `json:"roleId"`
	Version uint64 `json:"version"`
}

// ScopedMemberDetail 只展示此成员流程管理的角色，其余授权独立保留
type ScopedMemberDetail struct {
	OrgID            string `json:"orgId,omitempty"`
	ProjectID        string `json:"projectId,omitempty"`
	UserID           string `json:"userId"`
	Username         string `json:"username"`
	DisplayName      string `json:"displayName"`
	RoleID           string `json:"roleId"`
	Version          uint64 `json:"version"`
	HasOtherAccess   bool   `json:"hasOtherAccess"`
	IsProtectedOwner bool   `json:"isProtectedOwner"`
}

// ScopedMemberList 返回成员及变更能力
type ScopedMemberList struct {
	AssignableRoleIDs []string             `json:"assignableRoleIds"`
	List              []ScopedMemberDetail `json:"list"`
	Total             int                  `json:"total"`
	CanManageMembers  bool                 `json:"canManageMembers"`
	CanAssignOwner    bool                 `json:"canAssignOwner"`
}
