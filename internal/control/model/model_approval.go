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

// ApprovalRequest status constants.
const (
	ApprovalStatusPending  = 0
	ApprovalStatusApproved = 1
	ApprovalStatusRejected = 2
	ApprovalStatusExpired  = 3
	ApprovalStatusCanceled = 4
)

// ApprovalRequest represents a pipeline approval gate persisted in the DB.
type ApprovalRequest struct {
	BaseModel
	ApprovalID            string     `gorm:"column:approval_id;type:varchar(64);uniqueIndex" json:"approvalId"`
	PipelineRunID         string     `gorm:"column:pipeline_run_id;type:varchar(64);index" json:"pipelineRunId"`
	JobName               string     `gorm:"column:job_name;type:varchar(128)" json:"jobName"`
	StepName              string     `gorm:"column:step_name;type:varchar(128)" json:"stepName"`
	Plugin                string     `gorm:"column:plugin;type:varchar(64)" json:"plugin"`
	Status                int        `gorm:"column:status;type:tinyint;default:0;index" json:"status"`
	PolicyID              string     `gorm:"column:policy_id;type:varchar(64);index" json:"policyId"`
	Mode                  string     `gorm:"column:mode;type:varchar(16);default:'any'" json:"mode"`
	RequiredApproverCount int        `gorm:"column:required_approver_count;type:int;default:1" json:"requiredApproverCount"`
	ApprovedCount         int        `gorm:"column:approved_count;type:int;default:0" json:"approvedCount"`
	RejectedCount         int        `gorm:"column:rejected_count;type:int;default:0" json:"rejectedCount"`
	Environment           string     `gorm:"column:environment;type:varchar(32)" json:"environment"`
	RequestedBy           string     `gorm:"column:requested_by;type:varchar(64)" json:"requestedBy"`
	ApprovedBy            string     `gorm:"column:approved_by;type:varchar(64)" json:"approvedBy"`
	Reason                string     `gorm:"column:reason;type:text" json:"reason"`
	CallbackURL           string     `gorm:"column:callback_url;type:varchar(512)" json:"callbackUrl"`
	NotifyChannels        string     `gorm:"column:notify_channels;type:varchar(512)" json:"notifyChannels"`
	CandidateApprovers    string     `gorm:"column:candidate_approvers;type:json" json:"candidateApprovers"`
	ExpiresAt             *time.Time `gorm:"column:expires_at;index" json:"expiresAt"`
}

// TableName returns the database table name.
func (ApprovalRequest) TableName() string {
	return "approval_request"
}

// IsApprovalTerminal returns true when the approval is in a final state.
func IsApprovalTerminal(status int) bool {
	return status == ApprovalStatusApproved ||
		status == ApprovalStatusRejected ||
		status == ApprovalStatusExpired ||
		status == ApprovalStatusCanceled
}
