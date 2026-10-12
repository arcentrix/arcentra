package deployment

import (
	"fmt"
	"sort"
	"strings"

	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
)

// PipelineApprovalPolicy 仅由操作员配置审批人和门禁限额，DSL 不能覆盖
type PipelineApprovalPolicy struct {
	Approvers      []string `mapstructure:"approvers" json:"approvers"`
	MinApprovals   int      `mapstructure:"minApprovals" json:"minApprovals"`
	TimeoutSeconds int64    `mapstructure:"timeoutSeconds" json:"timeoutSeconds"`
}

// ValidatePipelineApprovalPolicy 校验操作员门禁配置，无配置表示未启用门禁
func ValidatePipelineApprovalPolicy(policy *PipelineApprovalPolicy) error {
	if policy == nil {
		return nil
	}
	if len(policy.Approvers) < 1 || len(policy.Approvers) > 100 || policy.MinApprovals < 1 || policy.MinApprovals > len(policy.Approvers) || policy.TimeoutSeconds < 1 || policy.TimeoutSeconds > 3600 {
		return fmt.Errorf("invalid operator deployment approval policy")
	}
	seen := map[string]bool{}
	for _, id := range policy.Approvers {
		if artifactutil.Identity(id) != nil || strings.TrimSpace(id) != id || len(id) > 64 || seen[id] {
			return fmt.Errorf("invalid operator deployment approval candidates")
		}
		seen[id] = true
	}
	return nil
}

// ClonePipelineApprovalPolicy 复制并规范化配置，避免外部切片修改审批身份
func ClonePipelineApprovalPolicy(policy *PipelineApprovalPolicy) *PipelineApprovalPolicy {
	if policy == nil {
		return nil
	}
	copy := *policy
	copy.Approvers = append([]string(nil), policy.Approvers...)
	sort.Strings(copy.Approvers)
	return &copy
}
