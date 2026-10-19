// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/polling"
	"github.com/stretchr/testify/require"
)

func setV3PollingTarget(target *polling.Target, scope polling.Scope, state string) {
	target.State = state
	target.EvidenceVersion = polling.ScopeVersion3
	target.ProviderProjectKey = scope.ProviderProjectID
	target.SourceProjectKey = "99"
	target.SourceProjectID = 0
	target.SourceRepository = "fork-owner/repo"
	target.TargetSHA = strings.Repeat("d", 40)
	target.MergeEvidence = polling.MergeEvidenceStateTimestamp
	if scope.Provider == "bitbucket" {
		target.SourceProjectKey = "23456789-2345-2345-2345-23456789abcd"
		target.MergeEvidence = polling.MergeEvidenceStateCommit
	}
	if state == "merged" {
		if scope.Provider == "gitee" {
			target.MergedAt = "2026-10-16T15:59:00.123456789Z"
		} else {
			target.MergeSHA = strings.Repeat("e", 40)
		}
	}
}

func TestPollingV3MergedEvidenceRoundTripAdmissionAndReplay(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			f := newPollingFixture(t, provider)
			s, run := f.captureState(t, f.enable(t), false, "merged")
			original, err := polling.ParseRunContext(run.PollingContext)
			require.NoError(t, err)
			batch, err := polling.ParseBatchForScope(s.PendingBatchJSON, f.scope)
			require.NoError(t, err)
			require.Equal(t, original.Target, batch.Targets[0])
			require.Equal(t, 0, int(original.Target.SourceProjectID))
			if provider == "gitee" {
				require.Empty(t, original.Target.MergeSHA)
				require.NotEmpty(t, original.Target.MergedAt)
			} else {
				require.Empty(t, original.Target.MergedAt)
				require.NotEmpty(t, original.Target.MergeSHA)
			}
			f.create(t, run)
			stored, err := f.runs.GetRun(f.ctx, run.RunID)
			require.NoError(t, err)
			restored, err := polling.ParseRunContext(stored.PollingContext)
			require.NoError(t, err)
			require.Equal(t, original, restored)
			// Restore through new repository objects, as after a process restart.
			f.runs = &PipelineRepo{IDatabase: statusTestDB{f.db}}
			f.streams = &SCMPollingRepo{IDatabase: statusTestDB{f.db}}
			result, err := f.runs.StartPollingRunIfPending(f.ctx, stored)
			require.NoError(t, err)
			require.True(t, result.ClaimedNow)
			duplicate, err := f.runs.CreatePollingRun(f.ctx, run)
			require.NoError(t, err)
			require.False(t, duplicate.CreatedNow)
			require.Equal(t, polling.OutcomeAdmitted, duplicate.Outcome)
			latest := f.latest(t)
			ack, err := f.streams.AcknowledgeBatch(f.ctx, AcknowledgeBatchInput{ProjectID: "project", ExpectedRevision: latest.Revision, ActivationGeneration: latest.ActivationGeneration, BatchID: batch.ID})
			require.NoError(t, err)
			require.Empty(t, ack.PendingBatchJSON)
			require.NotEqual(t, s.CursorJSON, ack.CursorJSON)
		})
	}
}

func TestPollingV3CaptureRejectsUnboundEvidenceAtomically(t *testing.T) {
	for _, provider := range []string{"gitlab", "github", "gitea", "gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			f := newPollingFixture(t, provider)
			stream := f.enable(t)
			target := polling.Target{MRID: 7, MRIID: 3, UpdatedAt: "2026-10-16T16:00:00.123456789Z", SourceBranch: "feature", TargetBranch: "main", SourceSHA: strings.Repeat("a", 40), ObservationID: "observation"}
			evidenceScope := f.scope
			if f.scope.Version != polling.ScopeVersion3 {
				evidenceScope.Provider = "gitee"
			}
			setV3PollingTarget(&target, evidenceScope, "opened")
			original := target.Observation()
			mutations := map[string]func(*polling.Observation){
				"missing_version":          func(o *polling.Observation) { o.EvidenceVersion = 0 },
				"mixed_numeric_identity":   func(o *polling.Observation) { o.SourceProjectID = 99 },
				"missing_key":              func(o *polling.Observation) { o.ProviderProjectKey = "" },
				"changed_bound_key":        func(o *polling.Observation) { o.ProviderProjectKey = o.SourceProjectKey },
				"missing_mode":             func(o *polling.Observation) { o.MergeEvidence = "" },
				"source_key_name_mismatch": func(o *polling.Observation) { o.SourceProjectKey = o.ProviderProjectKey },
			}
			if f.scope.Version != polling.ScopeVersion3 {
				mutations["well_formed_v3_in_legacy_scope"] = func(*polling.Observation) {}
			}
			for name, mutate := range mutations {
				t.Run(name, func(t *testing.T) {
					o := original
					mutate(&o)
					next := polling.Cursor{Version: polling.Version, Boundary: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), Complete: true, Observed: map[string]string{"7": o.ObservationID}}
					_, err := f.streams.CaptureObservations(f.ctx, CaptureObservationsInput{ProjectID: "project", ExpectedRevision: stream.Revision, ActivationGeneration: stream.ActivationGeneration, Batch: polling.ObservedBatch{Version: polling.Version, ID: "batch", ScopeDigest: stream.ScopeDigest, Generation: stream.ActivationGeneration, SelectionDigest: stream.SelectionDigest, NextCursor: next, Observations: []polling.Observation{o}}})
					require.Error(t, err)
					latest := f.latest(t)
					require.Equal(t, stream.Revision, latest.Revision)
					require.Equal(t, stream.CursorJSON, latest.CursorJSON)
					require.Empty(t, latest.PendingObservationJSON)
					require.Empty(t, latest.PendingBatchJSON)
				})
			}
		})
	}
}

func TestPollingV3FrozenProvenanceMutationCannotCreateOrAdmit(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			f := newPollingFixture(t, provider)
			stream, run := f.captureState(t, f.enable(t), false, "merged")
			original, err := polling.ParseRunContext(run.PollingContext)
			require.NoError(t, err)
			for name, mutate := range map[string]func(*polling.Target){
				"source_key": func(t *polling.Target) {
					if provider == "gitee" {
						t.SourceProjectKey = "100"
					} else {
						t.SourceProjectKey = "3456789a-3456-3456-3456-3456789abcde"
					}
				},
				"source_name": func(t *polling.Target) { t.SourceRepository = "different/repo" },
				"target_hash": func(t *polling.Target) { t.TargetSHA = strings.Repeat("f", 40) },
				"merge_evidence": func(t *polling.Target) {
					if provider == "gitee" {
						t.MergedAt = "2026-10-16T15:58:00.123456789Z"
					} else {
						t.MergeSHA = strings.Repeat("f", 40)
					}
				},
			} {
				t.Run(name, func(t *testing.T) {
					changed := *original
					mutate(&changed.Target)
					copy := *run
					copy.PollingContext, err = changed.Canonical()
					require.NoError(t, err)
					copy.PollingContextDigest, err = changed.Digest()
					require.NoError(t, err)
					result, err := f.runs.CreatePollingRun(f.ctx, &copy)
					require.Error(t, err)
					require.False(t, result.CreatedNow)
				})
			}
			f.create(t, run)
			changed := *original
			changed.Target.SourceRepository = "different/repo"
			copy := *run
			copy.PollingContext, err = changed.Canonical()
			require.NoError(t, err)
			copy.PollingContextDigest, err = changed.Digest()
			require.NoError(t, err)
			result, err := f.runs.StartPollingRunIfPending(f.ctx, &copy)
			require.Error(t, err)
			require.False(t, result.ClaimedNow)
			latest := f.latest(t)
			require.Equal(t, stream.CursorJSON, latest.CursorJSON)
			require.NotEmpty(t, latest.PendingBatchJSON)
			result, err = f.runs.StartPollingRunIfPending(f.ctx, run)
			require.NoError(t, err)
			require.True(t, result.ClaimedNow)
		})
	}
}

func TestPollingV3PersistedVersionConfusionBlocksReplayAndAcknowledgement(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			f := newPollingFixture(t, provider)
			stream, run := f.captureState(t, f.enable(t), false, "merged")
			batch, err := polling.ParseBatchForScope(stream.PendingBatchJSON, f.scope)
			require.NoError(t, err)
			batch.Targets[0].EvidenceVersion = 2
			body, err := json.Marshal(batch)
			require.NoError(t, err)
			require.NoError(t, f.db.Model(&model.SCMPollingStream{}).Where("stream_id = ?", stream.StreamID).Update("pending_batch_json", string(body)).Error)
			created, err := f.runs.CreatePollingRun(f.ctx, run)
			require.Error(t, err)
			require.False(t, created.CreatedNow)
			_, err = f.streams.AcknowledgeBatch(f.ctx, AcknowledgeBatchInput{ProjectID: "project", ExpectedRevision: stream.Revision, ActivationGeneration: stream.ActivationGeneration, BatchID: batch.ID})
			require.Error(t, err)
			latest := f.latest(t)
			require.Equal(t, stream.CursorJSON, latest.CursorJSON)
			require.Equal(t, stream.Revision, latest.Revision)
			require.Equal(t, string(body), latest.PendingBatchJSON)
		})
	}
}

// Executed only by TestPollingMySQLAcceptance with an explicitly supplied local
// disposable MySQL DSN. This test does not claim live-provider or build acceptance.
func pollingMySQLV3MergeEvidence(t *testing.T, f *pollingMySQLFixture) {
	t.Helper()
	stream, run := f.prepareState(t, "merged")
	original, err := polling.ParseRunContext(run.PollingContext)
	require.NoError(t, err)
	f.create(t, run)
	stored := f.stored(t, run)
	restored, err := polling.ParseRunContext(stored.PollingContext)
	require.NoError(t, err)
	require.Equal(t, original, restored)
	claimed, err := f.otherRuns.StartPollingRunIfPending(f.ctx, stored)
	require.NoError(t, err)
	require.True(t, claimed.ClaimedNow)
	duplicate, err := f.runs.CreatePollingRun(f.ctx, run)
	require.NoError(t, err)
	require.False(t, duplicate.CreatedNow)
	require.Equal(t, polling.OutcomeAdmitted, duplicate.Outcome)
	latest := f.latest(t)
	ack, err := f.otherStreams.AcknowledgeBatch(f.ctx, AcknowledgeBatchInput{ProjectID: f.projectID, ExpectedRevision: latest.Revision, ActivationGeneration: latest.ActivationGeneration, BatchID: f.batchID})
	require.NoError(t, err)
	require.Empty(t, ack.PendingBatchJSON)
	require.NotEqual(t, stream.CursorJSON, ack.CursorJSON)
}

func TestPollingV3ObservedReplayRejectsStrippedVersionBeforeFreezing(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			f := newPollingFixture(t, provider)
			stream, _ := f.captureState(t, f.enable(t), false, "merged")
			batch, err := polling.ParseBatchForScope(stream.PendingBatchJSON, f.scope)
			require.NoError(t, err)
			o := batch.Targets[0].Observation()
			o.EvidenceVersion = 0
			observed := polling.ObservedBatch{Version: polling.Version, ID: batch.ID, ScopeDigest: batch.ScopeDigest, Generation: batch.Generation, SelectionDigest: batch.SelectionDigest, Revision: stream.Revision, NextCursor: batch.NextCursor, Observations: []polling.Observation{o}}
			body, err := json.Marshal(observed)
			require.NoError(t, err)
			require.NoError(t, f.db.Model(&model.SCMPollingStream{}).Where("stream_id = ?", stream.StreamID).Updates(map[string]any{"pending_batch_json": "", "pending_observation_json": string(body)}).Error)
			_, err = f.streams.CaptureBatch(f.ctx, CaptureBatchInput{ProjectID: "project", ExpectedRevision: stream.Revision, ActivationGeneration: stream.ActivationGeneration, Batch: *batch})
			require.Error(t, err)
			latest := f.latest(t)
			require.Equal(t, stream.Revision, latest.Revision)
			require.Equal(t, stream.CursorJSON, latest.CursorJSON)
			require.Empty(t, latest.PendingBatchJSON)
			require.Equal(t, string(body), latest.PendingObservationJSON)
		})
	}
}
