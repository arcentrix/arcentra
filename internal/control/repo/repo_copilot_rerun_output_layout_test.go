// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCopilotRerunLayoutAllowsOnlyPostShellOutputPublication(t *testing.T) {
	for _, kind := range []string{"shell-run", "upload", "download", "plugin", "before-shell", "shell-after-upload", "continue", "wrong-action"} {
		t.Run(kind, func(t *testing.T) {
			_, in, _ := rerunValueFixture(t)
			upload := CopilotRerunStepLayout{Name: "publish", Index: 1, Uses: "builtin:artifacts", Action: "upload"}
			in.Layout.Steps = append(in.Layout.Steps, upload)
			switch kind {
			case "shell-run":
				in.Layout.Steps[0].Action = "run"
			case "download":
				in.Layout.Steps[1].Action = "download"
			case "plugin":
				in.Layout.Steps[1].Uses = "plugin:artifacts"
			case "before-shell":
				in.Layout.Steps[0].Uses = "builtin:artifacts"
				in.Layout.Steps[0].Action = "upload"
			case "shell-after-upload":
				in.Layout.Steps = append(in.Layout.Steps, CopilotRerunStepLayout{Name: "later", Index: 2, Uses: "shell"})
			case "continue":
				in.Layout.Steps[1].ContinueOnError = true
			case "wrong-action":
				in.Layout.Steps[0].Action = "deploy"
			}
			body, err := in.Layout.Canonical()
			if kind == "upload" || kind == "shell-run" {
				require.NoError(t, err)
				round, err := ParseCopilotRerunLayout(body)
				require.NoError(t, err)
				require.Equal(t, in.Layout, *round)
			} else {
				require.ErrorIs(t, err, ErrCopilotRerunInvalid)
			}
		})
	}
}
