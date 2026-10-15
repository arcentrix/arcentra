//go:build !sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"

	"github.com/arcentrix/arcentra/pkg/nova"
)

// Ordinary builds do not inspect context values or install observation.
func observeAcceptanceWorkerHandler(_ context.Context, handler nova.HandlerFunc) nova.HandlerFunc {
	return handler
}

func observeAcceptanceWorkerReplay(ctx context.Context) context.Context {
	return ctx
}
