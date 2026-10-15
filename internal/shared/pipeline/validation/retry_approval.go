// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/protobuf/proto"
)

// IsDurableRetryApproval 只识别经过完整运行时策略验证的精确人工门禁
func IsDurableRetryApproval(job *spec.Job, policy taskqueue.OfflineRuntimePolicy) bool {
	if job == nil || job.Approval == nil || !policy.AllowWholeJobRetryApproval || !policy.AllowWholeJobRetry || !policy.Enabled {
		return false
	}
	a := job.Approval
	return a.Required && a.Type == "manual" && a.Plugin == "builtin:retry-approval" && len(spec.StructAsMap(a.Params)) == 0 && taskqueue.ValidateWholeJobRetryPolicy(job.Retry) == nil
}

// RetryApprovalJobNames 返回定义中需要持久审批的完整作业集合，拒绝错误拼写或不支持的上下文
func RetryApprovalJobNames(p *spec.Pipeline, policy taskqueue.OfflineRuntimePolicy) ([]string, error) {
	if p == nil {
		return nil, fmt.Errorf("pipeline is required")
	}
	var names []string
	for _, j := range p.Jobs {
		if j == nil || j.Approval == nil || j.Approval.Plugin != "builtin:retry-approval" {
			continue
		}
		if p.Runtime == nil || p.Runtime.Type != taskqueue.RuntimeContainerdOfflineV1 || !IsDurableRetryApproval(j, policy) {
			return nil, fmt.Errorf("durable retry approval requires exact manual gate and enabled offline whole_job policy")
		}
		names = append(names, j.Name)
	}
	return names, nil
}

// ResolvedDefinitionDigest 对完整解析后的定义生成确定性摘要，不持久化任何秘密
func ResolvedDefinitionDigest(p *spec.Pipeline) (string, error) {
	if p == nil {
		return "", fmt.Errorf("pipeline is required")
	}
	data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("arcentra-resolved-definition-v1\x00"), data...))
	return "v1:" + hex.EncodeToString(sum[:]), nil
}
