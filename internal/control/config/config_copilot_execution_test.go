// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCopilotExecutionConfigurationRequiresExplicitOptIn(t *testing.T) {
	for _, tc := range []struct {
		name, contents string
		enabled        bool
	}{
		{"omitted", "[grpc]\nport = 9090\n", false},
		{"history-only", "[copilot]\nenabled = true\n[copilot.history]\nenabled = true\n", false},
		{"explicit-off", "[copilot.execution]\nretryApprovalEnabled = false\n", false},
		{"explicit-on", "[copilot.execution]\nretryApprovalEnabled = true\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "controller.toml")
			require.NoError(t, os.WriteFile(path, []byte(tc.contents), 0600))
			conf, err := NewConf(path)
			require.NoError(t, err)
			require.Equal(t, tc.enabled, conf.Copilot.Execution.RetryApprovalEnabled)
		})
	}
}
