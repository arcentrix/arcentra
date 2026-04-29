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

// ApprovalPolicy 表示审批策略
type ApprovalPolicy struct {
	BaseModel
	PolicyID       string         `gorm:"column:policy_id;not null;uniqueIndex" json:"policyId"`
	ScopeType      string         `gorm:"column:scope_type;not null;index:idx_scope,priority:1" json:"scopeType"`
	ScopeID        string         `gorm:"column:scope_id;not null;index:idx_scope,priority:2" json:"scopeId"`
	Name           string         `gorm:"column:name;not null" json:"name"`
	MatchRule      datatypes.JSON `gorm:"column:match_rule;type:json;not null" json:"matchRule"`
	ApproverRule   datatypes.JSON `gorm:"column:approver_rule;type:json;not null" json:"approverRule"`
	MinApprovals   int            `gorm:"column:min_approvals;not null;default:1" json:"minApprovals"`
	Mode           string         `gorm:"column:mode;not null;default:'any'" json:"mode"`
	TimeoutSeconds int            `gorm:"column:timeout_seconds;not null;default:3600" json:"timeoutSeconds"`
	Enabled        int            `gorm:"column:enabled;not null;default:1;index" json:"enabled"`
	Description    string         `gorm:"column:description;not null;default:''" json:"description"`
	CreatedBy      string         `gorm:"column:created_by;not null" json:"createdBy"`
}

// TableName 返回数据库表名
func (ApprovalPolicy) TableName() string {
	return "approval_policy"
}
