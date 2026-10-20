//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTerminalRerunAcceptanceBundleIsAtomicAndOnceBound(t *testing.T) {
	f := newRerunBridgeFixture(t)
	runtime := rerunSnapshotPolicy()
	for _, key := range []string{"ARCENTRA_CONTAINERD_ACCEPTANCE", "ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", "ARCENTRA_REQUIRE_TERMINAL_RERUN_ACCEPTANCE"} {
		t.Setenv(key, "1")
	}
	t.Setenv("ARCENTRA_SANDBOX_TEST_IMAGE", runtime.AllowedImages[0])
	s := &Services{repos: f.repositories, Agent: &AgentService{}, Secret: NewSecretService(nil), offlineRuntimePolicy: runtime, retryApprovalPolicy: &retryapproval.Policy{ID: "current-policy", Candidates: []string{"actor", "reviewer"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300}}
	var typedNil *rerunDispatchPublisher
	require.Error(t, s.ConfigureTerminalRerun(true, typedNil))
	require.Nil(t, s.TerminalRerunService())
	require.Nil(t, s.ProtectedRunCancellation())
	require.Nil(t, s.Agent.rerunClaim)
	require.Empty(t, s.rerunCapturePolicy)
	require.False(t, s.rerunStartupBound)
	require.NoError(t, s.ConfigureTerminalRerun(true, &rerunDispatchPublisher{}))
	require.NotNil(t, s.TerminalRerunService())
	require.NotNil(t, s.ProtectedRunCancellation())
	require.NotNil(t, s.Agent.rerunClaim)
	require.Equal(t, CopilotRerunCaptureEligibleVerifiedManual, s.rerunCapturePolicy)
	require.Error(t, s.ConfigureTerminalRerun(false, nil))
}
