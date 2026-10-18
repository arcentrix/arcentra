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

import (
	"errors"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// 审批请求状态常量
const (
	ApprovalStatusPending  = 0
	ApprovalStatusApproved = 1
	ApprovalStatusRejected = 2
	ApprovalStatusExpired  = 3
	ApprovalStatusCanceled = 4
)

// ApprovalRequest 保存审批门禁及不可变候选人快照
type ApprovalRequest struct {
	InboxOrigin   int     `gorm:"column:inbox_origin;not null;default:0;index:idx_approval_inbox_origin" json:"-"`
	RetryJobRunID *string `gorm:"column:retry_job_run_id;type:varchar(64);uniqueIndex:uk_approval_retry_job_run;check:ck_approval_exclusive_binding,retry_job_run_id IS NULL OR deployment_release_id IS NULL" json:"retryJobRunId,omitempty"`
	BindingKind   string  `gorm:"column:binding_kind;type:varchar(32);not null;default:''" json:"bindingKind"`
	EvidenceJSON  string  `gorm:"column:evidence_json;type:mediumtext" json:"-"`
	BaseModel
	DeploymentReleaseID   *string    `gorm:"column:deployment_release_id;type:varchar(64);uniqueIndex:uk_approval_deployment_release" json:"deploymentReleaseId,omitempty"`
	EvidenceHash          string     `gorm:"column:evidence_hash;type:varchar(67)" json:"evidenceHash,omitempty"`
	Version               int64      `gorm:"column:version;not null;default:1" json:"version"`
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
	Environment           string     `gorm:"column:environment;type:varchar(64)" json:"environment"`
	RequestedBy           string     `gorm:"column:requested_by;type:varchar(64)" json:"requestedBy"`
	ApprovedBy            string     `gorm:"column:approved_by;type:varchar(64)" json:"approvedBy"`
	Reason                string     `gorm:"column:reason;type:text" json:"reason"`
	CallbackURL           string     `gorm:"column:callback_url;type:varchar(512)" json:"callbackUrl"`
	NotifyChannels        string     `gorm:"column:notify_channels;type:varchar(512)" json:"notifyChannels"`
	CandidateApprovers    string     `gorm:"column:candidate_approvers;type:json" json:"candidateApprovers"`
	ExpiresAt             *time.Time `gorm:"column:expires_at;precision:3;index" json:"expiresAt"`
}

// TableName 返回审批请求表名
func (ApprovalRequest) TableName() string {
	return "approval_request"
}

// IsApprovalTerminal 判断审批请求是否处于终态
func IsApprovalTerminal(status int) bool {
	return status == ApprovalStatusApproved ||
		status == ApprovalStatusRejected ||
		status == ApprovalStatusExpired ||
		status == ApprovalStatusCanceled
}

// ApprovalCandidates 校验并读取持久化候选人快照，空值不能代表允许任意用户
func ApprovalCandidates(req *ApprovalRequest) ([]string, error) {
	if req == nil || len(req.CandidateApprovers) > 8192 {
		return nil, errors.New("invalid approval candidate snapshot")
	}
	var candidates []string
	if err := sonic.UnmarshalString(req.CandidateApprovers, &candidates); err != nil || len(candidates) == 0 || len(candidates) > 100 {
		return nil, errors.New("approval candidates are required")
	}
	seen := make(map[string]bool, len(candidates))
	for _, id := range candidates {
		if id == "" || len(id) > 64 || strings.TrimSpace(id) != id || seen[id] || id == req.RequestedBy {
			return nil, errors.New("invalid approval candidate snapshot")
		}
		seen[id] = true
	}
	if req.RequiredApproverCount < 1 || req.RequiredApproverCount > len(candidates) || (req.Mode != "any" && req.Mode != "all") || (req.Mode == "all" && req.RequiredApproverCount != len(candidates)) {
		return nil, errors.New("invalid approval quorum")
	}
	return candidates, nil
}

// IsApprovalCandidate 判断操作者是否属于可信候选人快照
func IsApprovalCandidate(req *ApprovalRequest, actor string) bool {
	if actor == "" || strings.TrimSpace(actor) != actor || len(actor) > 64 {
		return false
	}
	candidates, err := ApprovalCandidates(req)
	if err != nil {
		return false
	}
	for _, id := range candidates {
		if id == actor {
			return true
		}
	}
	return false
}
