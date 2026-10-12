// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

// Package sourceprovenance 绑定凭据隔离的作业初始检出身份
package sourceprovenance

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// Binding 保存不可变的源代码请求及其版本化摘要，不包含认证信息
// Repository 仅用于身份比较，调用者不能用它替换实际派发的仓库参数
// v1 不支持指定提交；有绑定的任务必须拒绝额外的 Commit 参数
// 该身份仅证明 Agent 上报的初始检出，不证明后续工作区或构建产物
type Binding struct {
	PipelineID    string
	PipelineRunID string
	JobRunID      string
	Type          string
	Repository    string
	Ref           string
	Digest        string
}

// NewBinding 为同一次作业的实际派发源参数创建稳定、无凭据的绑定
func NewBinding(pipelineID, runID, jobID, sourceType, repository, ref string) (*Binding, error) {
	for _, id := range []string{pipelineID, runID, jobID} {
		if !safeText(id, 64) || id == "" {
			return nil, fmt.Errorf("source execution identity is invalid")
		}
	}
	if sourceType == "" || strings.TrimSpace(sourceType) != sourceType {
		return nil, fmt.Errorf("source type is required")
	}
	sourceType = strings.ToLower(sourceType)
	if !safeText(sourceType, 32) || strings.ContainsAny(sourceType, " /:@") {
		return nil, fmt.Errorf("source type is invalid")
	}
	canonical, err := CanonicalRepository(repository)
	if err != nil {
		return nil, err
	}
	if !safeText(ref, 512) {
		return nil, fmt.Errorf("source ref is invalid")
	}
	b := &Binding{PipelineID: pipelineID, PipelineRunID: runID, JobRunID: jobID, Type: sourceType, Repository: canonical, Ref: ref}
	h := sha256.New()
	for _, value := range []string{"arcentra-source-binding-v1", b.PipelineID, b.PipelineRunID, b.JobRunID, b.Type, b.Repository, b.Ref} {
		_, _ = h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(value))))
		_, _ = h.Write([]byte(value))
	}
	b.Digest = "v1:" + hex.EncodeToString(h.Sum(nil))
	return b, nil
}

// CanonicalRepository 校验仓库身份，拒绝内嵌认证及查询参数并保留路径大小写
func CanonicalRepository(repository string) (string, error) {
	if repository == "" || !safeText(repository, 2048) {
		return "", fmt.Errorf("source repository identity is invalid")
	}
	if strings.ContainsAny(repository, "?#") {
		return "", fmt.Errorf("source repository must not contain query or fragment")
	}
	if strings.Contains(repository, "://") {
		u, err := url.Parse(repository)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return "", fmt.Errorf("source repository must not contain embedded authentication")
		}
		u.Scheme = strings.ToLower(u.Scheme)
		switch u.Scheme {
		case "https", "http", "ssh", "git":
			if u.Host == "" {
				return "", fmt.Errorf("source repository host is required")
			}
		case "file":
			if u.Host != "" && u.Host != "localhost" {
				return "", fmt.Errorf("source file repository host is invalid")
			}
		default:
			return "", fmt.Errorf("source repository scheme is unsupported")
		}
		if u.Path == "" {
			return "", fmt.Errorf("source repository path is required")
		}
		u.Host = strings.ToLower(u.Host)
		return u.String(), nil
	}
	// 常规 Git SSH 用户名不是凭据；其他嵌入认证形式不能成为持久化身份
	if strings.Contains(repository, "@") {
		if !strings.HasPrefix(repository, "git@") || strings.Count(repository, "@") != 1 || !strings.Contains(strings.TrimPrefix(repository, "git@"), ":") {
			return "", fmt.Errorf("source repository must not contain embedded authentication")
		}
	}
	return repository, nil
}

func safeText(value string, limit int) bool {
	if len(value) > limit || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// ValidCommit 检查完整、规范小写的 Git SHA-1 或 SHA-256 提交标识
func ValidCommit(value string) bool { return (len(value) == 40 || len(value) == 64) && validHex(value) }

// ValidBindingDigest 检查 v1 源绑定摘要格式
func ValidBindingDigest(value string) bool {
	return len(value) == 67 && strings.HasPrefix(value, "v1:") && validHex(value[3:])
}

func validHex(value string) bool {
	if strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
