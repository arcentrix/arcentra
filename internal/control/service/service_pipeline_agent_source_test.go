// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/stretchr/testify/require"
)

func TestAgentSSHSourceRejectedBeforeManualRunAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, source    string
		remote, allowed bool
	}{
		{"ssh-agent", "ssh://git@example.invalid/repo", true, false},
		{"scp-agent", "git@example.invalid:repo", true, false},
		{"https-agent", "https://example.invalid/repo", true, true},
		{"native-git-agent", "git://example.invalid/repo", true, true},
		{"ssh-local", "ssh://git@example.invalid/repo", false, true},
		{"scp-local", "git@example.invalid:repo", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := tc.source
			parsed := &pipelinev1.Spec{Namespace: "baremetal", Jobs: []*pipelinev1.Job{{Name: "build", Source: &pipelinev1.Source{Type: "git", Repo: source}, Steps: []*pipelinev1.Step{{Name: "local-looking", Uses: "shell", Args: spec.MapAsStruct(map[string]any{"run": "echo first"})}, {Name: "agent", Uses: "shell", RunOnAgent: tc.remote, Args: spec.MapAsStruct(map[string]any{"run": "echo second"})}}}}}
			adapter, repositories, _, _ := pipelineRuntimeFixture(t, parsed)
			services := NewServices(adapter, nil, repositories)
			t.Cleanup(func() { _ = services.LogAggregator.Close(context.Background()) })
			engine := &triggerCaptureEngine{}
			require.NoError(t, services.BindPipelineEngine(engine))
			service := NewPipelineServiceImpl(services)
			ctx := context.Background()
			allowed := tc.allowed
			response, err := service.ValidatePipelineSpec(ctx, &pipelinev1.ValidatePipelineSpecRequest{Spec: parsed})
			require.NoError(t, err)
			require.Equal(t, allowed, response.Success)
			result, err := service.TriggerPipeline(ctx, &pipelinev1.TriggerPipelineRequest{PipelineId: "runtime-pipeline", RequestId: "source-test", TriggeredBy: "fixture"})
			require.NoError(t, err)
			require.Equal(t, allowed, result.Success)
			var count int64
			require.NoError(t, adapter.db.Model(&model.PipelineRun{}).Count(&count).Error)
			if allowed {
				require.Equal(t, 1, engine.calls)
				require.EqualValues(t, 1, count)
			} else {
				require.EqualValues(t, 400, result.GetError().GetCode())
				require.Zero(t, engine.calls)
				require.Zero(t, count)
			}
		})
	}
}
