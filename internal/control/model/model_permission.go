// Copyright 2026 Arcentra Authors.
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

// Permission 权限字典表
type Permission struct {
	BaseModel
	PermissionID string `gorm:"column:permission_id;not null;uniqueIndex" json:"permissionId"`
	ResourceType string `gorm:"column:resource_type;not null;index" json:"resourceType"`
	Action       string `gorm:"column:action;not null" json:"action"`
	ScopeType    string `gorm:"column:scope_type;not null;default:'project';index" json:"scopeType"`
	Description  string `gorm:"column:description;not null;default:''" json:"description"`
	IsSystem     int    `gorm:"column:is_system;not null;default:1" json:"isSystem"`
}

// TableName 返回数据库表名
func (Permission) TableName() string {
	return "permission"
}

// RolePermissionBinding 角色与权限绑定表
type RolePermissionBinding struct {
	RoleID       string `gorm:"column:role_id;primaryKey" json:"roleId"`
	PermissionID string `gorm:"column:permission_id;primaryKey;index" json:"permissionId"`
}

// TableName 返回数据库表名
func (RolePermissionBinding) TableName() string {
	return "role_permission_binding"
}
