//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"github.com/arcentrix/arcentra/internal/agent/config"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"os"
)

func failedStepApprovalExecutionReady(ctx context.Context, conf *config.AgentConfig, p *taskpayload.JobRunTaskPayload) bool {
	if taskpayload.FailedStepRetryApprovalAccepted {
		return true
	}
	marker, ok := offlineRuntimeFromContext(ctx).(interface{ failedStepApprovalAcceptance() bool })
	return ok && marker.failedStepApprovalAcceptance() && conf != nil && conf.Agent.Sandbox.Containerd.Policy.AllowFailedStepRetryApproval && p != nil && p.Runtime != nil && p.Runtime.Image == os.Getenv("ARCENTRA_SANDBOX_TEST_IMAGE") && os.Getenv("ARCENTRA_CONTAINERD_ACCEPTANCE") == "1" && os.Getenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE") == "1"
}
