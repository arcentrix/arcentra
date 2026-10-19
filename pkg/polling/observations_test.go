// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package polling

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestObservedBatchExactPrecisionAndIdentity(t *testing.T) {
	stamp := "2026-10-16T16:00:00.123456789Z"
	observation := Observation{MRID: 7, MRIID: 3, State: "opened", UpdatedAt: stamp, SourceBranch: "feature", TargetBranch: "main", SourceSHA: strings.Repeat("a", 40), ObservationID: "observation"}
	b := ObservedBatch{Version: 1, ID: "batch", ScopeDigest: "v1:" + strings.Repeat("a", 64), Generation: 1, SelectionDigest: "v1:" + strings.Repeat("b", 64), Revision: 1, NextCursor: Cursor{Version: 1, Boundary: stamp, Complete: true, Observed: map[string]string{"7": "observation"}}, Observations: []Observation{observation}}
	raw, err := b.Canonical()
	require.NoError(t, err)
	parsed, err := ParseObservedBatch(raw)
	require.NoError(t, err)
	require.Equal(t, stamp, parsed.Observations[0].UpdatedAt)
	require.Equal(t, b, *parsed)
	for _, name := range []string{"duplicate", "cursor", "id", "time", "state", "source", "merge", "size"} {
		t.Run(name, func(t *testing.T) {
			changed := b
			changed.NextCursor.Observed = map[string]string{"7": "observation"}
			changed.Observations = append([]Observation(nil), b.Observations...)
			switch name {
			case "duplicate":
				changed.Observations = append(changed.Observations, observation)
			case "cursor":
				changed.NextCursor.Observed["7"] = "different"
			case "id":
				changed.Observations[0].MRID = 0
			case "time":
				changed.Observations[0].UpdatedAt = "bad"
			case "state":
				changed.Observations[0].State = "unknown"
			case "source":
				changed.Observations[0].SourceSHA = "main"
			case "merge":
				changed.Observations[0].State = "merged"
			case "size":
				changed.Observations = make([]Observation, MaxInventory+1)
			}
			_, err := changed.Canonical()
			require.Error(t, err)
		})
	}
}

func TestObservationSeparateMergeAndSquashProvenance(t *testing.T) {
	o := Observation{MRID: 9, MRIID: 4, State: "merged", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), MergedAt: time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), SourceBranch: "feature", TargetBranch: "main", SourceSHA: strings.Repeat("a", 40), MergeSHA: strings.Repeat("b", 40), SquashSHA: strings.Repeat("c", 40), ObservationID: "merge"}
	require.NoError(t, o.Validate())
	require.NotEqual(t, o.SourceSHA, o.MergeSHA)
	require.NotEqual(t, o.MergeSHA, o.SquashSHA)
	o.MergedAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	require.Error(t, o.Validate())
}

func TestOptionalSourceProvenancePreservesLegacyAndRejectsPartial(t *testing.T) {
	o := Observation{MRID: 7, MRIID: 3, State: "opened", UpdatedAt: "2026-10-16T16:00:00Z", SourceBranch: "feature", TargetBranch: "main", SourceSHA: strings.Repeat("a", 40), ObservationID: "observation"}
	raw, err := canonical(o)
	require.NoError(t, err)
	require.Equal(t, `{"mrId":7,"mrIid":3,"state":"opened","updatedAt":"2026-10-16T16:00:00Z","sourceBranch":"feature","targetBranch":"main","sourceSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observationId":"observation"}`, raw)
	require.NoError(t, o.Validate())
	o.SourceProjectID = 99
	o.SourceRepository = "fork/repo"
	o.TargetSHA = strings.Repeat("b", 40)
	require.NoError(t, o.Validate())
	raw, err = canonical(o)
	require.NoError(t, err)
	var restored Observation
	require.NoError(t, decode(raw, &restored))
	require.Equal(t, o, restored)
	for _, mutate := range []func(*Observation){func(o *Observation) { o.SourceProjectID = 0 }, func(o *Observation) { o.SourceProjectID = -1 }, func(o *Observation) { o.SourceRepository = "" }, func(o *Observation) { o.TargetSHA = "" }, func(o *Observation) { o.TargetSHA = "main" }, func(o *Observation) { o.SourceRepository = "https://evil.example/repo" }, func(o *Observation) { o.SourceRepository = "fork/../repo" }, func(o *Observation) { o.SourceRepository = "fork/repo?token=x" }} {
		changed := o
		mutate(&changed)
		require.Error(t, changed.Validate())
		target := Target{SourceProjectID: changed.SourceProjectID, SourceRepository: changed.SourceRepository, TargetSHA: changed.TargetSHA}
		require.Error(t, validateSourceProvenance(target.SourceProjectID, target.SourceRepository, target.TargetSHA))
	}
}
