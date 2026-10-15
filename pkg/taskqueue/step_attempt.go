// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package taskqueue

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

// StepAttemptAdmissionLifetime 为新 RUNNING 和 Resume 保留固定启动授权窗口，不缩短已开始命令的作业或步骤期限
const StepAttemptAdmissionLifetime = 15 * time.Minute

// PausedStepBoundaryLifetime 限制暂停边界审计的最大寿命，包含最长一小时重试延迟
const PausedStepBoundaryLifetime = time.Hour + StepAttemptAdmissionLifetime

// MaxStepAttemptContractBytes 限制单个不可变步骤协议记录的大小
const MaxStepAttemptContractBytes = 16384

// StepAttemptSession 固定原会话资源身份，不包含任何可恢复的活动能力或秘密
type StepAttemptSession struct {
	SessionID               string `json:"sessionId"`
	SessionIncarnationNonce string `json:"sessionIncarnationNonce"`
	Namespace               string `json:"namespace"`
	ContainerID             string `json:"containerId"`
	SnapshotID              string `json:"snapshotId"`
	Snapshotter             string `json:"snapshotter"`
	ResourceOwnerDigest     string `json:"resourceOwnerDigest"`
	WorkspaceID             string `json:"workspaceId"`
	WorkspaceDevice         uint64 `json:"workspaceDevice"`
	WorkspaceInode          uint64 `json:"workspaceInode"`
}

// Validate 验证准确的原代次资源与非秘密会话化身
func (s StepAttemptSession) Validate(runtimeClaim string) error {
	if !canonicalUUID(runtimeClaim) || s.SessionID != runtimeClaim || !canonicalUUID(s.SessionIncarnationNonce) || s.ContainerID != "arcentra-"+runtimeClaim || s.SnapshotID != s.ContainerID+"-snapshot" || s.WorkspaceID != "job-"+runtimeClaim || s.WorkspaceInode == 0 || artifactutil.Digest(s.ResourceOwnerDigest) != nil {
		return fmt.Errorf("exact original step session identity is required")
	}
	for _, value := range []string{s.Namespace, s.Snapshotter} {
		if value == "" || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n/\\") {
			return fmt.Errorf("invalid step session namespace")
		}
		for _, c := range value {
			if c < 32 || c == 127 {
				return fmt.Errorf("invalid step session namespace")
			}
		}
	}
	return nil
}

// StepAttemptIdentity 固定一个预分配尝试的完整原作业、代次、输入、计划和会话范围
type StepAttemptIdentity struct {
	Identity                JobInputIdentity   `json:"identity"`
	GenerationID            string             `json:"generationId"`
	GenerationBindingDigest string             `json:"generationBindingDigest"`
	RuntimeClaimID          string             `json:"runtimeClaimId"`
	InputManifestDigest     string             `json:"inputManifestDigest"`
	RetryPlanDigest         string             `json:"retryPlanDigest"`
	Session                 StepAttemptSession `json:"session"`
	LogicalStepRunID        string             `json:"logicalStepRunId"`
	StepRunID               string             `json:"stepRunId"`
	StepName                string             `json:"stepName"`
	StepIndex               int32              `json:"stepIndex"`
	DefinitionDigest        string             `json:"definitionDigest"`
	Attempt                 int                `json:"attempt"`
	StepClaimID             string             `json:"stepClaimId"`
}

// StepAttemptClaimID 派生与原作业认领绑定的步骤认领，保持既有步骤认领算法
func StepAttemptClaimID(jobClaim, stepRunID string) string {
	digest := sha256.New()
	for _, value := range []string{"arcentra-job-step-claim-v1", jobClaim, stepRunID} {
		_, _ = digest.Write(binary.BigEndian.AppendUint64(nil, uint64(len(value))))
		_, _ = digest.Write([]byte(value))
	}
	return uuid.NewHash(sha256.New(), uuid.NameSpaceOID, digest.Sum(nil), 8).String()
}

// Validate 验证有界身份并拒绝使用会话认领替代原步骤认领
func (s StepAttemptIdentity) Validate() error {
	if err := s.Identity.Validate(); err != nil {
		return err
	}
	if !canonicalUUID(s.GenerationID) || !sourceprovenance.ValidBindingDigest(s.GenerationBindingDigest) || !sourceprovenance.ValidBindingDigest(s.InputManifestDigest) || !sourceprovenance.ValidBindingDigest(s.RetryPlanDigest) || !sourceprovenance.ValidBindingDigest(s.DefinitionDigest) || artifactutil.Identity(s.LogicalStepRunID) != nil || artifactutil.Identity(s.StepRunID) != nil || s.StepName == "" || len(s.StepName) > 256 || strings.TrimSpace(s.StepName) != s.StepName || s.StepIndex < 0 || s.Attempt < 1 || s.Attempt > 5 || s.StepClaimID != StepAttemptClaimID(s.Identity.ClaimID, s.StepRunID) {
		return fmt.Errorf("invalid immutable step attempt identity")
	}
	if (s.Attempt == 1) != (s.StepRunID == s.LogicalStepRunID) {
		return fmt.Errorf("logical step root must be its first attempt")
	}
	return s.Session.Validate(s.RuntimeClaimID)
}

// PausedStepBoundary 记录可信原 Agent 的暂停观察，不是可序列化活动证明或远程证明
type PausedStepBoundary struct {
	Version                      int                 `json:"version"`
	Scope                        StepAttemptIdentity `json:"scope"`
	BoundaryNonce                string              `json:"boundaryNonce"`
	BoundarySequence             uint64              `json:"boundarySequence"`
	FailedAdmissionID            string              `json:"failedAdmissionId"`
	FailedAdmissionBindingDigest string              `json:"failedAdmissionBindingDigest"`
	FailedStepReceiptDigest      string              `json:"failedStepReceiptDigest"`
	CapturedUnixMilli            int64               `json:"capturedUnixMilli"`
	ExpiresUnixMilli             int64               `json:"expiresUnixMilli"`
	FailedExecExitObserved       bool                `json:"failedExecExitObserved"`
	ExecReaped                   bool                `json:"execReaped"`
	TaskPaused                   bool                `json:"taskPaused"`
	ExactLiveInitOnly            bool                `json:"exactLiveInitOnly"`
	Childless                    bool                `json:"childless"`
}

// Validate 验证所有暂停事实及固定有界寿命，活动证明仍须由本地运行时独立持有
func (b PausedStepBoundary) Validate() error {
	if err := b.Scope.Validate(); err != nil {
		return err
	}
	if b.Version != 1 || !canonicalUUID(b.BoundaryNonce) || b.BoundarySequence == 0 || !canonicalUUID(b.FailedAdmissionID) || !sourceprovenance.ValidBindingDigest(b.FailedAdmissionBindingDigest) || artifactutil.Digest(b.FailedStepReceiptDigest) != nil || b.CapturedUnixMilli <= 0 || b.ExpiresUnixMilli <= b.CapturedUnixMilli || b.ExpiresUnixMilli-b.CapturedUnixMilli > PausedStepBoundaryLifetime.Milliseconds() || !b.FailedExecExitObserved || !b.ExecReaped || !b.TaskPaused || !b.ExactLiveInitOnly || !b.Childless {
		return fmt.Errorf("complete bounded paused step boundary is required")
	}
	return nil
}

// Canonical 返回严格校验后的暂停审计规范编码
func (b PausedStepBoundary) Canonical() (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	return stepAttemptCanonical(b)
}

// Digest 绑定原失败和全部暂停审计观察
func (b PausedStepBoundary) Digest() (string, error) {
	s, err := b.Canonical()
	if err != nil {
		return "", err
	}
	return retryDigest("arcentra-paused-step-boundary-v1", s), nil
}

// StepAttemptTransition 固定首次准入、紧邻后继推进或只读检查，不接受新的执行策略
type StepAttemptTransition struct {
	Version                        int                 `json:"version"`
	Operation                      string              `json:"operation"`
	Scope                          StepAttemptIdentity `json:"scope"`
	PreviousStepRunID              string              `json:"previousStepRunId"`
	PreviousStepClaimID            string              `json:"previousStepClaimId"`
	PreviousAdmissionID            string              `json:"previousAdmissionId"`
	PreviousAdmissionBindingDigest string              `json:"previousAdmissionBindingDigest"`
	FailedStepReceiptDigest        string              `json:"failedStepReceiptDigest"`
	Boundary                       *PausedStepBoundary `json:"boundary"`
	RequestDigest                  string              `json:"requestDigest"`
}

// Digest 计算步骤转换请求的不可变摘要，不包含自身摘要字段
func (t StepAttemptTransition) Digest() (string, error) {
	t.RequestDigest = ""
	s, err := stepAttemptCanonical(t)
	if err != nil {
		return "", err
	}
	return retryDigest("arcentra-step-attempt-transition-v1", s), nil
}

// Validate 验证完整请求及前序失败绑定，检查请求不能携带推进证据
func (t StepAttemptTransition) Validate() error {
	if t.Version != 1 {
		return fmt.Errorf("unsupported step attempt version")
	}
	if err := t.Scope.Validate(); err != nil {
		return err
	}
	switch t.Operation {
	case "admit", "inspect":
		if t.Operation == "admit" && t.Scope.Attempt != 1 {
			return fmt.Errorf("initial step admission must target attempt one")
		}
		if t.PreviousStepRunID != "" || t.PreviousStepClaimID != "" || t.PreviousAdmissionID != "" || t.PreviousAdmissionBindingDigest != "" || t.FailedStepReceiptDigest != "" || t.Boundary != nil {
			return fmt.Errorf("admission or inspect cannot carry retry evidence")
		}
	case "advance":
		if t.Scope.Attempt < 2 || artifactutil.Identity(t.PreviousStepRunID) != nil || t.PreviousStepClaimID != StepAttemptClaimID(t.Scope.Identity.ClaimID, t.PreviousStepRunID) || !canonicalUUID(t.PreviousAdmissionID) || !sourceprovenance.ValidBindingDigest(t.PreviousAdmissionBindingDigest) || artifactutil.Digest(t.FailedStepReceiptDigest) != nil || t.Boundary == nil {
			return fmt.Errorf("retry requires an exact admitted failed predecessor")
		}
		if err := t.Boundary.Validate(); err != nil {
			return err
		}
		previous := t.Scope
		previous.StepRunID = t.PreviousStepRunID
		previous.StepClaimID = t.PreviousStepClaimID
		previous.Attempt--
		if t.Boundary.Scope != previous || t.Boundary.FailedAdmissionID != t.PreviousAdmissionID || t.Boundary.FailedAdmissionBindingDigest != t.PreviousAdmissionBindingDigest || t.Boundary.FailedStepReceiptDigest != t.FailedStepReceiptDigest {
			return fmt.Errorf("paused boundary belongs to a different failure")
		}
	default:
		return fmt.Errorf("unsupported step attempt operation")
	}
	digest, err := t.Digest()
	if err != nil || digest != t.RequestDigest {
		return fmt.Errorf("step transition digest mismatch")
	}
	return nil
}

// Canonical 返回完整校验的步骤转换规范编码
func (t StepAttemptTransition) Canonical() (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	return stepAttemptCanonical(t)
}

// StepAttemptAdmission 是控制面持久化且不可变的步骤准入描述，不包含执行许可观察
type StepAttemptAdmission struct {
	Version             int                 `json:"version"`
	Scope               StepAttemptIdentity `json:"scope"`
	AdmissionID         string              `json:"admissionId"`
	PreviousAdmissionID string              `json:"previousAdmissionId"`
	RequestDigest       string              `json:"requestDigest"`
	NotBeforeUnixMilli  int64               `json:"notBeforeUnixMilli"`
	ExpiresUnixMilli    int64               `json:"expiresUnixMilli"`
	BindingDigest       string              `json:"bindingDigest"`
}

// Digest 绑定全部不可变准入内容
func (a StepAttemptAdmission) Digest() (string, error) {
	a.BindingDigest = ""
	s, err := stepAttemptCanonical(a)
	if err != nil {
		return "", err
	}
	return retryDigest("arcentra-step-attempt-admission-v1", s), nil
}

// Validate 验证步骤准入身份、前序和固定寿命
func (a StepAttemptAdmission) Validate() error {
	if err := a.Scope.Validate(); err != nil {
		return err
	}
	if a.Version != 1 || !canonicalUUID(a.AdmissionID) || !sourceprovenance.ValidBindingDigest(a.RequestDigest) || (a.Scope.Attempt == 1 && a.PreviousAdmissionID != "") || (a.Scope.Attempt > 1 && !canonicalUUID(a.PreviousAdmissionID)) || a.NotBeforeUnixMilli <= 0 || a.ExpiresUnixMilli <= a.NotBeforeUnixMilli || a.ExpiresUnixMilli-a.NotBeforeUnixMilli > StepAttemptAdmissionLifetime.Milliseconds() {
		return fmt.Errorf("invalid step attempt admission")
	}
	digest, err := a.Digest()
	if err != nil || digest != a.BindingDigest {
		return fmt.Errorf("step admission binding mismatch")
	}
	return nil
}

// ValidateTransition 验证响应逐字段绑定到原始已保存转换请求
func (a StepAttemptAdmission) ValidateTransition(t StepAttemptTransition) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if err := t.Validate(); err != nil {
		return err
	}
	if t.Operation == "inspect" || a.Scope != t.Scope || a.RequestDigest != t.RequestDigest || a.PreviousAdmissionID != t.PreviousAdmissionID {
		return fmt.Errorf("step admission does not match transition")
	}
	return nil
}

// Canonical 返回控制面不可变准入规范编码
func (a StepAttemptAdmission) Canonical() (string, error) {
	if err := a.Validate(); err != nil {
		return "", err
	}
	return stepAttemptCanonical(a)
}

// StepAttemptResult 将历史准入描述与本次新鲜执行许可观察分离
type StepAttemptResult struct {
	Admission        *StepAttemptAdmission
	ExecutionAllowed bool
}

func stepAttemptCanonical(value any) (string, error) {
	s, err := retryCanonical(value)
	if err != nil || len(s) > MaxStepAttemptContractBytes {
		return "", fmt.Errorf("step attempt contract exceeds limit")
	}
	return s, nil
}

// ParseStepAttemptTransition 拒绝未知字段、重复字段和非规范请求
func ParseStepAttemptTransition(encoded string) (*StepAttemptTransition, error) {
	if len(encoded) > MaxStepAttemptContractBytes {
		return nil, fmt.Errorf("step transition exceeds limit")
	}
	var value StepAttemptTransition
	if sonic.UnmarshalString(encoded, &value) != nil {
		return nil, fmt.Errorf("invalid step transition")
	}
	canonical, err := value.Canonical()
	if err != nil || canonical != encoded {
		return nil, fmt.Errorf("step transition is not canonical")
	}
	return &value, nil
}

// ParseStepAttemptAdmission 拒绝未知字段、重复字段和非规范响应
func ParseStepAttemptAdmission(encoded string) (*StepAttemptAdmission, error) {
	if len(encoded) > MaxStepAttemptContractBytes {
		return nil, fmt.Errorf("step admission exceeds limit")
	}
	var value StepAttemptAdmission
	if sonic.UnmarshalString(encoded, &value) != nil {
		return nil, fmt.Errorf("invalid step admission")
	}
	canonical, err := value.Canonical()
	if err != nil || canonical != encoded {
		return nil, fmt.Errorf("step admission is not canonical")
	}
	return &value, nil
}

// ParsePausedStepBoundary 拒绝非规范暂停审计数据，不恢复活动证明
func ParsePausedStepBoundary(encoded string) (*PausedStepBoundary, error) {
	if len(encoded) > MaxStepAttemptContractBytes {
		return nil, fmt.Errorf("paused boundary exceeds limit")
	}
	var value PausedStepBoundary
	if sonic.UnmarshalString(encoded, &value) != nil {
		return nil, fmt.Errorf("invalid paused boundary")
	}
	canonical, err := value.Canonical()
	if err != nil || canonical != encoded {
		return nil, fmt.Errorf("paused boundary is not canonical")
	}
	return &value, nil
}
