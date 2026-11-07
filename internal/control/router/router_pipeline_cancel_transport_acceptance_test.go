//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import "testing"

// Startup/HTTP wiring coverage only. This does not start a broker, dispatcher,
// container runtime, or imply that hosted terminal-rerun acceptance has passed.
func TestProtectedCancellationRegisteredRouteEnabled(t *testing.T) {
	testRegisteredCancellationRoute(t, true)
}
