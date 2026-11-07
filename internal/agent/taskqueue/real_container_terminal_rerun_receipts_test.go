//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"maps"
	"testing"

	"github.com/bytedance/sonic"
)

func terminalRerunBrowserReceiptName(phase string) (string, bool) {
	switch phase {
	case "prepare":
		return "preparation", true
	case "outcome", "owner-outcome":
		return phase, true
	default:
		return "", false
	}
}

// These are browser observations, not authority to create or approve an execution.
type terminalRerunResponseRecovery struct {
	PrepareResponseLost bool `json:"prepareResponseLost"`
	ConfirmResponseLost bool `json:"confirmResponseLost"`
	Reloads             int  `json:"reloads"`
	PrepareInspections  int  `json:"prepareInspections"`
	ConfirmInspections  int  `json:"confirmInspections"`
}

func (r terminalRerunResponseRecovery) valid() bool {
	return r == (terminalRerunResponseRecovery{PrepareResponseLost: true, ConfirmResponseLost: true, Reloads: 2, PrepareInspections: 1, ConfirmInspections: 1})
}

// Only independently observed owner output is compared here. Approval evidence
// remains the second reviewer's and the Go fixture's responsibility.
type terminalRerunOwnerOutputBinding struct {
	Version               string `json:"version"`
	ActorID               string `json:"actorId"`
	RequesterID           string `json:"requesterId"`
	ProjectID             string `json:"projectId"`
	PipelineID            string `json:"pipelineId"`
	OriginalRunID         string `json:"originalRunId"`
	OriginalJobRunID      string `json:"originalJobRunId"`
	OriginalRunStatus     int    `json:"originalRunStatus"`
	OriginalJobStatus     int    `json:"originalJobStatus"`
	RequestID             string `json:"requestId"`
	IntentID              string `json:"intentId"`
	NewRunID              string `json:"newRunId"`
	NewJobRunID           string `json:"newJobRunId"`
	NewRunStatus          int    `json:"newRunStatus"`
	NewJobStatus          int    `json:"newJobStatus"`
	SourceCommit          string `json:"sourceCommit"`
	CommandStepRunID      string `json:"commandStepRunId"`
	UploadStepRunID       string `json:"uploadStepRunId"`
	CommandMarkerObserved bool   `json:"commandMarkerObserved"`
	ArtifactID            string `json:"artifactId"`
	ArtifactSize          int64  `json:"artifactSize"`
	ArtifactSHA256        string `json:"artifactSha256"`
}

type terminalRerunOwnerOutcomeReceipt struct {
	terminalRerunOwnerOutputBinding
	Phase                  string `json:"phase"`
	OwnerOutcomeComplete   bool   `json:"ownerOutcomeComplete"`
	BrowserOutcomeComplete bool   `json:"browserOutcomeComplete"`
	PreparePosts           *int   `json:"preparePosts"`
	ConfirmPosts           *int   `json:"confirmPosts"`
	ApprovalPosts          *int   `json:"approvalPosts"`
	ArtifactDownloads      *int   `json:"artifactDownloads"`
}

func (r terminalRerunOwnerOutcomeReceipt) validFor(want terminalRerunOwnerOutputBinding) bool {
	return r.Phase == "browser-owner-output-observed" && r.OwnerOutcomeComplete && !r.BrowserOutcomeComplete &&
		r.terminalRerunOwnerOutputBinding == want &&
		r.PreparePosts != nil && *r.PreparePosts == 0 && r.ConfirmPosts != nil && *r.ConfirmPosts == 0 &&
		r.ApprovalPosts != nil && *r.ApprovalPosts == 0 && r.ArtifactDownloads != nil && *r.ArtifactDownloads == 1
}

func TestContainerTerminalRerunBrowserPhaseReceipts(t *testing.T) {
	for phase, want := range map[string]string{"prepare": "preparation", "outcome": "outcome", "owner-outcome": "owner-outcome"} {
		got, ok := terminalRerunBrowserReceiptName(phase)
		if !ok || got != want {
			t.Fatalf("phase %q did not retain its separate receipt", phase)
		}
	}
	for _, phase := range []string{"", "owner", "owner-outcome/../outcome", "preparation", "../prepare", "OUTCOME"} {
		if name, ok := terminalRerunBrowserReceiptName(phase); ok || name != "" {
			t.Fatalf("invalid phase %q received a receipt path", phase)
		}
	}
}

func TestContainerTerminalRerunResponseRecoveryReceipt(t *testing.T) {
	valid := terminalRerunResponseRecovery{PrepareResponseLost: true, ConfirmResponseLost: true, Reloads: 2, PrepareInspections: 1, ConfirmInspections: 1}
	if !valid.valid() {
		t.Fatal("exact two-loss/two-reload recovery was rejected")
	}
	for name, mutate := range map[string]func(*terminalRerunResponseRecovery){
		"prepare response delivered": func(r *terminalRerunResponseRecovery) { r.PrepareResponseLost = false },
		"confirm response delivered": func(r *terminalRerunResponseRecovery) { r.ConfirmResponseLost = false },
		"missing reload":             func(r *terminalRerunResponseRecovery) { r.Reloads = 1 },
		"extra reload":               func(r *terminalRerunResponseRecovery) { r.Reloads = 3 },
		"prepare not inspected":      func(r *terminalRerunResponseRecovery) { r.PrepareInspections = 0 },
		"prepare inspected twice":    func(r *terminalRerunResponseRecovery) { r.PrepareInspections = 2 },
		"confirm not inspected":      func(r *terminalRerunResponseRecovery) { r.ConfirmInspections = 0 },
		"confirm inspected twice":    func(r *terminalRerunResponseRecovery) { r.ConfirmInspections = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			got := valid
			mutate(&got)
			if got.valid() {
				t.Fatal("inexact response recovery evidence was accepted")
			}
		})
	}
	if (terminalRerunResponseRecovery{}).valid() {
		t.Fatal("absent recovery evidence was accepted")
	}
}

func TestContainerTerminalRerunOwnerOutcomeReceipt(t *testing.T) {
	want := terminalRerunOwnerOutputBinding{
		Version: "copilot-terminal-rerun-outcome-browser-v1", ActorID: "owner", RequesterID: "owner", ProjectID: "project", PipelineID: "pipeline",
		OriginalRunID: "old-run", OriginalJobRunID: "old-job", OriginalRunStatus: 4, OriginalJobStatus: 5,
		RequestID: "request", IntentID: "intent", NewRunID: "new-run", NewJobRunID: "new-job", NewRunStatus: 3, NewJobStatus: 4,
		SourceCommit: "source", CommandStepRunID: "command-step", UploadStepRunID: "upload-step", CommandMarkerObserved: true,
		ArtifactID: "artifact", ArtifactSize: 37, ArtifactSHA256: "hash",
	}
	zero, one := 0, 1
	valid := terminalRerunOwnerOutcomeReceipt{terminalRerunOwnerOutputBinding: want, Phase: "browser-owner-output-observed", OwnerOutcomeComplete: true, PreparePosts: &zero, ConfirmPosts: &zero, ApprovalPosts: &zero, ArtifactDownloads: &one}
	raw, err := sonic.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := sonic.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, body map[string]any, accepted bool) {
		t.Helper()
		raw, err := sonic.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var got terminalRerunOwnerOutcomeReceipt
		if err := sonic.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.validFor(want) != accepted {
			t.Fatal("owner receipt acceptance did not match exact observed output")
		}
	}
	check(t, fields, true)
	// Mutating or omitting any bound output fact, completion flag or counter
	// must fail. Only the reviewer-specific completion flag may be omitted.
	for key, original := range fields {
		t.Run(key, func(t *testing.T) {
			fields := maps.Clone(fields)
			delete(fields, key)
			check(t, fields, key == "browserOutcomeComplete")
			switch v := original.(type) {
			case string:
				fields[key] = v + "-different"
			case float64:
				fields[key] = v + 1
			case bool:
				fields[key] = !v
			}
			check(t, fields, false)
			fields[key] = original
		})
	}
}
