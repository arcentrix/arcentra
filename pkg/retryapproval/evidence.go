// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package retryapproval

import (
	"fmt"

	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
)

// Artifact 固定登记制品的生产者和完整存储内容身份
type Artifact struct {
	Name              string                      `json:"name"`
	ProducerJobRunID  string                      `json:"producerJobRunId"`
	ProducerStepRunID string                      `json:"producerStepRunId"`
	StorageKey        string                      `json:"storageKey"`
	Reference         taskqueue.ArtifactReference `json:"reference"`
}

// Evidence 是可安全展示的完整冻结证据，不包含命令参数、环境值或凭据
type Evidence struct {
	Version             int                        `json:"version"`
	Kind                string                     `json:"kind"`
	ApprovalID          string                     `json:"approvalId"`
	Context             RunContext                 `json:"context"`
	Identity            taskqueue.JobInputIdentity `json:"identity"`
	JobName             string                     `json:"jobName"`
	RetryPlan           taskqueue.JobRetryPlan     `json:"retryPlan"`
	RetryPlanDigest     string                     `json:"retryPlanDigest"`
	Runtime             taskqueue.RuntimeRequest   `json:"runtime"`
	Source              *sourceprovenance.Binding  `json:"source"`
	Inputs              taskqueue.JobInputManifest `json:"inputs"`
	InputManifestDigest string                     `json:"inputManifestDigest"`
	Artifacts           []Artifact                 `json:"artifacts"`
	CreatedAtUnixMilli  int64                      `json:"createdAtUnixMilli"`
	ExpiresAtUnixMilli  int64                      `json:"expiresAtUnixMilli"`
}

// Canonical 校验全部交叉绑定、完整有限计划和生产者清单
func (e *Evidence) Canonical() (string, error) {
	if e == nil || e.RetryPlan.Version != 1 || e.RetryPlan.Policy == nil || e.RetryPlan.Policy.Mode != "whole_job" || !e.RetryPlan.RetryApprovalRequired || e.RetryPlan.RetryApprovalTimeoutSeconds != e.Context.Policy.TimeoutSeconds || e.Version != 1 || e.Kind != Kind || !validID(e.ApprovalID) || e.Identity.Validate() != nil || !e.Context.RequiresJob(e.JobName) || e.Context.PipelineID != e.Identity.PipelineID || e.Context.PipelineRunID != e.Identity.PipelineRunID || e.CreatedAtUnixMilli <= 0 || e.ExpiresAtUnixMilli <= e.CreatedAtUnixMilli || e.ExpiresAtUnixMilli > e.CreatedAtUnixMilli+e.Context.Policy.TimeoutSeconds*1000 {
		return "", fmt.Errorf("invalid approval evidence identity")
	}
	if _, err := e.Context.Canonical(); err != nil {
		return "", err
	}
	rd, err := taskqueue.RuntimeBindingDigest(e.Identity.PipelineID, e.Identity.PipelineRunID, e.Identity.JobRunID, e.Identity.AgentID, &e.Runtime)
	if err != nil || rd != e.Runtime.BindingDigest || rd != e.Identity.RuntimeBindingDigest {
		return "", fmt.Errorf("evidence runtime mismatch")
	}
	pd, err := e.RetryPlan.Digest(e.Identity.PipelineID, e.Identity.PipelineRunID, e.Identity.JobRunID, e.Identity.AgentID, rd)
	if err != nil || pd != e.RetryPlanDigest {
		return "", fmt.Errorf("evidence plan mismatch")
	}
	md, err := e.Inputs.Digest(e.Identity)
	if err != nil || md != e.InputManifestDigest {
		return "", fmt.Errorf("evidence input mismatch")
	}
	if e.Source == nil {
		if e.Identity.SourceBindingDigest != "" || e.Inputs.SourceCommit != "" {
			return "", fmt.Errorf("evidence source absence mismatch")
		}
	} else {
		b, err := sourceprovenance.NewBinding(e.Identity.PipelineID, e.Identity.PipelineRunID, e.Identity.JobRunID, e.Source.Type, e.Source.Repository, e.Source.Ref)
		if err != nil || *b != *e.Source || b.Digest != e.Identity.SourceBindingDigest || !sourceprovenance.ValidCommit(e.Inputs.SourceCommit) {
			return "", fmt.Errorf("evidence source mismatch")
		}
	}
	if e.Artifacts == nil || len(e.Artifacts) != len(e.Inputs.Artifacts) {
		return "", fmt.Errorf("evidence producers incomplete")
	}
	for i, a := range e.Artifacts {
		f := e.Inputs.Artifacts[i]
		if !validID(a.ProducerJobRunID) || !validID(a.ProducerStepRunID) || a.ProducerJobRunID == e.Identity.JobRunID || a.Name != f.Name || a.StorageKey != f.Key {
			return "", fmt.Errorf("evidence producer mismatch")
		}
		if expires := a.Reference.ExpiresAt; expires > 0 && (e.ExpiresAtUnixMilli/1000 > expires || (e.ExpiresAtUnixMilli/1000 == expires && e.ExpiresAtUnixMilli%1000 != 0)) {
			return "", fmt.Errorf("approval cannot outlive pinned artifact inputs")
		}
		x, _ := encode(a.Reference)
		y, _ := encode(f.Reference)
		if x != y {
			return "", fmt.Errorf("evidence artifact mismatch")
		}
	}
	return encode(e)
}

// Digest 返回证据域隔离摘要
func (e *Evidence) Digest() (string, error) {
	s, err := e.Canonical()
	if err != nil {
		return "", err
	}
	return digest("arcentra-retry-approval-evidence-v1", s), nil
}

// ParseEvidence 拒绝非规范证据
func ParseEvidence(s string) (*Evidence, error) {
	var e Evidence
	if len(s) > MaxBytes || sonic.UnmarshalString(s, &e) != nil {
		return nil, fmt.Errorf("invalid approval evidence")
	}
	v, err := e.Canonical()
	if err != nil || s != v {
		return nil, fmt.Errorf("noncanonical approval evidence")
	}
	return &e, nil
}

// Review 返回安全冻结证据和当前审批状态，不包含执行授权
type Review struct {
	ApprovalID    string    `json:"approvalId"`
	RetryJobRunID string    `json:"retryJobRunId"`
	EvidenceHash  string    `json:"evidenceHash"`
	Status        int       `json:"status"`
	Evidence      *Evidence `json:"evidence"`
}
