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

// RoleGrant 表示角色授权记录
type RoleGrant struct {
	BaseModel
	GrantID     string     `gorm:"column:grant_id;not null;uniqueIndex" json:"grantId"`
	SubjectType string     `gorm:"column:subject_type;not null;index:idx_subject_scope,priority:1;index:idx_subject,priority:1" json:"subjectType"` //nolint:lll
	SubjectID   string     `gorm:"column:subject_id;not null;index:idx_subject_scope,priority:2;index:idx_subject,priority:2" json:"subjectId"`
	RoleID      string     `gorm:"column:role_id;not null;index:idx_subject_scope,priority:3;index" json:"roleId"`
	ScopeType   string     `gorm:"column:scope_type;not null;index:idx_subject_scope,priority:4;index:idx_scope,priority:1" json:"scopeType"`
	ScopeID     string     `gorm:"column:scope_id;not null;index:idx_subject_scope,priority:5;index:idx_scope,priority:2" json:"scopeId"`
	GrantedBy   string     `gorm:"column:granted_by;not null;default:''" json:"grantedBy"`
	ExpiresAt   *time.Time `gorm:"column:expires_at;index" json:"expiresAt"`
	IsEnabled   int        `gorm:"column:is_enabled;not null;default:1" json:"isEnabled"`
}

// TableName 返回数据库表名
func (RoleGrant) TableName() string {
	return "role_grant"
}
