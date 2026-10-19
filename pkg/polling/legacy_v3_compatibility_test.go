// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package polling

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type legacyGoldenPart struct {
	Bytes  string `json:"bytes"`
	Digest string `json:"digest"`
}

func legacyEvidenceFixture(t *testing.T, provider string) (Scope, Observation, Target, ObservedBatch, Batch, RunContext) {
	t.Helper()
	s := Scope{Version: Version, ProjectID: "project", Provider: "gitlab", APIOrigin: "https://gitlab.example.com", Repository: "https://gitlab.example.com/group/nested/repo", ProviderProjectID: "42", Stream: "merge_requests"}
	if provider != "gitlab" {
		s.Version = ScopeVersion2
		s.Provider = provider
		s.Stream = "pull_requests"
		s.Repository = "https://github.com/owner/repo"
		s.APIOrigin = "https://api.github.com"
		if provider == "gitea" {
			s.Repository = "https://gitea.example.com/owner/repo"
			s.APIOrigin = "https://gitea.example.com"
		}
	}
	sd, err := s.Digest()
	require.NoError(t, err)
	request, err := EventRequestID(s, "merge:42:7:immutable")
	require.NoError(t, err)
	target := Target{MRID: 7, MRIID: 3, State: "merged", UpdatedAt: "2026-10-16T16:00:00.123456789Z", MergedAt: "2026-10-16T15:59:00.123456789Z", MergeSHA: strings.Repeat("c", 40), SquashSHA: strings.Repeat("d", 40), PipelineID: "pipeline", ObservationID: "merge:42:7:immutable", RequestID: request, SourceBranch: "feature", TargetBranch: "main", SourceSHA: strings.Repeat("a", 40), DefinitionCommitSHA: strings.Repeat("b", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: "v1:" + strings.Repeat("e", 64)}
	if provider != "gitlab" {
		target.SourceProjectID = 99
		target.SourceRepository = "fork/repo"
		target.TargetSHA = strings.Repeat("f", 40)
	}
	o := target.Observation()
	cursor := Cursor{Version: Version, Boundary: "2026-10-16T17:00:00.123456789Z", Complete: true, Observed: map[string]string{"7": target.ObservationID}}
	selection := "v1:" + strings.Repeat("d", 64)
	observed := ObservedBatch{Version: Version, ID: "batch", ScopeDigest: sd, Generation: 2, SelectionDigest: selection, Revision: 3, NextCursor: cursor, Observations: []Observation{o}}
	batch := Batch{ObservationCount: 1, Version: Version, ID: "batch", ScopeDigest: sd, Generation: 2, SelectionDigest: selection, Revision: 4, NextCursor: cursor, Targets: []Target{target}, Results: []TargetResult{{PipelineID: target.PipelineID, RequestID: target.RequestID, RunID: "run", Outcome: OutcomeAdmitted}}}
	context := RunContext{BaselineID: "baseline", BaselineCutoff: "2026-10-16T12:00:00.123456789Z", Version: Version, StreamID: "stream", Scope: s, ScopeDigest: sd, ActivationGeneration: 2, ActorID: "actor", SelectionDigest: selection, BatchID: "batch", BatchRevision: 4, RunID: "run", Target: target}
	return s, o, target, observed, batch, context
}

func legacyGoldenEvidence(t *testing.T, provider string) map[string]legacyGoldenPart {
	t.Helper()
	s, o, target, observed, batch, context := legacyEvidenceFixture(t, provider)
	parts := map[string]any{"scope": s, "observation": o, "target": target, "observedBatch": observed, "batch": batch, "runContext": context}
	out := map[string]legacyGoldenPart{}
	for name, part := range parts {
		body, err := canonical(part)
		require.NoError(t, err)
		out[name] = legacyGoldenPart{Bytes: body, Digest: digest(body)}
	}
	return out
}

// Fixtures were generated with the pre-v3 implementation at 3670087a1d5015ccf948666b6c553ff9f862f229.
// Compare complete bytes and digests, then replay the same bytes through the
// scope-aware readers used for new captures; no migration or rewrite is needed.
func TestLegacyV1V2GoldenEvidenceAndScopedReplay(t *testing.T) {
	for _, provider := range []string{"gitlab", "github", "gitea"} {
		t.Run(provider, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("testdata", provider+"_legacy_evidence.json"))
			require.NoError(t, err)
			var expected map[string]legacyGoldenPart
			require.NoError(t, json.Unmarshal(body, &expected))
			require.Equal(t, expected, legacyGoldenEvidence(t, provider))
			s, o, target, observed, batch, context := legacyEvidenceFixture(t, provider)
			require.NoError(t, o.ValidateForScope(s))
			require.NoError(t, target.ValidateForScope(s))
			raw, err := observed.CanonicalForScope(s)
			require.NoError(t, err)
			require.Equal(t, expected["observedBatch"].Bytes, raw)
			restoredObserved, err := ParseObservedBatchForScope(raw, s)
			require.NoError(t, err)
			require.Equal(t, observed, *restoredObserved)
			raw, err = batch.CanonicalForScope(s)
			require.NoError(t, err)
			require.Equal(t, expected["batch"].Bytes, raw)
			restoredBatch, err := ParseBatchForScope(raw, s)
			require.NoError(t, err)
			require.Equal(t, batch, *restoredBatch)
			raw, err = context.Canonical()
			require.NoError(t, err)
			require.Equal(t, expected["runContext"].Bytes, raw)
			restoredContext, err := ParseRunContext(raw)
			require.NoError(t, err)
			require.Equal(t, context, *restoredContext)
		})
	}
}
