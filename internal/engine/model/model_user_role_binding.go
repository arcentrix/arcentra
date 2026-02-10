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

import "time"

// UserRoleBinding 用户角色绑定表（支持多层级权限管理）
type UserRoleBinding struct {
	ID         int       `gorm:"column:id;primaryKey;autoIncrement" json:"-"`
	BindingId  string    `gorm:"column:binding_id;not null;uniqueIndex" json:"bindingId"` // 绑定唯一标识
	UserId     string    `gorm:"column:user_id;not null;index" json:"userId"`             // 用户ID
	RoleId     string    `gorm:"column:role_id;not null;index" json:"roleId"`             // 角色ID
	GrantedBy  *string   `gorm:"column:granted_by" json:"grantedBy"`                      // 授权人ID（可为空）
	CreateTime time.Time `gorm:"column:create_time;autoCreateTime" json:"createTime"`     // 创建时间
	UpdateTime time.Time `gorm:"column:update_time;autoUpdateTime" json:"updateTime"`     // 更新时间
}

func (UserRoleBinding) TableName() string {
	return "t_user_role_binding"
}
