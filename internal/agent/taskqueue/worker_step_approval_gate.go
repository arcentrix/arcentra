//go:build !sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"github.com/arcentrix/arcentra/internal/agent/config"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
)

func failedStepApprovalExecutionReady(_ context.Context, _ *config.AgentConfig, _ *taskpayload.JobRunTaskPayload) bool {
	return taskpayload.FailedStepRetryApprovalAccepted
}
