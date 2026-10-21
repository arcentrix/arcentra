// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import "time"

// CopilotRerunPrepareRequest 绑定一个显式准备操作的固定恢复身份
// Owner, deadline, policy and new execution identities are server-selected.
type CopilotRerunPrepareRequest struct {
	RequestID        string `json:"requestId"`
	OriginalJobRunID string `json:"originalJobRunId"`
	SnapshotDigest   string `json:"snapshotDigest"`
}

// CopilotRerunConfirmRequest 绑定已审阅内容和独立的新执行确认
// This acknowledgement never records an approval vote.
type CopilotRerunConfirmRequest struct {
	ExpectedVersion          uint64 `json:"expectedVersion"`
	IntentDigest             string `json:"intentDigest"`
	ReviewedRunContextDigest string `json:"reviewedRunContextDigest"`
	AcknowledgeNewExecution  bool   `json:"acknowledgeNewExecution"`
}

// CopilotRerunCancelRequest 仅取消尚未确认的准备请求
// It is not a target cancellation or rollback request.
type CopilotRerunCancelRequest struct {
	ExpectedVersion          uint64 `json:"expectedVersion"`
	IntentDigest             string `json:"intentDigest"`
	ReviewedRunContextDigest string `json:"reviewedRunContextDigest"`
}

// CopilotRerunAvailability 表示独立的服务可用性，不依赖模型或投票功能
type CopilotRerunAvailability struct {
	Available                   bool `json:"available"`
	CancelNewExecutionAvailable bool `json:"cancelNewExecutionAvailable"`
}

// CopilotRerunDefinitionReview 只含安全的不可变定义定位信息
type CopilotRerunDefinitionReview struct {
	Repository         string `json:"repository"`
	Path               string `json:"path"`
	Revision           string `json:"revision"`
	ResolvedSpecDigest string `json:"resolvedSpecDigest"`
}

// CopilotRerunSourceReview 区分预期检出和实际检出观察
type CopilotRerunSourceReview struct {
	Repository     string `json:"repository"`
	Ref            string `json:"ref"`
	ExpectedCommit string `json:"expectedCommit"`
}

// CopilotRerunPolicyReview 仅提供当前已授权的独立审批策略
type CopilotRerunPolicyReview struct {
	ID                    string   `json:"id"`
	Fingerprint           string   `json:"fingerprint"`
	Mode                  string   `json:"mode"`
	RequiredApproverCount int      `json:"requiredApproverCount"`
	Candidates            []string `json:"candidates"`
	TimeoutSeconds        int64    `json:"timeoutSeconds"`
}

// CopilotRerunRetryBudget 限定整个新作业的重试预算
type CopilotRerunRetryBudget struct {
	MaxAttempts      int32   `json:"maxAttempts"`
	Delay            string  `json:"delay"`
	AllowedExitCodes []int32 `json:"allowedExitCodes"`
}

// CopilotRerunReview 不包含原始定义、变量、命令、秘密或执行凭据
type CopilotRerunReview struct {
	ProjectID         string                       `json:"projectId"`
	PipelineID        string                       `json:"pipelineId"`
	OriginalRunID     string                       `json:"originalRunId"`
	OriginalJobRunID  string                       `json:"originalJobRunId"`
	OriginalRunStatus int                          `json:"originalRunStatus"`
	OriginalJobStatus int                          `json:"originalJobStatus"`
	JobName           string                       `json:"jobName"`
	RetryMode         string                       `json:"retryMode"`
	SnapshotDigest    string                       `json:"snapshotDigest"`
	SnapshotExpiresAt time.Time                    `json:"snapshotExpiresAt"`
	Definition        CopilotRerunDefinitionReview `json:"definition"`
	Source            CopilotRerunSourceReview     `json:"source"`
	Policy            CopilotRerunPolicyReview     `json:"policy"`
	RetryBudget       CopilotRerunRetryBudget      `json:"retryBudget"`
}

// CopilotRerunEligibility 只读返回支持范围和可安全展示的审阅
type CopilotRerunEligibility struct {
	Available  bool                `json:"available"`
	Eligible   bool                `json:"eligible"`
	ReasonCode string              `json:"reasonCode"`
	ServerTime time.Time           `json:"serverTime"`
	Review     *CopilotRerunReview `json:"review,omitempty"`
}

// CopilotRerunIntent 保留固定新执行身份，不将确认伪装成审批或运行
type CopilotRerunIntent struct {
	IntentID                    string              `json:"intentId"`
	RequestID                   string              `json:"requestId"`
	ProjectID                   string              `json:"projectId"`
	PipelineID                  string              `json:"pipelineId"`
	OriginalRunID               string              `json:"originalRunId"`
	OriginalJobRunID            string              `json:"originalJobRunId"`
	NewRunID                    string              `json:"newRunId"`
	NewJobRunID                 string              `json:"newJobRunId"`
	SnapshotDigest              string              `json:"snapshotDigest"`
	IntentDigest                string              `json:"intentDigest"`
	ReviewedRunContextDigest    string              `json:"reviewedRunContextDigest"`
	Version                     uint64              `json:"version"`
	State                       string              `json:"state"`
	CreatedAt                   time.Time           `json:"createdAt"`
	ExpiresAt                   time.Time           `json:"expiresAt"`
	ConfirmedAt                 *time.Time          `json:"confirmedAt,omitempty"`
	CancelledAt                 *time.Time          `json:"cancelledAt,omitempty"`
	ServerTime                  time.Time           `json:"serverTime"`
	Review                      *CopilotRerunReview `json:"review,omitempty"`
	ReviewStatus                string              `json:"reviewStatus"`
	CanConfirm                  bool                `json:"canConfirm"`
	CanCancelPrepared           bool                `json:"canCancelPrepared"`
	CancelNewExecutionAvailable bool                `json:"cancelNewExecutionAvailable"`
}
