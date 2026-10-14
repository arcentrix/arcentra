// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package taskqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/bytedance/sonic"
)

// MaxJobInputManifestBytes 为默认 gRPC 接收上限保留身份和协议封装空间
const MaxJobInputManifestBytes = 3 * 1024 * 1024

// JobInputArtifact 固定一个已登记制品的完整位置和内容，Size 必须显式存在
type JobInputArtifact struct {
	Name      string            `json:"name"`
	Key       string            `json:"key"`
	Reference ArtifactReference `json:"reference"`
}

// JobInputManifest 保存版本化输入，不包含凭据或可变工作区观察
type JobInputManifest struct {
	Version      int                `json:"version"`
	SourceCommit string             `json:"sourceCommit"`
	Artifacts    []JobInputArtifact `json:"artifacts"`
}

// JobInputIdentity 绑定原派发身份及原执行认领，不改变任务 v1 摘要
type JobInputIdentity struct {
	PipelineID           string `json:"pipelineId"`
	PipelineRunID        string `json:"pipelineRunId"`
	JobRunID             string `json:"jobRunId"`
	AgentID              string `json:"agentId"`
	ClaimID              string `json:"claimId"`
	RuntimeBindingDigest string `json:"runtimeBindingDigest"`
	SourceBindingDigest  string `json:"sourceBindingDigest"`
}

// Validate 拒绝非规范或缺失的输入执行身份
func (i JobInputIdentity) Validate() error {
	for _, value := range []string{i.PipelineID, i.PipelineRunID, i.JobRunID, i.AgentID, i.ClaimID} {
		if artifactutil.Identity(value) != nil || strings.TrimSpace(value) != value {
			return fmt.Errorf("exact input execution identity is required")
		}
	}
	if !sourceprovenance.ValidBindingDigest(i.RuntimeBindingDigest) || (i.SourceBindingDigest != "" && !sourceprovenance.ValidBindingDigest(i.SourceBindingDigest)) {
		return fmt.Errorf("invalid input execution binding")
	}
	return nil
}

// NewJobInputManifest 深拷贝并排序派发制品，拒绝任何缺失的内容元数据
func NewJobInputManifest(commit string, uris map[string]string, refs map[string]ArtifactReference) (*JobInputManifest, error) {
	if len(uris) != len(refs) || len(uris) > 1000 {
		return nil, fmt.Errorf("input artifacts require complete references")
	}
	manifest := &JobInputManifest{Version: 1, SourceCommit: commit, Artifacts: make([]JobInputArtifact, 0, len(uris))}
	for name, key := range uris {
		ref, ok := refs[name]
		if !ok || ref.ValidateIntegrity() != nil {
			return nil, fmt.Errorf("input artifact integrity is unknown")
		}
		size := *ref.Size
		ref.Size = &size
		manifest.Artifacts = append(manifest.Artifacts, JobInputArtifact{Name: name, Key: key, Reference: ref})
	}
	sort.Slice(manifest.Artifacts, func(i, j int) bool { return manifest.Artifacts[i].Name < manifest.Artifacts[j].Name })
	if _, err := manifest.Canonical(); err != nil {
		return nil, err
	}
	return manifest, nil
}

// Canonical 校验确定性清单编码，空提交仅用于控制面派发期望
func (m *JobInputManifest) Canonical() (string, error) {
	if m == nil || m.Version != 1 || m.Artifacts == nil || len(m.Artifacts) > 1000 || (m.SourceCommit != "" && !sourceprovenance.ValidCommit(m.SourceCommit)) {
		return "", fmt.Errorf("invalid input manifest")
	}
	previous := ""
	for _, file := range m.Artifacts {
		if artifactutil.Name(file.Name) != nil || artifactutil.Name(file.Key) != nil || file.Name <= previous || artifactutil.Identity(file.Reference.StorageID) != nil || artifactutil.Digest(file.Reference.Fingerprint) != nil || file.Reference.ExpiresAt < 0 || file.Reference.ValidateIntegrity() != nil {
			return "", fmt.Errorf("invalid or unordered input artifact")
		}
		previous = file.Name
	}
	encoded, err := sonic.Marshal(m)
	if len(encoded) > MaxJobInputManifestBytes {
		return "", fmt.Errorf("input manifest exceeds size limit")
	}
	return string(encoded), err
}

// ParseJobInputManifest 只接受规范编码，拒绝重复字段或未知字段造成的身份歧义
func ParseJobInputManifest(encoded string) (*JobInputManifest, error) {
	if len(encoded) > MaxJobInputManifestBytes {
		return nil, fmt.Errorf("input manifest exceeds size limit")
	}
	var m JobInputManifest
	if err := sonic.UnmarshalString(encoded, &m); err != nil {
		return nil, fmt.Errorf("invalid input manifest encoding")
	}
	canonical, err := m.Canonical()
	if err != nil || canonical != encoded {
		return nil, fmt.Errorf("input manifest is not canonical")
	}
	return &m, nil
}

// Digest 对规范清单及精确执行身份生成独立摘要
func (m *JobInputManifest) Digest(identity JobInputIdentity) (string, error) {
	if err := identity.Validate(); err != nil {
		return "", err
	}
	canonical, err := m.Canonical()
	if err != nil {
		return "", err
	}
	envelope := struct {
		Domain   string           `json:"domain"`
		Identity JobInputIdentity `json:"identity"`
		Manifest string           `json:"manifest"`
	}{"arcentra-job-inputs-v1", identity, canonical}
	encoded, err := sonic.Marshal(envelope)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "v1:" + hex.EncodeToString(sum[:]), nil
}
