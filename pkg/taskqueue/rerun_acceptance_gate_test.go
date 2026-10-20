//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import "testing"

func TestTerminalRerunAcceptanceRequiresAllExplicitBounds(t *testing.T) {
	p := runtimeTestPolicy()
	p.AllowWholeJobRetry, p.AllowWholeJobRetryApproval = true, true
	keys := []string{"ARCENTRA_CONTAINERD_ACCEPTANCE", "ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", "ARCENTRA_REQUIRE_TERMINAL_RERUN_ACCEPTANCE"}
	for _, key := range keys {
		t.Setenv(key, "1")
	}
	t.Setenv("ARCENTRA_SANDBOX_TEST_IMAGE", p.AllowedImages[0])
	if !TerminalRerunAllowed(p) {
		t.Fatal("bounded acceptance policy denied")
	}
	for _, key := range keys {
		t.Setenv(key, "")
		if TerminalRerunAllowed(p) {
			t.Fatal("missing guard accepted:", key)
		}
		t.Setenv(key, "1")
	}
	t.Setenv("ARCENTRA_SANDBOX_TEST_IMAGE", "different")
	if TerminalRerunAllowed(p) {
		t.Fatal("image mismatch accepted")
	}
	t.Setenv("ARCENTRA_SANDBOX_TEST_IMAGE", p.AllowedImages[0])
	p.AllowedImages = append(p.AllowedImages, p.AllowedImages[0])
	if TerminalRerunAllowed(p) {
		t.Fatal("multiple images accepted")
	}
}
