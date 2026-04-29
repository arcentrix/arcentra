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

const (
	// ApprovalDecisionApprove 表示同意
	ApprovalDecisionApprove = "approve"
	// ApprovalDecisionReject 表示拒绝
	ApprovalDecisionReject = "reject"
)

// ApprovalDecision 表示审批决策记录
type ApprovalDecision struct {
	BaseModel
	DecisionID string `gorm:"column:decision_id;not null;uniqueIndex" json:"decisionId"`
	ApprovalID string `gorm:"column:approval_id;not null;index:idx_approval_user,priority:1" json:"approvalId"`
	UserID     string `gorm:"column:user_id;not null;index:idx_approval_user,priority:2" json:"userId"`
	Decision   string `gorm:"column:decision;not null" json:"decision"`
	Comment    string `gorm:"column:comment;type:text" json:"comment"`
}

// TableName 返回数据库表名
func (ApprovalDecision) TableName() string {
	return "approval_decision"
}
