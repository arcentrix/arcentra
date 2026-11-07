//go:build !sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import "testing"

func TestTerminalRerunReleaseGateCannotBeEnabledByEnvironment(t *testing.T) {
	p := runtimeTestPolicy()
	p.AllowWholeJobRetry, p.AllowWholeJobRetryApproval = true, true
	for _, key := range []string{"ARCENTRA_CONTAINERD_ACCEPTANCE", "ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", "ARCENTRA_REQUIRE_TERMINAL_RERUN_ACCEPTANCE"} {
		t.Setenv(key, "1")
	}
	t.Setenv("ARCENTRA_SANDBOX_TEST_IMAGE", p.AllowedImages[0])
	if err := p.ValidateProduction(); err != nil {
		t.Fatal(err)
	}
	if terminalRerunAcceptanceBuild(p) {
		t.Fatal("ordinary binary exposed acceptance bypass")
	}
	if !TerminalRerunAccepted && TerminalRerunAllowed(p) {
		t.Fatal("closed release gate enabled")
	}
}
