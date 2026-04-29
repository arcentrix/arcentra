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

import "gorm.io/datatypes"

const (
	// AuditResultSuccess 表示执行成功
	AuditResultSuccess = "success"
	// AuditResultDenied 表示权限拒绝
	AuditResultDenied = "denied"
	// AuditResultError 表示执行错误
	AuditResultError = "error"
)

// AuditLog 表示审计日志
type AuditLog struct {
	BaseModel
	LogID        string         `gorm:"column:log_id;not null;uniqueIndex" json:"logId"`
	ActorUserID  string         `gorm:"column:actor_user_id;not null;default:'';index" json:"actorUserId"`
	ActorIP      string         `gorm:"column:actor_ip;not null;default:''" json:"actorIp"`
	Action       string         `gorm:"column:action;not null;index" json:"action"`
	ResourceType string         `gorm:"column:resource_type;not null;default:'';index:idx_resource,priority:1" json:"resourceType"`
	ResourceID   string         `gorm:"column:resource_id;not null;default:'';index:idx_resource,priority:2" json:"resourceId"`
	ScopeType    string         `gorm:"column:scope_type;not null;default:''" json:"scopeType"`
	ScopeID      string         `gorm:"column:scope_id;not null;default:''" json:"scopeId"`
	Result       string         `gorm:"column:result;not null" json:"result"`
	Reason       string         `gorm:"column:reason;not null;default:''" json:"reason"`
	Payload      datatypes.JSON `gorm:"column:payload;type:json" json:"payload"`
}

// TableName 返回数据库表名
func (AuditLog) TableName() string {
	return "audit_log"
}
