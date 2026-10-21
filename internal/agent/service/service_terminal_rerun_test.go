// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestTerminalRerunCapabilityRequiresIndependentOptInAndLiveReadiness(t *testing.T) {
	image := "docker.io/library/alpine@sha256:" + strings.Repeat("a", 64)
	for _, key := range []string{"ARCENTRA_CONTAINERD_ACCEPTANCE", "ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", "ARCENTRA_REQUIRE_TERMINAL_RERUN_ACCEPTANCE"} {
		t.Setenv(key, "1")
	}
	t.Setenv("ARCENTRA_SANDBOX_TEST_IMAGE", image)
	conf := &config.AgentConfig{Agent: config.AgentInfo{Mode: "sandbox"}}
	conf.Agent.Sandbox.Enable = true
	conf.Agent.Sandbox.Containerd.Policy = taskqueue.OfflineRuntimePolicy{Enabled: true, AllowedImages: []string{image}, MaxCPU: "2", MaxMemory: "1Gi", AllowWholeJobRetry: true, AllowWholeJobRetryApproval: true}
	s := NewAgentServiceImpl(conf, nil, nil)
	ready := true
	s.SetOfflineRuntimeReadiness(func() bool { return ready })
	require.False(t, s.runtimeCapabilitiesLocked().CopilotWholeJobRerunPreparationV1)
	conf.Agent.Sandbox.Containerd.TerminalRerunEnabled = true
	require.Equal(t, taskqueue.TerminalRerunAllowed(conf.Agent.Sandbox.Containerd.Policy), s.runtimeCapabilitiesLocked().CopilotWholeJobRerunPreparationV1)
	ready = false
	require.False(t, s.runtimeCapabilitiesLocked().CopilotWholeJobRerunPreparationV1)
	ready = true
	conf.Agent.Sandbox.Containerd.Policy.AllowWholeJobRetryApproval = false
	require.False(t, s.runtimeCapabilitiesLocked().CopilotWholeJobRerunPreparationV1)
	conf.Agent.Sandbox.Containerd.Policy.AllowWholeJobRetryApproval = true
	conf.Agent.Mode = "baremetal"
	require.False(t, s.runtimeCapabilitiesLocked().CopilotWholeJobRerunPreparationV1)
}
