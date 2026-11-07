// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package config

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestTerminalRerunConfigurationIsIndependentOptIn(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		enabled    bool
	}{{"omitted", "[grpc]\nport=9090\n", false}, {"model-and-vote", "[copilot]\nenabled=true\n[copilot.execution]\nretryApprovalEnabled=true\n", false}, {"explicit-off", "[copilot.execution]\nterminalRerunEnabled=false\n", false}, {"explicit-on", "[copilot.execution]\nterminalRerunEnabled=true\n", true}} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "control.toml")
			require.NoError(t, os.WriteFile(p, []byte(tc.body), 0600))
			c, e := NewConf(p)
			require.NoError(t, e)
			require.Equal(t, tc.enabled, c.Copilot.Execution.TerminalRerunEnabled)
		})
	}
}
