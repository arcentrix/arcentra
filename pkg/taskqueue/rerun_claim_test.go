// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRerunClaimReferenceAndPrivateFormatting(t *testing.T) {
	ref := RerunClaimReference{AgentID: "agent", PipelineID: "pipeline", RunID: "11111111-1111-4111-8111-111111111111", JobRunID: "22222222-2222-4222-8222-222222222222", GateID: "33333333-3333-4333-8333-333333333333", ScheduleID: "44444444-4444-4444-8444-444444444444", SealID: "55555555-5555-4555-8555-555555555555", SealDigest: "v1:" + strings.Repeat("a", 64), PayloadDigest: "v1:" + strings.Repeat("b", 64)}
	req := RerunClaimRequest{Reference: ref, ClaimID: "66666666-6666-4666-8666-666666666666", AdmissionNonce: bytes.Repeat([]byte("s"), 32)}
	require.NoError(t, req.Validate())
	receipt := RerunClaimReceipt{Reference: ref, ClaimID: req.ClaimID, ClaimedAtUnixMilli: 1}
	require.True(t, receipt.Matches(req))
	for _, format := range []string{"%v", "%+v", "%#v", "%x", "%s"} {
		require.NotContains(t, fmt.Sprintf(format, req), string(req.AdmissionNonce))
		require.NotContains(t, fmt.Sprintf(format, &req), string(req.AdmissionNonce))
		require.NotContains(t, fmt.Sprintf(format, RerunClaimResult{PayloadJSON: []byte("private-payload")}), "private-payload")
	}
	body, err := json.Marshal(req)
	require.NoError(t, err)
	require.NotContains(t, string(body), string(req.AdmissionNonce))
	for name, mutate := range map[string]func(*RerunClaimRequest){"missing": func(r *RerunClaimRequest) { r.Reference.AgentID = "" }, "space": func(r *RerunClaimRequest) { r.Reference.AgentID = " agent" }, "zero": func(r *RerunClaimRequest) { r.Reference.RunID = "00000000-0000-0000-0000-000000000000" }, "duplicate": func(r *RerunClaimRequest) { r.Reference.JobRunID = r.Reference.RunID }, "claim-collision": func(r *RerunClaimRequest) { r.ClaimID = r.Reference.RunID }, "digest": func(r *RerunClaimRequest) { r.Reference.SealDigest = "bad" }, "nonce": func(r *RerunClaimRequest) { r.AdmissionNonce = nil }} {
		t.Run(name, func(t *testing.T) { r := req; mutate(&r); require.Error(t, r.Validate()) })
	}
}
