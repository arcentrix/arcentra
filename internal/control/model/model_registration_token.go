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

import "time"

// RegistrationToken 注册令牌 — Agent 动态注册时使用的共享令牌。
// 对应数据库表 t_registration_token。
// 明文令牌仅在创建时返回一次，数据库中仅存储 bcrypt 哈希。
type RegistrationToken struct {
	BaseModel
	TokenHash   string     `gorm:"column:token_hash" json:"-"`            // bcrypt 哈希后的令牌值，禁止序列化到 JSON
	Description string     `gorm:"column:description" json:"description"` // 令牌描述（用途/环境等）
	CreatedBy   string     `gorm:"column:created_by" json:"createdBy"`    // 创建者用户名（用于前端展示）
	ExpiresAt   *time.Time `gorm:"column:expires_at" json:"expiresAt"`    // 过期时间，NULL=永不过期
	MaxUses     int        `gorm:"column:max_uses" json:"maxUses"`        // 最大使用次数，0=无限制
	UseCount    int        `gorm:"column:use_count" json:"useCount"`      // 已使用次数
	IsActive    int        `gorm:"column:is_active" json:"isActive"`      // 0=已吊销 1=启用中
	CreatedAt   time.Time  `gorm:"column:created_at" json:"createdAt"`    // 创建时间
	UpdatedAt   time.Time  `gorm:"column:updated_at" json:"updatedAt"`    // 更新时间
}

func (t *RegistrationToken) TableName() string {
	return "registration_token"
}

// CreateRegistrationTokenReq 创建注册令牌请求
type CreateRegistrationTokenReq struct {
	Description string `json:"description"`         // 令牌描述（必填）
	CreatedBy   string `json:"createdBy,omitempty"` // 创建者，不填时由后端自动填充当前登录用户
	ExpiresAt   string `json:"expiresAt,omitempty"` // 过期时间（ISO 8601），空=永不过期
	MaxUses     int    `json:"maxUses,omitempty"`   // 最大使用次数，0=无限制
}

// CreateRegistrationTokenResp 创建注册令牌响应。
// 明文 Token 仅在此次响应中返回，后续无法再次获取。
type CreateRegistrationTokenResp struct {
	ID          uint64  `json:"id"`          // 数据库主键
	Token       string  `json:"token"`       // 明文令牌（art_ 前缀，仅展示一次）
	Description string  `json:"description"` // 描述
	CreatedBy   string  `json:"createdBy"`   // 创建者
	ExpiresAt   *string `json:"expiresAt"`   // 过期时间
	MaxUses     int     `json:"maxUses"`     // 最大使用次数
}

// ListRegistrationTokenResp 令牌列表项（永不返回明文字符串）。
type ListRegistrationTokenResp struct {
	ID          uint64  `json:"id"`          // 主键
	Description string  `json:"description"` // 描述
	CreatedBy   string  `json:"createdBy"`   // 创建者
	ExpiresAt   *string `json:"expiresAt"`   // 过期时间
	MaxUses     int     `json:"maxUses"`     // 最大使用次数
	UseCount    int     `json:"useCount"`    // 已使用次数
	IsActive    int     `json:"isActive"`    // 0=已吊销 1=启用
	CreatedAt   string  `json:"createdAt"`   // 创建时间
}
