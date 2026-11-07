// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package process

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReservedRerunCapabilityTypedDecode(t *testing.T) {
	for _, raw := range []string{`{}`, `{"copilotWholeJobRerunPreparationV1":false}`, `{"copilotWholeJobRerunPreparationV1":true}`} {
		c, err := decodeAgentRuntimeCapabilities([]byte(raw))
		require.NoError(t, err)
		require.Equal(t, raw == `{"copilotWholeJobRerunPreparationV1":true}`, c.CopilotWholeJobRerunPreparationV1)
		require.False(t, c.ContainerdOfflineV1)
	}
	for _, raw := range []string{`{"copilotWholeJobRerunPreparationV1":"true"}`, `{"copilotWholeJobRerunPreparationV1":1}`, `{"copilotWholeJobRerunPreparationV1":null}`} {
		_, err := decodeAgentRuntimeCapabilities([]byte(raw))
		require.Error(t, err)
	}
}
