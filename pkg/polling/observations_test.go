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
