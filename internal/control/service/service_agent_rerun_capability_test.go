// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"encoding/json"
	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRerunCapabilityRequiresCompleteProtocolChain(t *testing.T) {
	for _, tc := range []struct {
		name, os string
		edit     func(*agentv1.RuntimeCapabilities)
		want     bool
	}{
		{"complete", "linux", func(*agentv1.RuntimeCapabilities) {}, true}, {"no-parent", "linux", func(c *agentv1.RuntimeCapabilities) { c.ContainerdRetryApprovalV1 = false }, false}, {"no-whole-job", "linux", func(c *agentv1.RuntimeCapabilities) { c.ContainerdWholeJobRetryV1 = false }, false}, {"not-ready", "linux", func(c *agentv1.RuntimeCapabilities) { c.ContainerdOfflineV1 = false }, false}, {"windows", "windows", func(*agentv1.RuntimeCapabilities) {}, false}, {"old-agent", "linux", func(c *agentv1.RuntimeCapabilities) { c.CopilotWholeJobRerunPreparationV1 = false }, false}, {"baremetal", "linux", func(c *agentv1.RuntimeCapabilities) { c.ExecutionMode = "baremetal" }, false}} {
		t.Run(tc.name, func(t *testing.T) {
			c := &agentv1.RuntimeCapabilities{ExecutionMode: taskqueue.RuntimeContainerdOfflineV1, ContainerdOfflineV1: true, ContainerdWholeJobRetryV1: true, ContainerdRetryApprovalV1: true, CopilotWholeJobRerunPreparationV1: true}
			tc.edit(c)
			var out taskqueue.AgentRuntimeCapabilities
			require.NoError(t, json.Unmarshal(agentRuntimeCapabilities(c, tc.os), &out))
			require.Equal(t, tc.want, out.CopilotWholeJobRerunPreparationV1)
		})
	}
}
