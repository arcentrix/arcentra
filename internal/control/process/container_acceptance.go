//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package process

import (
	"fmt"
	"os"

	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

// ConfigureContainerAcceptancePolicy 仅在专用验收构建注入单一固定镜像策略
// 普通发行构建没有此方法；不会改变生产门禁或已提交作业的策略
func (e *Process) ConfigureContainerAcceptancePolicy(policy taskqueue.OfflineRuntimePolicy) error {
	if os.Getenv("ARCENTRA_CONTAINERD_ACCEPTANCE") != "1" || os.Getenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE") != "1" {
		return fmt.Errorf("explicit dedicated container acceptance authorization is required")
	}
	if taskqueue.OfflineRuntimeAccepted || !policy.Enabled || len(policy.AllowedImages) != 1 || policy.AllowedImages[0] != os.Getenv("ARCENTRA_SANDBOX_TEST_IMAGE") {
		return fmt.Errorf("acceptance injection is restricted to the gated single-image test profile")
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	active := false
	e.runs.Range(func(_, _ any) bool { active = true; return false })
	if e.stopped || active || e.offlineRuntimePolicy.Enabled {
		return fmt.Errorf("acceptance policy must be injected once before submitting work")
	}
	e.offlineRuntimePolicy = policy.Clone()
	return nil
}
