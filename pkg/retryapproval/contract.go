// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

// Package retryapproval 定义不可扩张的整作业重试审批证据
package retryapproval

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	artifact "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
)

// Kind 标识既有整作业精确内容审批
const Kind = "retry_whole_job_v1"

// FailedStepKind 标识保持同一活动会话的精确失败步骤审批
const FailedStepKind = "retry_failed_step_v1"

// KindForPlan 从完整规范计划派生审批种类，禁止版本与模式替换
func KindForPlan(plan *taskqueue.JobRetryPlan) (string, error) {
	if plan == nil || !plan.RetryApprovalRequired {
		return "", fmt.Errorf("approval requires a gated retry plan")
	}
	if _, err := plan.Canonical(); err != nil {
		return "", err
	}
	switch {
	case plan.Version == 1 && plan.Policy.Mode == "whole_job":
		return Kind, nil
	case plan.Version == 2 && plan.Policy.Mode == "failed_step":
		return FailedStepKind, nil
	default:
		return "", fmt.Errorf("unsupported approval plan tuple")
	}
}

// ValidateKindPlan 验证审批种类与完整冻结计划严格匹配
func ValidateKindPlan(kind string, plan *taskqueue.JobRetryPlan) error {
	expected, err := KindForPlan(plan)
	if err != nil {
		return err
	}
	if kind != expected {
		return fmt.Errorf("approval kind does not match retry plan")
	}
	return nil
}

// Plugin 为内置审批门禁名称
const Plugin = "builtin:retry-approval"

// MaxBytes 限制证据和协议大小
const MaxBytes = taskqueue.MaxJobInputManifestBytes

// Policy 固定操作者配置和候选人快照
type Policy struct {
	ID                    string   `json:"id"`
	Fingerprint           string   `json:"fingerprint"`
	Candidates            []string `json:"candidates"`
	Mode                  string   `json:"mode"`
	RequiredApproverCount int      `json:"requiredApproverCount"`
	TimeoutSeconds        int64    `json:"timeoutSeconds"`
}

// RunContext 固定可信手动请求和完整解析定义身份
type RunContext struct {
	Version                  int      `json:"version"`
	ProjectID                string   `json:"projectId"`
	PipelineID               string   `json:"pipelineId"`
	PipelineRunID            string   `json:"pipelineRunId"`
	ActorID                  string   `json:"actorId"`
	DefinitionCommitSHA      string   `json:"definitionCommitSha"`
	DefinitionPath           string   `json:"definitionPath"`
	ResolvedDefinitionDigest string   `json:"resolvedDefinitionDigest"`
	GatedJobs                []string `json:"gatedJobs"`
	Policy                   Policy   `json:"policy"`
}

func validID(s string) bool { return artifact.Identity(s) == nil && strings.TrimSpace(s) == s }
func encode(v any) (string, error) {
	b, e := (sonic.Config{SortMapKeys: true}).Froze().Marshal(v)
	if e != nil || len(b) > MaxBytes {
		return "", fmt.Errorf("approval encoding invalid or oversized")
	}
	return string(b), nil
}
func digest(domain, s string) string {
	h := sha256.Sum256([]byte(domain + "\x00" + s))
	return "v1:" + hex.EncodeToString(h[:])
}

// Validate 校验有界策略，不允许请求者自批
func (p Policy) Validate(actor string) error {
	if !validID(p.ID) || !sourceprovenance.ValidBindingDigest(p.Fingerprint) || len(p.Candidates) < 1 || len(p.Candidates) > 100 || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 3600 || p.RequiredApproverCount < 1 || p.RequiredApproverCount > len(p.Candidates) || (p.Mode != "any" && p.Mode != "all") || (p.Mode == "all" && p.RequiredApproverCount != len(p.Candidates)) {
		return fmt.Errorf("invalid retry approval policy")
	}
	previous := ""
	for _, id := range p.Candidates {
		if !validID(id) || id == actor || id <= previous {
			return fmt.Errorf("approval candidates must be sorted unique non-requesters")
		}
		previous = id
	}
	return nil
}

// Canonical 验证可信运行上下文并返回规范编码
func (c *RunContext) Canonical() (string, error) {
	if c == nil || c.Version != 1 || !validID(c.ProjectID) || !validID(c.PipelineID) || !validID(c.PipelineRunID) || !validID(c.ActorID) || !sourceprovenance.ValidCommit(c.DefinitionCommitSHA) || !sourceprovenance.ValidBindingDigest(c.ResolvedDefinitionDigest) || c.DefinitionPath == "" || len(c.DefinitionPath) > 1024 || strings.ContainsAny(c.DefinitionPath, "\x00\r\n") || len(c.GatedJobs) < 1 || len(c.GatedJobs) > 1000 {
		return "", fmt.Errorf("invalid trusted retry approval context")
	}
	if err := c.Policy.Validate(c.ActorID); err != nil {
		return "", err
	}
	previous := ""
	for _, n := range c.GatedJobs {
		if n == "" || len(n) > 128 || strings.TrimSpace(n) != n || n <= previous {
			return "", fmt.Errorf("gated jobs must be sorted unique names")
		}
		previous = n
	}
	return encode(c)
}

// Digest 返回上下文的域隔离摘要
func (c *RunContext) Digest() (string, error) {
	s, e := c.Canonical()
	if e != nil {
		return "", e
	}
	return digest("arcentra-retry-approval-context-v1", s), nil
}

// ParseRunContext 拒绝非规范、未知和重复字段
func ParseRunContext(s string) (*RunContext, error) {
	var c RunContext
	if len(s) > MaxBytes || sonic.UnmarshalString(s, &c) != nil {
		return nil, fmt.Errorf("invalid approval context")
	}
	v, e := c.Canonical()
	if e != nil || v != s {
		return nil, fmt.Errorf("noncanonical approval context")
	}
	return &c, nil
}

// RequiresJob 判断不可变运行上下文中的指定门禁
func (c *RunContext) RequiresJob(name string) bool {
	if c == nil {
		return false
	}
	for _, n := range c.GatedJobs {
		if n == name {
			return true
		}
	}
	return false
}

// Request 固定一次观察或新执行准入的完整身份
type Request struct {
	Version                 int                        `json:"version"`
	Operation               string                     `json:"operation"`
	Identity                taskqueue.JobInputIdentity `json:"identity"`
	GenerationID            string                     `json:"generationId"`
	GenerationBindingDigest string                     `json:"generationBindingDigest"`
	RetryPlanDigest         string                     `json:"retryPlanDigest"`
	InputManifestDigest     string                     `json:"inputManifestDigest"`
}

// Canonical 校验版本与完整执行身份
func (r *Request) Canonical() (string, error) {
	if r == nil || r.Version != 1 || (r.Operation != "observe" && r.Operation != "admit") || r.Identity.Validate() != nil || !validID(r.GenerationID) || !sourceprovenance.ValidBindingDigest(r.GenerationBindingDigest) || !sourceprovenance.ValidBindingDigest(r.RetryPlanDigest) || !sourceprovenance.ValidBindingDigest(r.InputManifestDigest) {
		return "", fmt.Errorf("invalid approval admission request")
	}
	return encode(r)
}

// Digest 返回准确请求摘要
func (r *Request) Digest() (string, error) {
	s, e := r.Canonical()
	if e != nil {
		return "", e
	}
	return digest("arcentra-retry-approval-request-v1", s), nil
}

// ParseRequest 只接受规范请求
func ParseRequest(s string) (*Request, error) {
	var r Request
	if len(s) > MaxBytes || sonic.UnmarshalString(s, &r) != nil {
		return nil, fmt.Errorf("invalid approval request")
	}
	v, e := r.Canonical()
	if e != nil || v != s {
		return nil, fmt.Errorf("noncanonical approval request")
	}
	return &r, nil
}

// Admission 保存不可变准入回执，历史回执自身不授予执行权
type Admission struct {
	Version             int     `json:"version"`
	Request             Request `json:"request"`
	ApprovalID          string  `json:"approvalId"`
	EvidenceHash        string  `json:"evidenceHash"`
	AdmittedAtUnixMilli int64   `json:"admittedAtUnixMilli"`
	ExpiresAtUnixMilli  int64   `json:"expiresAtUnixMilli"`
}

// Canonical 验证完整准入回执
func (a *Admission) Canonical() (string, error) {
	if a == nil || a.Version != 1 || a.Request.Operation != "admit" || !validID(a.ApprovalID) || !sourceprovenance.ValidBindingDigest(a.EvidenceHash) || a.AdmittedAtUnixMilli <= 0 || a.ExpiresAtUnixMilli <= a.AdmittedAtUnixMilli {
		return "", fmt.Errorf("invalid approval admission")
	}
	if _, e := a.Request.Canonical(); e != nil {
		return "", e
	}
	return encode(a)
}

// Digest 返回回执摘要
func (a *Admission) Digest() (string, error) {
	s, e := a.Canonical()
	if e != nil {
		return "", e
	}
	return digest("arcentra-retry-approval-admission-v1", s), nil
}

// ParseAdmission 只接受规范回执
func ParseAdmission(s string) (*Admission, error) {
	var a Admission
	if len(s) > MaxBytes || sonic.UnmarshalString(s, &a) != nil {
		return nil, fmt.Errorf("invalid approval admission")
	}
	v, e := a.Canonical()
	if e != nil || v != s {
		return nil, fmt.Errorf("noncanonical approval admission")
	}
	return &a, nil
}

// Response 明确区分观察、待审和新准入结果
type Response struct {
	Version            int        `json:"version"`
	Required           bool       `json:"required"`
	ExecutionAllowed   bool       `json:"executionAllowed"`
	State              string     `json:"state"`
	ApprovalID         string     `json:"approvalId"`
	EvidenceHash       string     `json:"evidenceHash"`
	ExpiresAtUnixMilli int64      `json:"expiresAtUnixMilli"`
	Admission          *Admission `json:"admission,omitempty"`
}

// Canonical 验证响应，缺失字段不能降级为无门禁
func (r *Response) Canonical() (string, error) {
	if r == nil || r.Version != 1 {
		return "", fmt.Errorf("invalid approval response")
	}
	if r.ExecutionAllowed && (!r.Required || r.Admission == nil || r.State != "approved") {
		return "", fmt.Errorf("invalid execution grant")
	}
	if !r.Required {
		if r.State != "ungated" || r.ApprovalID != "" || r.EvidenceHash != "" || r.ExpiresAtUnixMilli != 0 || r.Admission != nil {
			return "", fmt.Errorf("invalid ungated response")
		}
	} else {
		if !validID(r.ApprovalID) || !sourceprovenance.ValidBindingDigest(r.EvidenceHash) || r.ExpiresAtUnixMilli <= 0 {
			return "", fmt.Errorf("missing gate identity")
		}
		switch r.State {
		case "pending", "approved", "rejected", "expired", "canceled":
		default:
			return "", fmt.Errorf("unknown approval state")
		}
		if r.Admission != nil {
			if r.State != "approved" || r.Admission.ApprovalID != r.ApprovalID || r.Admission.EvidenceHash != r.EvidenceHash || r.Admission.ExpiresAtUnixMilli != r.ExpiresAtUnixMilli {
				return "", fmt.Errorf("admission identity mismatch")
			}
			if _, e := r.Admission.Canonical(); e != nil {
				return "", e
			}
		}
	}
	return encode(r)
}

// ParseResponse 只接受规范响应
func ParseResponse(s string) (*Response, error) {
	var r Response
	if len(s) > MaxBytes || sonic.UnmarshalString(s, &r) != nil {
		return nil, fmt.Errorf("invalid approval response")
	}
	v, e := r.Canonical()
	if e != nil || v != s {
		return nil, fmt.Errorf("noncanonical approval response")
	}
	return &r, nil
}

// Digest 计算策略指纹，忽略调用者提供的指纹字段
func (p Policy) Digest() (string, error) {
	p.Fingerprint = ""
	s, e := encode(p)
	if e != nil {
		return "", e
	}
	return digest("arcentra-retry-approval-policy-v1", s), nil
}
