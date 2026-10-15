// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package taskqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/bytedance/sonic"
	"google.golang.org/protobuf/proto"
)

// JobGenerationEnvironment 为容器整作业重试保留一基代次环境变量
const JobGenerationEnvironment = RuntimeGenerationEnv

// RetryStepPlan 固定全部预分配尝试及其不可变步骤定义摘要
// 完整可执行定义只保留摘要，避免在计划中复制环境秘密
type RetryStepPlan struct {
	Name             string   `json:"name"`
	StepIndex        int32    `json:"stepIndex"`
	Uses             string   `json:"uses"`
	Action           string   `json:"action"`
	ContinueOnError  bool     `json:"continueOnError"`
	AttemptRunIDs    []string `json:"attemptRunIds"`
	DefinitionDigest string   `json:"definitionDigest"`
}

// JobRetryPlan 保存控制面在派发时授权的完整有限重试计划
// Policy 包括允许退出码、幂等声明、次数和等待时间，Agent 不得扩张
type JobRetryPlan struct {
	RetryApprovalRequired       bool              `json:"retryApprovalRequired,omitempty"`
	RetryApprovalTimeoutSeconds int64             `json:"retryApprovalTimeoutSeconds,omitempty"`
	Version                     int               `json:"version"`
	Policy                      *pipelinev1.Retry `json:"policy"`
	DefinitionDigest            string            `json:"definitionDigest"`
	Steps                       []RetryStepPlan   `json:"steps"`
}

func retryCanonical(value any) (string, error) {
	data, err := (sonic.Config{SortMapKeys: true}).Froze().Marshal(value)
	if err != nil || len(data) > MaxJobInputManifestBytes {
		return "", fmt.Errorf("retry contract encoding is invalid or exceeds limit")
	}
	return string(data), nil
}
func retryDigest(domain, value string) string {
	sum := sha256.Sum256([]byte(domain + "\x00" + value))
	return "v1:" + hex.EncodeToString(sum[:])
}

// ValidateWholeJobRetryPolicy 拒绝缺失、越界或未声明幂等的容器整作业重试
func ValidateWholeJobRetryPolicy(p *pipelinev1.Retry) error {
	if p == nil || p.Mode != "whole_job" || p.MaxAttempts < 2 || p.MaxAttempts > 5 || !p.Idempotent || len(p.AllowedExitCodes) == 0 {
		return fmt.Errorf("container retry requires explicit idempotent whole_job policy with 2-5 attempts")
	}
	seen := map[int32]bool{}
	for _, code := range p.AllowedExitCodes {
		if code < 1 || code > 125 || seen[code] {
			return fmt.Errorf("retry requires unique allowed exit codes 1-125")
		}
		seen[code] = true
	}
	if p.Delay != "" {
		d, err := time.ParseDuration(p.Delay)
		if err != nil || d < 0 || d > time.Hour {
			return fmt.Errorf("retry delay must be between 0s and 1h")
		}
	}
	return nil
}

// NewJobRetryPlan 冻结完整可执行载荷、重试上限和预分配步骤身份
func NewJobRetryPlan(payload *JobRunTaskPayload) (*JobRetryPlan, error) {
	if payload == nil || payload.Runtime == nil {
		return nil, fmt.Errorf("runtime retry payload is required")
	}
	if payload.RetryApprovalRequired {
		if payload.Retry == nil || payload.Retry.Mode != "whole_job" {
			return nil, fmt.Errorf("durable approval supports only whole_job retry")
		}
		if payload.RetryApprovalTimeoutSeconds < 1 || payload.RetryApprovalTimeoutSeconds > 3600 {
			return nil, fmt.Errorf("retry approval requires a bounded trusted timeout")
		}
	} else if payload.RetryApprovalTimeoutSeconds != 0 {
		return nil, fmt.Errorf("ungated retry cannot carry approval timeout")
	}
	if err := ValidateContainerRetryPolicy(payload.Retry); err != nil {
		return nil, err
	}
	copy := *payload
	copy.RetryPlan = nil
	copy.RetryPlanDigest = ""
	// 凭据仅用于既有源获取，不能成为重试授权或远端明文持久化数据
	if copy.Source != nil {
		source := *copy.Source
		source.Auth = nil
		copy.Source = &source
	}
	encoded, err := retryCanonical(copy)
	if err != nil {
		return nil, err
	}
	version, definitionDomain := 1, "arcentra-job-retry-definition-v1"
	if payload.Retry.Mode == "failed_step" {
		version, definitionDomain = 2, "arcentra-failed-step-job-definition-v1"
	}
	plan := &JobRetryPlan{RetryApprovalRequired: payload.RetryApprovalRequired, RetryApprovalTimeoutSeconds: payload.RetryApprovalTimeoutSeconds, Version: version, Policy: proto.Clone(payload.Retry).(*pipelinev1.Retry), DefinitionDigest: retryDigest(definitionDomain, encoded), Steps: make([]RetryStepPlan, 0, len(payload.Steps))}
	if _, exists := payload.Env[JobGenerationEnvironment]; exists {
		return nil, fmt.Errorf("reserved generation environment cannot be overridden")
	}
	if _, exists := payload.Variables[JobGenerationEnvironment]; exists {
		return nil, fmt.Errorf("reserved generation variable cannot be overridden")
	}
	for _, step := range payload.Steps {
		if len(step.AttemptRunIDs) == 0 || step.StepRunID != step.AttemptRunIDs[0] {
			return nil, fmt.Errorf("initial step must match complete attempt plan")
		}
		if _, exists := step.Env[JobGenerationEnvironment]; exists {
			return nil, fmt.Errorf("reserved generation environment cannot be overridden")
		}
		encoded, err := retryCanonical(step)
		if err != nil {
			return nil, err
		}
		plan.Steps = append(plan.Steps, RetryStepPlan{Name: step.Name, StepIndex: step.StepIndex, Uses: step.Uses, Action: step.Action, ContinueOnError: step.ContinueOnError, AttemptRunIDs: append([]string(nil), step.AttemptRunIDs...), DefinitionDigest: retryDigest("arcentra-retry-step-definition-v1", encoded)})
	}
	if _, err := plan.Canonical(); err != nil {
		return nil, err
	}
	return plan, nil
}

// Canonical 验证完整计划及全部互异尝试身份并返回规范编码
func (p *JobRetryPlan) Canonical() (string, error) {
	if p == nil || (p.Version != 1 && p.Version != 2) || !sourceprovenance.ValidBindingDigest(p.DefinitionDigest) || len(p.Steps) == 0 || len(p.Steps) > 1000 {
		return "", fmt.Errorf("invalid retry plan")
	}
	if p.RetryApprovalRequired {
		if p.Version != 1 || p.Policy == nil || p.Policy.Mode != "whole_job" {
			return "", fmt.Errorf("durable approval supports only whole_job v1 plan")
		}
		if p.RetryApprovalTimeoutSeconds < 1 || p.RetryApprovalTimeoutSeconds > 3600 {
			return "", fmt.Errorf("retry approval plan requires bounded timeout")
		}
	} else if p.RetryApprovalTimeoutSeconds != 0 {
		return "", fmt.Errorf("ungated plan has approval timeout")
	}
	if err := ValidateContainerRetryPolicy(p.Policy); err != nil {
		return "", err
	}
	if (p.Version == 1 && p.Policy.Mode != "whole_job") || (p.Version == 2 && p.Policy.Mode != "failed_step") {
		return "", fmt.Errorf("retry plan version does not match mode")
	}
	ids, names := map[string]bool{}, map[string]bool{}
	previous := int32(-1)
	for _, s := range p.Steps {
		if s.Name == "" || strings.TrimSpace(s.Name) != s.Name || names[s.Name] || s.StepIndex <= previous || !sourceprovenance.ValidBindingDigest(s.DefinitionDigest) || len(s.AttemptRunIDs) != int(p.Policy.MaxAttempts) {
			return "", fmt.Errorf("invalid retry step plan")
		}
		if p.Policy.Mode == "failed_step" && s.ContinueOnError {
			return "", fmt.Errorf("failed_step first profile rejects continueOnError")
		}
		names[s.Name] = true
		previous = s.StepIndex
		for _, id := range s.AttemptRunIDs {
			if artifactutil.Identity(id) != nil || ids[id] {
				return "", fmt.Errorf("retry attempt identities must be unique")
			}
			ids[id] = true
		}
	}
	return retryCanonical(p)
}

// ParseJobRetryPlan 只读取规范编码，拒绝未知或重复字段造成的歧义
func ParseJobRetryPlan(encoded string) (*JobRetryPlan, error) {
	if len(encoded) > MaxJobInputManifestBytes {
		return nil, fmt.Errorf("retry plan exceeds limit")
	}
	var p JobRetryPlan
	if err := sonic.UnmarshalString(encoded, &p); err != nil {
		return nil, fmt.Errorf("invalid retry plan encoding")
	}
	canonical, err := p.Canonical()
	if err != nil || canonical != encoded {
		return nil, fmt.Errorf("retry plan is not canonical")
	}
	return &p, nil
}

// Digest 将重试计划绑定原派发身份及版本一运行时摘要
func (p *JobRetryPlan) Digest(pipelineID, runID, jobID, agentID, runtimeDigest string) (string, error) {
	for _, id := range []string{pipelineID, runID, jobID, agentID} {
		if artifactutil.Identity(id) != nil {
			return "", fmt.Errorf("exact retry dispatch identity is required")
		}
	}
	if !sourceprovenance.ValidBindingDigest(runtimeDigest) {
		return "", fmt.Errorf("runtime binding is required")
	}
	encoded, err := p.Canonical()
	if err != nil {
		return "", err
	}
	bound, err := retryCanonical([]string{pipelineID, runID, jobID, agentID, runtimeDigest, encoded})
	if err != nil {
		return "", err
	}
	domain := "arcentra-job-retry-plan-v1"
	if p.Version == 2 {
		domain = "arcentra-failed-step-job-plan-v1"
	}
	return retryDigest(domain, bound), nil
}

// ValidateJobRetryPlan 拒绝与完整派发内容不同的计划或摘要
func ValidateJobRetryPlan(payload *JobRunTaskPayload) error {
	if payload == nil {
		return fmt.Errorf("job payload is required")
	}
	if payload.Retry == nil {
		if payload.RetryPlan != nil || payload.RetryPlanDigest != "" || payload.RetryApprovalRequired || payload.RetryApprovalTimeoutSeconds != 0 {
			return fmt.Errorf("unexpected retry plan")
		}
		return nil
	}
	plan, err := NewJobRetryPlan(payload)
	if err != nil {
		return err
	}
	canonical, _ := plan.Canonical()
	stored, err := payload.RetryPlan.Canonical()
	if err != nil || canonical != stored {
		return fmt.Errorf("retry plan does not match complete dispatch")
	}
	digest, err := plan.Digest(payload.PipelineID, payload.PipelineRunID, payload.JobRunID, payload.AgentID, payload.Runtime.BindingDigest)
	if err != nil || digest != payload.RetryPlanDigest {
		return fmt.Errorf("retry plan binding mismatch")
	}
	return nil
}

// ValidateFailedStepRetryPolicy 校验独立失败步骤模式并保留既有有界重试限制
func ValidateFailedStepRetryPolicy(p *pipelinev1.Retry) error {
	if p == nil || p.Mode != "failed_step" {
		return fmt.Errorf("failed_step policy is required")
	}
	copy := proto.Clone(p).(*pipelinev1.Retry)
	copy.Mode = "whole_job"
	return ValidateWholeJobRetryPolicy(copy)
}

// ValidateContainerRetryPolicy 按明确模式校验，不改变 whole_job 版本一语义
func ValidateContainerRetryPolicy(p *pipelinev1.Retry) error {
	if p != nil && p.Mode == "failed_step" {
		return ValidateFailedStepRetryPolicy(p)
	}
	return ValidateWholeJobRetryPolicy(p)
}
