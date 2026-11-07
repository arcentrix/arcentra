// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"testing"
	"time"
)

func TestCopilotDefinitionLayoutRetainsBoundedArtifactOutputs(t *testing.T) {
	for _, kind := range []string{"upload", "download", "unknown-plugin", "upload-before-shell", "shell-after-upload", "continue-on-error", "outside-workspace", "unknown-args"} {
		t.Run(kind, func(t *testing.T) {
			parsed := rerunSnapshotServiceSpec()
			output := &pipelinev1.Step{Name: "publish", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"paths": []any{"dist/result.bin"}})}
			job := parsed.Jobs[0]
			job.Steps = append(job.Steps, output)
			switch kind {
			case "download":
				output.Action = "download"
			case "unknown-plugin":
				output.Uses = "plugin:artifacts"
			case "upload-before-shell":
				job.Steps = []*pipelinev1.Step{output, job.Steps[0]}
			case "shell-after-upload":
				job.Steps = append(job.Steps, &pipelinev1.Step{Name: "later", Uses: "shell", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"run": "true"})})
			case "continue-on-error":
				output.ContinueOnError = true
			case "outside-workspace":
				output.Args = spec.MapAsStruct(map[string]any{"paths": []any{"../result.bin"}})
			case "unknown-args":
				output.Args = spec.MapAsStruct(map[string]any{"paths": []any{"dist/result.bin"}, "endpoint": "https://untrusted.invalid"})
			}
			layout, err := copilotDefinitionLayout(parsed, rerunSnapshotPolicy(), 300)
			if kind == "upload" {
				require.NoError(t, err)
				require.Equal(t, "builtin:artifacts", layout.Steps[len(layout.Steps)-1].Uses)
				require.Equal(t, "upload", layout.Steps[len(layout.Steps)-1].Action)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestCopilotDefinitionSnapshotOutputRoundTrip(t *testing.T) {
	svc, p, q, receipt, parsed, _, key, _ := rerunSnapshotLoadFixture(t)
	parsed.Jobs[0].Steps[0].Action = "run"
	parsed.Jobs[0].Steps = append(parsed.Jobs[0].Steps, &pipelinev1.Step{Name: "publish", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"paths": []any{"dist/result.bin"}})})
	var err error
	receipt.resolvedSpec, err = (proto.MarshalOptions{Deterministic: true}).Marshal(parsed)
	require.NoError(t, err)
	run := rerunSnapshotRun(t, p, q, parsed, receipt)
	snapshot, err := buildCopilotDefinitionSnapshot(receipt, p, q, run, parsed, svc.offlineRuntimePolicy, key, time.Now())
	require.NoError(t, err)
	decoded, _, _, err := openCopilotDefinitionSnapshot(snapshot, svc.offlineRuntimePolicy, key)
	require.NoError(t, err)
	require.True(t, proto.Equal(parsed, decoded))
	require.NotContains(t, string(snapshot.PayloadCiphertext), "dist/result.bin")
}
