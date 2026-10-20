// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"encoding/json"
	"strings"
	"testing"
)

func sourceExpectationFixture() (*SourceCheckoutExpectation, JobInputIdentity) {
	return &SourceCheckoutExpectation{1, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", strings.Repeat("a", 40)}, JobInputIdentity{"pipeline", "run", "job", "agent", "claim", "v1:" + strings.Repeat("b", 64), "v1:" + strings.Repeat("c", 64)}
}
func TestSourceExpectationCanonicalAndIdentityBinding(t *testing.T) {
	e, i := sourceExpectationFixture()
	body, err := e.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ParseSourceCheckoutExpectation(body); err != nil || *got != *e {
		t.Fatal(got, err)
	}
	digest, err := e.Digest(i)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*JobInputIdentity){func(i *JobInputIdentity) { i.PipelineID += "x" }, func(i *JobInputIdentity) { i.PipelineRunID += "x" }, func(i *JobInputIdentity) { i.JobRunID += "x" }, func(i *JobInputIdentity) { i.AgentID += "x" }, func(i *JobInputIdentity) { i.ClaimID += "x" }, func(i *JobInputIdentity) { i.RuntimeBindingDigest = "v1:" + strings.Repeat("d", 64) }, func(i *JobInputIdentity) { i.SourceBindingDigest = "v1:" + strings.Repeat("d", 64) }} {
		changed := i
		mutate(&changed)
		d, err := e.Digest(changed)
		if err != nil || d == digest {
			t.Fatal("identity not bound", err)
		}
	}

	for _, mutate := range []func(*SourceCheckoutExpectation){func(e *SourceCheckoutExpectation) { e.ScheduleID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc" }, func(e *SourceCheckoutExpectation) { e.SealID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc" }, func(e *SourceCheckoutExpectation) { e.ExpectedSourceCommit = strings.Repeat("d", 40) }} {
		changed := *e
		mutate(&changed)
		d, err := changed.Digest(i)
		if err != nil || d == digest {
			t.Fatal("expectation field not bound", err)
		}
	}
	for _, bad := range []string{body + " ", strings.Replace(body, `"version":1`, `"version":1,"extra":true`, 1), strings.Replace(body, `"version":1`, `"version":1,"version":1`, 1), strings.Replace(body, `"version":1`, `"version":2`, 1), strings.Replace(body, e.ScheduleID, strings.ToUpper(e.ScheduleID), 1), strings.Replace(body, e.SealID, e.ScheduleID, 1), strings.Replace(body, e.ExpectedSourceCommit, "abcd", 1)} {
		if _, err := ParseSourceCheckoutExpectation(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	m, _ := NewJobInputManifest(e.ExpectedSourceCommit, nil, nil)
	if e.ValidateObservation(nil) != nil || e.ValidateObservation(m) != nil {
		t.Fatal("valid observation rejected")
	}
	m.SourceCommit = strings.Repeat("d", 40)
	if e.ValidateObservation(m) == nil {
		t.Fatal("wrong source accepted")
	}
	// The field is inside canonical task serialization, absent for legacy payloads.
	p := JobRunTaskPayload{}
	before, _ := json.Marshal(p)
	p.SourceExpectation = e
	after, _ := json.Marshal(p)
	if string(before) == string(after) || strings.Contains(string(before), "sourceExpectation") {
		t.Fatal("payload binding missing")
	}
}
