//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import "testing"

// This is unprivileged startup/HTTP wiring coverage, using the real guarded
// bundle. It does not start a dispatcher, broker, or container runtime and is
// not evidence that hosted terminal-rerun acceptance has passed.
func TestCopilotRerunFullRouteGroupIdentityAndLegacyIsolation(t *testing.T) {
	testCopilotRerunFullRouteGroupIdentityAndLegacyIsolation(t)
}
