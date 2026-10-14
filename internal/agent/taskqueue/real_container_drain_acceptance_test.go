//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"fmt"
	"testing"

	"github.com/bytedance/sonic"
)

// containerAcceptanceRetainedAudit 固定已逐条验证的历史隔离证据，不允许把新隔离当作已排空
func containerAcceptanceRetainedAudit(records map[string]terminalRecord) (map[string]string, error) {
	result := make(map[string]string, len(records))
	for key, record := range records {
		if record.Quarantine == nil || key != record.Kind+"/"+record.RunID {
			return nil, fmt.Errorf("retained acceptance audit is not exact quarantined evidence")
		}
		encoded, err := sonic.MarshalString(record)
		if err != nil {
			return nil, err
		}
		result[key] = encoded
	}
	return result, nil
}

func containerAcceptanceAuditMatches(expected map[string]string, actual map[string]terminalRecord) bool {
	if len(expected) != len(actual) {
		return false
	}
	encoded, err := containerAcceptanceRetainedAudit(actual)
	if err != nil {
		return false
	}
	for key, value := range expected {
		if encoded[key] != value {
			return false
		}
	}
	return true
}

type containerAcceptanceDrainState struct {
	terminalRecords int
	exactAudit      bool
	runtimeRecords  int
	recoveries      int
	ready           bool
}

func (s containerAcceptanceDrainState) drained() bool {
	return s.exactAudit && s.runtimeRecords == 0 && s.recoveries == 0 && s.ready
}

func testContainerAcceptanceRetainedAuditDrain(t *testing.T) {
	records := func() map[string]terminalRecord {
		result := make(map[string]terminalRecord)
		for _, name := range []string{"job/old-job", "step/old-step-1", "step/old-step-2"} {
			kind, id := "step", name[len("step/"):]
			if name == "job/old-job" {
				kind, id = "job", "old-job"
			}
			result[name] = terminalRecord{Version: 1, Binding: "original-binding", Kind: kind, RunID: id, ClaimID: "original-claim", Dependencies: []string{"original-dependency"}, Payload: []byte("original-receipt"), Checksum: "original-checksum", Quarantine: &terminalQuarantine{Version: 1, Disposition: terminalConflictDisposition, ReceiptChecksum: "original-checksum", Checksum: "original-quarantine", QuarantinedAt: "original-time"}}
		}
		return result
	}
	baseline := records()
	expected, err := containerAcceptanceRetainedAudit(baseline)
	if err != nil || !containerAcceptanceAuditMatches(expected, baseline) {
		t.Fatalf("exact prior verified audit must survive drain: %v", err)
	}
	if !containerAcceptanceAuditMatches(nil, nil) || containerAcceptanceAuditMatches(nil, baseline) {
		t.Fatal("default drain must still require an empty terminal outbox")
	}
	// The saved snapshot must not alias mutable payload or quarantine fields.
	baseline["job/old-job"].Payload[0] = 'X'
	baseline["job/old-job"].Quarantine.Checksum = "changed"
	if containerAcceptanceAuditMatches(expected, baseline) || !containerAcceptanceAuditMatches(expected, records()) {
		t.Fatal("retained audit snapshot changed with live records")
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]terminalRecord)
	}{
		{"new-pending-receipt", func(m map[string]terminalRecord) { m["job/new"] = terminalRecord{Kind: "job", RunID: "new"} }},
		{"new-quarantine", func(m map[string]terminalRecord) {
			r := m["job/old-job"]
			r.RunID = "new"
			m["job/new"] = r
		}},
		{"removed-audit", func(m map[string]terminalRecord) { delete(m, "job/old-job") }},
		{"changed-payload", func(m map[string]terminalRecord) { m["job/old-job"].Payload[0] = 'X' }},
		{"changed-dependency", func(m map[string]terminalRecord) { m["job/old-job"].Dependencies[0] = "foreign" }},
		{"changed-quarantine", func(m map[string]terminalRecord) { m["job/old-job"].Quarantine.QuarantinedAt = "changed" }},
		{"changed-claim", func(m map[string]terminalRecord) {
			r := m["job/old-job"]
			r.ClaimID = "foreign"
			m["job/old-job"] = r
		}},
		{"no-longer-quarantined", func(m map[string]terminalRecord) {
			r := m["job/old-job"]
			r.Quarantine = nil
			m["job/old-job"] = r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual := records()
			tc.mutate(actual)
			if containerAcceptanceAuditMatches(expected, actual) {
				t.Fatal("drain accepted changed or additional terminal evidence")
			}
		})
	}
	clean := containerAcceptanceDrainState{terminalRecords: len(expected), exactAudit: true, ready: true}
	if !clean.drained() {
		t.Fatal("exact retained audit blocked clean ready runtime")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*containerAcceptanceDrainState)
	}{
		{"receipt-mismatch", func(s *containerAcceptanceDrainState) { s.exactAudit = false }},
		{"runtime-tombstone", func(s *containerAcceptanceDrainState) { s.runtimeRecords = 1 }},
		{"journal-unavailable", func(s *containerAcceptanceDrainState) { s.runtimeRecords = -1 }},
		{"pending-recovery", func(s *containerAcceptanceDrainState) { s.recoveries = 1 }},
		{"not-ready", func(s *containerAcceptanceDrainState) { s.ready = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := clean
			tc.mutate(&state)
			if state.drained() {
				t.Fatal("retained audit bypassed runtime readiness or cleanup")
			}
		})
	}
}
