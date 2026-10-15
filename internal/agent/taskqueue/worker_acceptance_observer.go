//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"

	"github.com/arcentrix/arcentra/pkg/nova"
)

// The attachment is private and per worker. Only same-package acceptance tests
// install the test-only collector; no runtime setting enables this seam.
type acceptanceWorkerObserverKey struct{}

type acceptanceWorkerObserver interface {
	wrap(nova.IHandler) nova.HandlerFunc
	backgroundContext(context.Context) context.Context
}

func observeAcceptanceWorkerHandler(ctx context.Context, handler nova.HandlerFunc) nova.HandlerFunc {
	if observer, ok := ctx.Value(acceptanceWorkerObserverKey{}).(acceptanceWorkerObserver); ok {
		return observer.wrap(handler)
	}
	return handler
}

// Only the periodic replay's derived context is marked. Startup replay and the
// incoming Kafka handler context retain their own provenance and cancellation.
func observeAcceptanceWorkerReplay(ctx context.Context) context.Context {
	if observer, ok := ctx.Value(acceptanceWorkerObserverKey{}).(acceptanceWorkerObserver); ok {
		return observer.backgroundContext(ctx)
	}
	return ctx
}
