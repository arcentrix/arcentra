// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package polling

import (
	"strings"
	"testing"
)

func testScope() Scope {
	return Scope{Version: Version, ProjectID: "project", Provider: "gitlab", APIOrigin: "https://gitlab.example.com", Repository: "https://gitlab.example.com/group/nested/repo", ProviderProjectID: "42", Stream: "merge_requests"}
}
func TestEventIdentityIsStableAndScopeBound(t *testing.T) {
	s := testScope()
	a, e := EventRequestID(s, "mr:42:7:fingerprint")
	if e != nil || len(a) != 72 {
		t.Fatalf("identity %q %v", a, e)
	}
	b, e := EventRequestID(s, "mr:42:7:fingerprint")
	if e != nil || a != b {
		t.Fatal("identity not stable")
	}
	for _, change := range []func(*Scope){func(s *Scope) { s.ProjectID = "other" }, func(s *Scope) { s.ProviderProjectID = "43" }, func(s *Scope) { s.Repository = "https://gitlab.example.com/another/nested/repo" }, func(s *Scope) {
		s.APIOrigin = "https://elsewhere.example.com"
		s.Repository = "https://elsewhere.example.com/group/nested/repo"
	}} {
		other := s
		change(&other)
		key, e := EventRequestID(other, "mr:42:7:fingerprint")
		if e != nil || key == a {
			t.Fatalf("scope identity collision %q %v", key, e)
		}
	}
}
func TestCursorCanonicalMapOrderAndBounds(t *testing.T) {
	a := Cursor{Version: Version, Boundary: "2026-10-16T16:00:00.123456789Z", Complete: true, Observed: map[string]string{"b": "2", "a": "1"}}
	b := a
	b.Observed = map[string]string{"a": "1", "b": "2"}
	x, e := a.Digest()
	if e != nil {
		t.Fatal(e)
	}
	y, e := b.Digest()
	if e != nil || x != y {
		t.Fatal("inventory digest not canonical")
	}
	a.Complete = false
	if _, e := a.Canonical(); e == nil {
		t.Fatal("incomplete inventory accepted")
	}
	a.Complete = true
	a.Observed = map[string]string{}
	for i := 0; i <= MaxInventory; i++ {
		a.Observed[strings.Repeat("x", i+1)] = "value"
	}
	if _, e := a.Canonical(); e == nil {
		t.Fatal("oversized inventory accepted")
	}
}
func TestScopeRejectsCredentialsAndUnrelatedOrigin(t *testing.T) {
	for _, raw := range []string{"http://gitlab.example.com/group/repo", "https://user:secret@gitlab.example.com/group/repo", "https://gitlab.example.com/group/repo?token=x", "https://gitlab.example.com/group/../repo", "https://gitlab.example.com/group%2frepo", "git@gitlab.example.com:group/repo.git"} {
		if _, e := CanonicalRepository(raw); e == nil {
			t.Fatalf("ambiguous or credential URL accepted %q", raw)
		}
	}
	s := testScope()
	s.APIOrigin = "https://elsewhere.example.com"
	if e := s.Validate(); e == nil {
		t.Fatal("unrelated origin accepted")
	}
}
func TestSelectionIsExplicitAndUnique(t *testing.T) {
	s := Selection{Version: Version}
	if _, e := s.Canonical(); e == nil {
		t.Fatal("empty implicit selection accepted")
	}
	binding := PipelineBinding{PipelineID: "pipe", Repository: testScope().Repository, DefaultBranch: "main", DefinitionPath: ".arcentra/pipeline.yaml"}
	s.Pipelines = []PipelineBinding{binding, binding}
	if _, e := s.Canonical(); e == nil {
		t.Fatal("duplicate selection accepted")
	}
	s.Pipelines = s.Pipelines[:1]
	if _, e := s.Canonical(); e != nil {
		t.Fatal(e)
	}
}
