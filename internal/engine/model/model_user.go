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

import (
	"time"
)

type User struct {
	BaseModel
	UserId       string `gorm:"column:user_id" json:"userId"`
	Username     string `gorm:"column:username" json:"username"`
	FullName     string `gorm:"column:full_name" json:"fullName"`
	Password     string `gorm:"column:password" json:"password"`
	Avatar       string `gorm:"column:avatar" json:"avatar"`
	Email        string `gorm:"column:email" json:"email"`
	Phone        string `gorm:"column:phone" json:"phone"`
	IsEnabled    int    `gorm:"column:is_enabled" json:"isEnabled"`                 // 0: disabled, 1: enabled
	IsSuperAdmin int    `gorm:"column:is_superadmin;default:0" json:"isSuperAdmin"` // 0: normal user, 1: super admin
}

func (User) TableName() string {
	return "t_user"
}

type Register struct {
	UserId    string    `json:"userId"`
	Username  string    `json:"username"`
	FullName  string    `gorm:"column:full_name" json:"fullName"`
	Email     string    `json:"email"`
	Avatar    string    `gorm:"column:avatar" json:"avatar"`
	Password  string    `json:"password"`
	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
}

type Login struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginResp struct {
	UserInfo UserInfo          `json:"userInfo"`
	Token    map[string]string `json:"token"`
	Role     []RoleDTO         `json:"role"`   // 用户角色信息列表
	Routes   []string          `json:"routes"` // 用户可访问的路由列表
	ExpireAt int64             `json:"-"`
	CreateAt int64             `json:"-"`
}

// RoleDTO 角色数据传输对象
type RoleDTO struct {
	RoleId      string `json:"roleId"`      // 角色ID
	Name        string `json:"name"`        // 角色名称
	DisplayName string `json:"displayName"` // 显示名称
	Description string `json:"description"` // 角色描述
}

// MenuDTO 菜单数据传输对象
type MenuDTO struct {
	MenuId      string    `json:"menuId"`
	ParentId    string    `json:"parentId"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Component   string    `json:"component"`
	Icon        string    `json:"icon"`
	Order       int       `json:"order"`
	IsVisible   bool      `json:"isVisible"`
	IsEnabled   bool      `json:"isEnabled"`
	Description string    `json:"description"`
	Children    []MenuDTO `json:"children,omitempty"` // 子菜单列表
}

type UserInfo struct {
	UserId    string `json:"userId"`
	Username  string `json:"username"`
	FullName  string `json:"fullName"`
	Avatar    string `json:"avatar"`
	Email     string `json:"email"`
	Phone     string `json:"phone"`
	IsEnabled int    `json:"isEnabled"`
}

type AddUserReq struct {
	UserId    string    `json:"userId"`
	Username  string    `json:"username"`
	FullName  string    `gorm:"column:full_name" json:"fullName"`
	Password  string    `json:"password"`
	Avatar    string    `json:"avatar"`
	Email     string    `json:"email"`
	Phone     string    `json:"phone"`
	IsEnabled int       `gorm:"column:is_enabled" json:"isEnabled"`
	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
}

// ResetPasswordReq reset password request (for forgot password scenario)
type ResetPasswordReq struct {
	NewPassword string `json:"newPassword"` // new password (base64 encoded)
}

// UpdateUserReq request for updating user
type UpdateUserReq struct {
	FullName  *string `json:"fullName,omitempty"`
	Avatar    *string `json:"avatar,omitempty"`
	Email     *string `json:"email,omitempty"`
	Phone     *string `json:"phone,omitempty"`
	IsEnabled *int    `json:"isEnabled,omitempty"`
}
