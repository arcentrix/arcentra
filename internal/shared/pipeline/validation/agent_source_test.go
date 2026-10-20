// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package validation

import (
	"encoding/json"
	"strings"
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/stretchr/testify/require"
)

func TestBaremetalAgentDSLRejectsExplicitSSHOnly(t *testing.T) {
	for _, tc := range []struct {
		source   string
		rejected bool
	}{
		{"ssh://git@example.invalid/repo", true}, {"SSH://example.invalid/repo", true}, {"git+ssh://example.invalid/repo", true}, {"ssh+git://example.invalid/repo", true}, {"git@example.invalid:repo", true}, {"example.invalid:repo", true}, {"git@[::1]:repo", true}, {"ssh://${{ vars.host }}/repo", true},
		{"https://example.invalid/repo", false}, {"git://example.invalid/repo", false}, {"${{ vars.repository }}", false}, {"${{ vars.protocol }}://example.invalid/repo", false}, {"/tmp/local-repo", false}, {"C:\\repo", false},
	} {
		t.Run(tc.source, func(t *testing.T) {
			for _, remote := range []bool{false, true} {
				document := map[string]any{"namespace": "source", "jobs": []any{map[string]any{"name": "build", "source": map[string]any{"type": "git", "repo": tc.source}, "steps": []any{map[string]any{"name": "first", "uses": "shell"}, map[string]any{"name": "second", "uses": "shell", "runOnAgent": remote}}}}}
				raw, err := json.Marshal(document)
				require.NoError(t, err)
				parsed, err := spec.ParseContentToProto(string(raw), pipelinev1.SpecFormat_SPEC_FORMAT_JSON)
				require.NoError(t, err)
				require.Nil(t, parsed.Runtime, "this exercises ordinary baremetal validation")
				err = NewSchemaValidator().Validate(parsed)
				if remote && tc.rejected {
					require.ErrorContains(t, err, "Agent checkout does not support SSH")
					require.False(t, strings.Contains(err.Error(), tc.source))
				} else {
					require.NoError(t, err)
				}
			}
		})
	}
}

func TestAgentDisabledSourceRetainsExistingSemantics(t *testing.T) {
	p := capabilityPipeline(&spec.Step{Name: "agent", Uses: "shell", RunOnAgent: true})
	p.Jobs[0].Source = &spec.Source{Repo: "ssh://example.invalid/repo"}
	require.NoError(t, NewSchemaValidator().Validate(p))
	p.Jobs[0].Source.Type = "GIT"
	require.ErrorContains(t, NewSchemaValidator().Validate(p), "Agent checkout does not support SSH")
}
