// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package polling

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func v3Scope(provider string) Scope {
	s := Scope{Version: ScopeVersion3, ProjectID: "project", Provider: provider, APIOrigin: "https://gitee.com", Repository: "https://gitee.com/owner/repo", ProviderProjectID: "42", Stream: "pull_requests"}
	if provider == "bitbucket" {
		s.APIOrigin = "https://api.bitbucket.org"
		s.Repository = "https://bitbucket.org/owner/repo"
		s.ProviderProjectID = "12345678-1234-1234-1234-123456789abc"
	}
	return s
}

func v3Observation(s Scope, state string) Observation {
	o := Observation{MRID: 7, MRIID: 3, State: state, UpdatedAt: "2026-10-16T16:00:00.123456789Z", SourceBranch: "feature/topic", TargetBranch: "main", SourceSHA: strings.Repeat("a", 40), TargetSHA: strings.Repeat("b", 40), ObservationID: "provider:7:evidence", SourceRepository: "fork/repo", EvidenceVersion: ScopeVersion3, ProviderProjectKey: s.ProviderProjectID, SourceProjectKey: "99", MergeEvidence: MergeEvidenceStateTimestamp}
	if s.Provider == "bitbucket" {
		o.MRIID = o.MRID
		o.SourceProjectKey = "23456789-2345-2345-2345-23456789abcd"
		o.MergeEvidence = MergeEvidenceStateCommit
	}
	if state == "merged" {
		if s.Provider == "gitee" {
			o.MergedAt = "2026-10-16T15:59:59.123456789Z"
		} else {
			o.MergeSHA = strings.Repeat("c", 40)
		}
	}
	return o
}

func v3Target(t *testing.T, s Scope, o Observation) Target {
	t.Helper()
	request, err := EventRequestID(s, o.ObservationID)
	require.NoError(t, err)
	return Target{MRID: o.MRID, MRIID: o.MRIID, State: o.State, UpdatedAt: o.UpdatedAt, MergedAt: o.MergedAt, MergeSHA: o.MergeSHA, SquashSHA: o.SquashSHA, PipelineID: "pipeline", ObservationID: o.ObservationID, RequestID: request, SourceBranch: o.SourceBranch, TargetBranch: o.TargetBranch, SourceSHA: o.SourceSHA, DefinitionCommitSHA: strings.Repeat("d", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: "v1:" + strings.Repeat("e", 64), SourceProjectID: o.SourceProjectID, SourceRepository: o.SourceRepository, TargetSHA: o.TargetSHA, EvidenceVersion: o.EvidenceVersion, ProviderProjectKey: o.ProviderProjectKey, SourceProjectKey: o.SourceProjectKey, MergeEvidence: o.MergeEvidence}
}

func TestV3ProviderScopePolicy(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			s := v3Scope(provider)
			require.NoError(t, s.Validate())
			for name, mutate := range map[string]func(*Scope){
				"v1": func(s *Scope) { s.Version = Version }, "v2": func(s *Scope) { s.Version = ScopeVersion2 }, "future_version": func(s *Scope) { s.Version++ },
				"legacy_provider": func(s *Scope) { s.Provider = "gitlab" }, "other_v3_provider": func(s *Scope) {
					if s.Provider == "gitee" {
						s.Provider = "bitbucket"
					} else {
						s.Provider = "gitee"
					}
				},
				"api_path": func(s *Scope) { s.APIOrigin += "/api" }, "api_alias": func(s *Scope) { s.APIOrigin = "https://evil.example.com" },
				"unknown_host": func(s *Scope) { s.Repository = "https://enterprise.example.com/owner/repo" }, "port": func(s *Scope) { s.Repository = strings.Replace(s.Repository, "/owner/", ":443/owner/", 1) },
				"api_repository_host": func(s *Scope) { s.Repository = "https://api.bitbucket.org/owner/repo" }, "nested": func(s *Scope) { s.Repository += "/extra" }, "unsafe_name": func(s *Scope) { s.Repository += ";x" },
				"name_leading_dash": func(s *Scope) { s.Repository = strings.Replace(s.Repository, "/owner/", "/-owner/", 1) },
				"double_dot_name":   func(s *Scope) { s.Repository += "..x" },
				"empty_key":         func(s *Scope) { s.ProviderProjectID = "" }, "nil_uuid": func(s *Scope) { s.ProviderProjectID = "00000000-0000-0000-0000-000000000000" }, "space_key": func(s *Scope) { s.ProviderProjectID += " " },
				"query": func(s *Scope) { s.Repository += "?x=y" }, "fragment": func(s *Scope) { s.Repository += "#x" }, "encoded": func(s *Scope) { s.Repository += "%2fother" },
				"wrong_stream": func(s *Scope) { s.Stream = "merge_requests" },
			} {
				t.Run(name, func(t *testing.T) { other := s; mutate(&other); require.Error(t, other.Validate()) })
			}
			badKeys := []string{"0", "-1", "+42", "042", "9223372036854775808", "12345678123412341234123456789abc", "{12345678-1234-1234-1234-123456789abc}", "12345678-1234-1234-1234-123456789ABC"}
			if provider == "gitee" {
				badKeys = append(badKeys, v3Scope("bitbucket").ProviderProjectID)
			} else {
				badKeys = append(badKeys, "42")
			}
			for _, key := range badKeys {
				other := s
				other.ProviderProjectID = key
				require.Error(t, other.Validate(), key)
			}
			original, err := s.Digest()
			require.NoError(t, err)
			for _, mutate := range []func(*Scope){func(s *Scope) { s.ProjectID += "-other" }, func(s *Scope) { s.Repository += "-renamed" }, func(s *Scope) {
				if provider == "gitee" {
					s.ProviderProjectID = "43"
				} else {
					s.ProviderProjectID = "12345678-1234-1234-1234-123456789abd"
				}
			}} {
				other := s
				mutate(&other)
				hash, err := other.Digest()
				require.NoError(t, err)
				require.NotEqual(t, original, hash)
			}
		})
	}
}

func TestV3ObservationEvidenceModesAndScopeBinding(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		for _, state := range []string{"opened", "closed", "merged"} {
			t.Run(provider+"/"+state, func(t *testing.T) {
				s := v3Scope(provider)
				o := v3Observation(s, state)
				require.NoError(t, o.Validate())
				require.NoError(t, o.ValidateForScope(s))
				for name, mutate := range map[string]func(*Observation){
					"legacy_discriminator": func(o *Observation) { o.EvidenceVersion = 0 }, "v2_discriminator": func(o *Observation) { o.EvidenceVersion = 2 }, "future_discriminator": func(o *Observation) { o.EvidenceVersion = 4 },
					"missing_mode": func(o *Observation) { o.MergeEvidence = "" }, "unknown_mode": func(o *Observation) { o.MergeEvidence = "sha" }, "numeric_and_string": func(o *Observation) { o.SourceProjectID = 99 },
					"missing_provider_key": func(o *Observation) { o.ProviderProjectKey = "" }, "missing_source_key": func(o *Observation) { o.SourceProjectKey = "" }, "missing_source_repository": func(o *Observation) { o.SourceRepository = "" },
					"source_url": func(o *Observation) { o.SourceRepository = "https://fork/repo" }, "missing_target_sha": func(o *Observation) { o.TargetSHA = "" }, "short_source_sha": func(o *Observation) { o.SourceSHA = "abcd" },
					"short_target_sha": func(o *Observation) { o.TargetSHA = "abcd" }, "bad_state": func(o *Observation) { o.State = "locked" }, "squash": func(o *Observation) { o.SquashSHA = strings.Repeat("d", 40) },
					"source_ref_traversal": func(o *Observation) { o.SourceBranch = "../topic" }, "target_ref_option": func(o *Observation) { o.TargetBranch = "--main" }, "ref_control": func(o *Observation) { o.SourceBranch = "topic\tname" },
					"noncanonical_time": func(o *Observation) { o.UpdatedAt = "2026-10-16T16:00:00.123456789+00:00" }, "comma_time": func(o *Observation) { o.UpdatedAt = "2026-10-16T16:00:00,123456789Z" },
				} {
					t.Run(name, func(t *testing.T) {
						changed := o
						mutate(&changed)
						require.Error(t, changed.ValidateForScope(s))
						require.Error(t, changed.Validate())
					})
				}
				changed := o
				changed.ProviderProjectKey = changed.SourceProjectKey
				require.NoError(t, changed.Validate())
				require.Error(t, changed.ValidateForScope(s))
				changed = o
				changed.SourceProjectKey = s.ProviderProjectID
				require.Error(t, changed.ValidateForScope(s))
				changed.SourceRepository = "owner/repo"
				require.NoError(t, changed.ValidateForScope(s))
				changed.SourceProjectKey = o.SourceProjectKey
				require.Error(t, changed.ValidateForScope(s))
				for _, legacy := range []Scope{testScope(), {Version: ScopeVersion2, ProjectID: "project", Provider: "github", APIOrigin: "https://api.github.com", Repository: "https://github.com/owner/repo", ProviderProjectID: "42", Stream: "pull_requests"}} {
					require.Error(t, o.ValidateForScope(legacy))
				}
				if provider == "gitee" {
					changed = o
					changed.MergeSHA = strings.Repeat("e", 40)
					require.Error(t, changed.ValidateForScope(s))
					if state == "merged" {
						changed = o
						changed.MergedAt = ""
						require.Error(t, changed.ValidateForScope(s))
						changed.MergedAt = "2026-10-17T00:00:00Z"
						require.Error(t, changed.ValidateForScope(s))
					}
				} else {
					changed = o
					changed.MergedAt = "2026-10-16T00:00:00Z"
					require.Error(t, changed.ValidateForScope(s))
					if state == "merged" {
						changed = o
						changed.MergeSHA = ""
						require.Error(t, changed.ValidateForScope(s))
						changed.MergeSHA = "abcd"
						require.Error(t, changed.ValidateForScope(s))
					}
				}
				if state != "merged" {
					changed = v3Observation(s, "merged")
					changed.State = state
					require.Error(t, changed.ValidateForScope(s))
				}
			})
		}
	}
}

func TestV3EvidenceRoundTripAndVersionConfusion(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			s := v3Scope(provider)
			o := v3Observation(s, "merged")
			target := v3Target(t, s, o)
			require.Equal(t, o, target.Observation())
			require.NoError(t, target.ValidateForScope(s))
			sd, err := s.Digest()
			require.NoError(t, err)
			selection := "v1:" + strings.Repeat("e", 64)
			next := Cursor{Version: Version, Boundary: "2026-10-16T17:00:00Z", Complete: true, Observed: map[string]string{"7": o.ObservationID}}
			observed := ObservedBatch{Version: Version, ID: "batch", ScopeDigest: sd, Generation: 1, SelectionDigest: selection, Revision: 2, NextCursor: next, Observations: []Observation{o}}
			body, err := observed.CanonicalForScope(s)
			require.NoError(t, err)
			restoredObserved, err := ParseObservedBatchForScope(body, s)
			require.NoError(t, err)
			require.Equal(t, observed, *restoredObserved)
			batch := Batch{ObservationCount: 1, Version: Version, ID: "batch", ScopeDigest: sd, Generation: 1, SelectionDigest: selection, Revision: 3, NextCursor: next, Targets: []Target{target}}
			body, err = batch.CanonicalForScope(s)
			require.NoError(t, err)
			restoredBatch, err := ParseBatchForScope(body, s)
			require.NoError(t, err)
			require.Equal(t, batch, *restoredBatch)
			context := RunContext{BaselineID: "baseline", BaselineCutoff: "2026-10-16T12:00:00Z", Version: Version, StreamID: "stream", Scope: s, ScopeDigest: sd, ActivationGeneration: 1, ActorID: "actor", SelectionDigest: selection, BatchID: "batch", BatchRevision: 3, RunID: "run", Target: target}
			body, err = context.Canonical()
			require.NoError(t, err)
			restoredContext, err := ParseRunContext(body)
			require.NoError(t, err)
			require.Equal(t, context, *restoredContext)
			// Rehashing a forged legacy scope does not authorize the weaker mode.
			legacy := testScope()
			legacyDigest, err := legacy.Digest()
			require.NoError(t, err)
			observed.ScopeDigest = legacyDigest
			body, err = observed.Canonical()
			require.NoError(t, err)
			_, err = ParseObservedBatchForScope(body, legacy)
			require.Error(t, err)
			batch.ScopeDigest = legacyDigest
			batch.Targets[0].RequestID, err = EventRequestID(legacy, target.ObservationID)
			require.NoError(t, err)
			body, err = batch.Canonical()
			require.NoError(t, err)
			_, err = ParseBatchForScope(body, legacy)
			require.Error(t, err)
			context.Scope = legacy
			context.ScopeDigest = legacyDigest
			context.Target = batch.Targets[0]
			_, err = context.Canonical()
			require.Error(t, err)
			body, err = canonical(context)
			require.NoError(t, err)
			_, err = ParseRunContext(body)
			require.Error(t, err)
			// Erasing v3 evidence cannot make it admissible to the original v3 scope.
			target.EvidenceVersion = 0
			target.ProviderProjectKey = ""
			target.SourceProjectKey = ""
			target.MergeEvidence = ""
			require.Error(t, target.ValidateForScope(s))
		})
	}
}

func TestV3ScopeCanonicalGolden(t *testing.T) {
	for provider, expected := range map[string]legacyGoldenPart{
		"gitee":     {Bytes: `{"version":3,"projectId":"project","provider":"gitee","apiOrigin":"https://gitee.com","repository":"https://gitee.com/owner/repo","providerProjectId":"42","stream":"pull_requests"}`, Digest: "v1:1e544c4f6245fa6ae247f32a048b9118ad0a66c05eed63c1f9ae4b4f5f2f66c7"},
		"bitbucket": {Bytes: `{"version":3,"projectId":"project","provider":"bitbucket","apiOrigin":"https://api.bitbucket.org","repository":"https://bitbucket.org/owner/repo","providerProjectId":"12345678-1234-1234-1234-123456789abc","stream":"pull_requests"}`, Digest: "v1:f75f9c163038e8b85cee88c12ba3c0522af71d5deee17dbf27eb12448974e4ce"},
	} {
		scope := v3Scope(provider)
		raw, err := scope.Canonical()
		require.NoError(t, err)
		require.Equal(t, expected.Bytes, raw)
		digest, err := scope.Digest()
		require.NoError(t, err)
		require.Equal(t, expected.Digest, digest)
	}
}

func TestV3BitbucketLocalPullRequestIdentity(t *testing.T) {
	scope := v3Scope("bitbucket")
	o := v3Observation(scope, "opened")
	o.MRIID++
	require.Error(t, o.ValidateForScope(scope))
}

func TestV3CaptureBoundaryRejectsFutureEvidence(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		scope := v3Scope(provider)
		observation := v3Observation(scope, "opened")
		target := v3Target(t, scope, observation)
		sd, err := scope.Digest()
		require.NoError(t, err)
		cursor := Cursor{Version: Version, Boundary: "2026-10-16T15:00:00Z", Complete: true, Observed: map[string]string{"7": observation.ObservationID}}
		selection := "v1:" + strings.Repeat("a", 64)
		observed := ObservedBatch{Version: Version, ID: "batch", ScopeDigest: sd, Generation: 1, SelectionDigest: selection, Revision: 1, NextCursor: cursor, Observations: []Observation{observation}}
		_, err = observed.CanonicalForScope(scope)
		require.Error(t, err)
		batch := Batch{ObservationCount: 1, Version: Version, ID: "batch", ScopeDigest: sd, Generation: 1, SelectionDigest: selection, Revision: 1, NextCursor: cursor, Targets: []Target{target}}
		_, err = batch.CanonicalForScope(scope)
		require.Error(t, err)
		observation.UpdatedAt = "0000-01-01T00:00:00Z"
		require.Error(t, observation.ValidateForScope(scope))
	}
}
