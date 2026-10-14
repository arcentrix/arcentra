// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package taskqueue

import (
	"fmt"
	"strings"

	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

// JobGeneration 是控制面颁发的一基容器代次身份，不替换原作业认领
// ExecutionAllowed 仅允许当前活动 worker 准备会话，命令仍须步骤 RUNNING 授权
type JobGeneration struct {
	Version              int    `json:"version"`
	PipelineID           string `json:"pipelineId"`
	PipelineRunID        string `json:"pipelineRunId"`
	JobRunID             string `json:"jobRunId"`
	AgentID              string `json:"agentId"`
	OriginalClaimID      string `json:"originalClaimId"`
	RuntimeBindingDigest string `json:"runtimeBindingDigest"`
	SourceBindingDigest  string `json:"sourceBindingDigest"`
	InputManifestDigest  string `json:"inputManifestDigest"`
	RetryPlanDigest      string `json:"retryPlanDigest"`
	GenerationID         string `json:"generationId"`
	Number               int    `json:"number"`
	RuntimeClaimID       string `json:"runtimeClaimId"`
	BindingDigest        string `json:"bindingDigest"`
	PreviousGenerationID string `json:"previousGenerationId"`
	NotBeforeUnixMilli   int64  `json:"notBeforeUnixMilli"`
	PreparationOnly      bool   `json:"preparationOnly"`
	ExecutionAllowed     bool   `json:"executionAllowed"`
}

func canonicalUUID(value string) bool {
	v, err := uuid.Parse(value)
	return err == nil && v.String() == value && v != uuid.Nil
}

// Digest 计算完整代次身份绑定，不把只读响应标记当作执行授权
func (g JobGeneration) Digest() (string, error) {
	g.BindingDigest = ""
	g.ExecutionAllowed = false
	g.PreparationOnly = false
	encoded, err := retryCanonical(g)
	if err != nil {
		return "", err
	}
	return retryDigest("arcentra-job-generation-v1", encoded), nil
}

// Validate 验证固定输入、原认领、完整计划与独立运行时认领的版本化绑定
func (g JobGeneration) Validate() error {
	i := JobInputIdentity{PipelineID: g.PipelineID, PipelineRunID: g.PipelineRunID, JobRunID: g.JobRunID, AgentID: g.AgentID, ClaimID: g.OriginalClaimID, RuntimeBindingDigest: g.RuntimeBindingDigest, SourceBindingDigest: g.SourceBindingDigest}
	if err := i.Validate(); err != nil {
		return err
	}
	if g.Version != 1 || g.Number < 1 || g.Number > 5 || !canonicalUUID(g.GenerationID) || !canonicalUUID(g.RuntimeClaimID) || (g.InputManifestDigest == "" && g.Number != 1 || g.InputManifestDigest != "" && !sourceprovenance.ValidBindingDigest(g.InputManifestDigest)) || !sourceprovenance.ValidBindingDigest(g.RetryPlanDigest) || g.NotBeforeUnixMilli < 0 {
		return fmt.Errorf("invalid generation identity")
	}
	if (g.Number == 1 && g.PreviousGenerationID != "") || (g.Number > 1 && (!canonicalUUID(g.PreviousGenerationID) || g.PreviousGenerationID == g.GenerationID)) {
		return fmt.Errorf("invalid generation predecessor")
	}
	digest, err := g.Digest()
	if err != nil || digest != g.BindingDigest {
		return fmt.Errorf("generation binding mismatch")
	}
	return nil
}

// Canonical 返回已验证代次的规范编码
func (g JobGeneration) Canonical() (string, error) {
	if err := g.Validate(); err != nil {
		return "", err
	}
	return retryCanonical(g)
}

// ParseJobGeneration 读取规范代次响应并验证完整绑定
func ParseJobGeneration(encoded string) (*JobGeneration, error) {
	if len(encoded) > 16384 {
		return nil, fmt.Errorf("generation exceeds limit")
	}
	var g JobGeneration
	if err := sonic.UnmarshalString(encoded, &g); err != nil {
		return nil, fmt.Errorf("invalid generation encoding")
	}
	canonical, err := g.Canonical()
	if err != nil || canonical != encoded {
		return nil, fmt.Errorf("generation is not canonical")
	}
	return &g, nil
}

// JobGenerationCleanupReceipt 是原 Agent 对准确资源的清理证据，不是独立远程证明
// 容器后端没有独立创建租约，因此协议不虚构租约清理事实
type JobGenerationCleanupReceipt struct {
	Version                 int    `json:"version"`
	PipelineRunID           string `json:"pipelineRunId"`
	JobRunID                string `json:"jobRunId"`
	AgentID                 string `json:"agentId"`
	OriginalClaimID         string `json:"originalClaimId"`
	RuntimeBindingDigest    string `json:"runtimeBindingDigest"`
	GenerationID            string `json:"generationId"`
	GenerationBindingDigest string `json:"generationBindingDigest"`
	RuntimeClaimID          string `json:"runtimeClaimId"`
	SessionID               string `json:"sessionId"`
	Namespace               string `json:"namespace"`
	ContainerID             string `json:"containerId"`
	SnapshotID              string `json:"snapshotId"`
	Snapshotter             string `json:"snapshotter"`
	ResourceOwnerDigest     string `json:"resourceOwnerDigest"`
	WorkspaceID             string `json:"workspaceId"`
	WorkspaceDevice         uint64 `json:"workspaceDevice"`
	WorkspaceInode          uint64 `json:"workspaceInode"`
	ProcessesAbsent         bool   `json:"processesAbsent"`
	ContainerAbsent         bool   `json:"containerAbsent"`
	SnapshotAbsent          bool   `json:"snapshotAbsent"`
	WorkspaceAbsent         bool   `json:"workspaceAbsent"`
}

// Canonical 只接受完整成功清理及由控制面运行时认领派生的准确资源身份
func (r JobGenerationCleanupReceipt) Canonical() (string, error) {
	if r.Version != 1 || !canonicalUUID(r.GenerationID) || !canonicalUUID(r.RuntimeClaimID) || r.SessionID != r.RuntimeClaimID || r.ContainerID != "arcentra-"+r.RuntimeClaimID || r.SnapshotID != r.ContainerID+"-snapshot" || r.WorkspaceID != "job-"+r.RuntimeClaimID || r.WorkspaceInode == 0 || !r.ProcessesAbsent || !r.ContainerAbsent || !r.SnapshotAbsent || !r.WorkspaceAbsent {
		return "", fmt.Errorf("complete exact generation cleanup evidence is required")
	}
	for _, id := range []string{r.PipelineRunID, r.JobRunID, r.AgentID, r.OriginalClaimID} {
		if artifactutil.Identity(id) != nil {
			return "", fmt.Errorf("invalid cleanup identity")
		}
	}
	for _, value := range []string{r.Namespace, r.Snapshotter} {
		if value == "" || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n/\\") {
			return "", fmt.Errorf("invalid runtime resource namespace")
		}
		for _, c := range value {
			if c < 32 || c == 127 {
				return "", fmt.Errorf("invalid runtime resource namespace")
			}
		}
	}
	if !sourceprovenance.ValidBindingDigest(r.RuntimeBindingDigest) || !sourceprovenance.ValidBindingDigest(r.GenerationBindingDigest) || artifactutil.Digest(r.ResourceOwnerDigest) != nil {
		return "", fmt.Errorf("invalid cleanup binding")
	}
	return retryCanonical(r)
}

// Digest 固定全部准确资源及清理事实，重放必须保持逐字节相同
func (r JobGenerationCleanupReceipt) Digest() (string, error) {
	s, err := r.Canonical()
	if err != nil {
		return "", err
	}
	return retryDigest("arcentra-generation-cleanup-v1", s), nil
}

// ValidateGeneration 验证清理证据只属于已颁发的前序代次
func (r JobGenerationCleanupReceipt) ValidateGeneration(g JobGeneration) error {
	if _, err := r.Canonical(); err != nil {
		return err
	}
	if err := g.Validate(); err != nil {
		return err
	}
	if r.PipelineRunID != g.PipelineRunID || r.JobRunID != g.JobRunID || r.AgentID != g.AgentID || r.OriginalClaimID != g.OriginalClaimID || r.RuntimeBindingDigest != g.RuntimeBindingDigest || r.GenerationID != g.GenerationID || r.GenerationBindingDigest != g.BindingDigest || r.RuntimeClaimID != g.RuntimeClaimID {
		return fmt.Errorf("cleanup receipt belongs to a different generation")
	}
	return nil
}

// JobGenerationTransition 固定一个首次准入、前进或只读检查请求的完整身份
// Agent 只引用已授权计划及已提交失败回执，不能携带新的执行策略
type JobGenerationTransition struct {
	Version                 int                          `json:"version"`
	Identity                JobInputIdentity             `json:"identity"`
	InputManifestDigest     string                       `json:"inputManifestDigest"`
	RetryPlanDigest         string                       `json:"retryPlanDigest"`
	Operation               string                       `json:"operation"`
	PreviousGenerationID    string                       `json:"previousGenerationId"`
	FailedStepRunID         string                       `json:"failedStepRunId"`
	FailedStepReceiptDigest string                       `json:"failedStepReceiptDigest"`
	CleanupReceipt          *JobGenerationCleanupReceipt `json:"cleanupReceipt"`
	RequestDigest           string                       `json:"requestDigest"`
}

// Digest 计算稳定的完整转换请求摘要，不包含其自身摘要字段
func (t JobGenerationTransition) Digest() (string, error) {
	t.RequestDigest = ""
	s, err := retryCanonical(t)
	if err != nil {
		return "", err
	}
	return retryDigest("arcentra-job-generation-transition-v1", s), nil
}

// Validate 拒绝不完整、越权或混合首次准入和后继推进的转换
func (t JobGenerationTransition) Validate() error {
	if t.Version != 1 {
		return fmt.Errorf("unsupported transition version")
	}
	if err := t.Identity.Validate(); err != nil {
		return err
	}
	if (t.InputManifestDigest == "" && t.Operation == "advance" || t.InputManifestDigest != "" && !sourceprovenance.ValidBindingDigest(t.InputManifestDigest)) || !sourceprovenance.ValidBindingDigest(t.RetryPlanDigest) {
		return fmt.Errorf("input and retry plan bindings are required")
	}
	switch t.Operation {
	case "admit", "inspect":
		if t.PreviousGenerationID != "" || t.FailedStepRunID != "" || t.FailedStepReceiptDigest != "" || t.CleanupReceipt != nil {
			return fmt.Errorf("initial admission or inspect cannot carry advancement evidence")
		}
	case "advance":
		if !canonicalUUID(t.PreviousGenerationID) || artifactutil.Identity(t.FailedStepRunID) != nil || artifactutil.Digest(t.FailedStepReceiptDigest) != nil || t.CleanupReceipt == nil {
			return fmt.Errorf("advancement requires exact prior generation, failure and cleanup")
		}
		if _, err := t.CleanupReceipt.Canonical(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported generation operation")
	}
	digest, err := t.Digest()
	if err != nil || !strings.EqualFold(digest, t.RequestDigest) || digest != t.RequestDigest {
		return fmt.Errorf("transition digest mismatch")
	}
	return nil
}

// Canonical 返回经过完整校验的稳定转换编码
func (t JobGenerationTransition) Canonical() (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	return retryCanonical(t)
}

// ParseJobGenerationTransition 只允许有界规范编码，未知字段不能改变请求含义
func ParseJobGenerationTransition(encoded string) (*JobGenerationTransition, error) {
	if len(encoded) > 32768 {
		return nil, fmt.Errorf("generation transition exceeds limit")
	}
	var t JobGenerationTransition
	if err := sonic.UnmarshalString(encoded, &t); err != nil {
		return nil, fmt.Errorf("invalid transition encoding")
	}
	canonical, err := t.Canonical()
	if err != nil || canonical != encoded {
		return nil, fmt.Errorf("generation transition is not canonical")
	}
	return &t, nil
}
