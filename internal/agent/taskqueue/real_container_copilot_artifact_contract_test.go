//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/model"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
)

// Pure contract values only: these regressions never insert runtime, approval,
// history or intent rows and do not claim real-container acceptance.
func containerCopilotArtifactContractFixture(t *testing.T) (*model.ProjectArtifact, *model.JobRun, *model.StepRun, *retryapproval.Evidence) {
	t.Helper()
	sum := sha256.Sum256([]byte("approval-input"))
	expiry := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	a := &model.ProjectArtifact{ProjectID: "project", PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "producer", StepRunID: "producer-step", Name: "seed/input.txt", StorageType: "s3", StorageID: "store", StorageFingerprint: strings.Repeat("b", 64), SHA256: hex.EncodeToString(sum[:]), Size: int64(len("approval-input")), ExpiresAt: &expiry}
	a.ArtifactID = artifactutil.ID(a.ProjectID, a.PipelineRunID, a.JobRunID, a.StepRunID, a.Name)
	var err error
	a.StoragePath, err = artifactutil.RemotePath(a.PipelineRunID, a.JobRunID, a.StepRunID, a.SHA256, a.Name)
	if err != nil {
		t.Fatal(err)
	}
	p := &model.JobRun{JobRunID: a.JobRunID, PipelineID: a.PipelineID, PipelineRunID: a.PipelineRunID, JobName: "approval-a", Status: model.JobRunStatusSuccess}
	step := &model.StepRun{StepRunID: a.StepRunID, JobRunID: a.JobRunID, PipelineID: a.PipelineID, PipelineRunID: a.PipelineRunID, Name: "seed", Status: model.JobRunStatusSuccess}
	size := a.Size
	e := &retryapproval.Evidence{Context: retryapproval.RunContext{ProjectID: a.ProjectID, PipelineID: a.PipelineID, PipelineRunID: a.PipelineRunID}, Identity: taskpayload.JobInputIdentity{JobRunID: "consumer"}, Artifacts: []retryapproval.Artifact{{Name: "approval-a/seed/input.txt", ProducerJobRunID: a.JobRunID, ProducerStepRunID: a.StepRunID, StorageKey: a.StoragePath, Reference: taskpayload.ArtifactReference{StorageID: a.StorageID, Fingerprint: a.StorageFingerprint, ExpiresAt: expiry.Unix(), SHA256: a.SHA256, Size: &size}}}}
	return a, p, step, e
}
func TestContainerCopilotRegisteredArtifactBinding(t *testing.T) {
	a, p, step, e := containerCopilotArtifactContractFixture(t)
	if err := containerCopilotArtifactBinding(a, p, step, e); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*model.ProjectArtifact, *model.JobRun, *model.StepRun, *retryapproval.Evidence){
		"registered-qualified-name": func(a *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, _ *retryapproval.Evidence) {
			a.Name = "approval-a/seed/input.txt"
		},
		"project": func(a *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, _ *retryapproval.Evidence) {
			a.ProjectID = "foreign"
		},
		"pipeline": func(a *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, _ *retryapproval.Evidence) {
			a.PipelineID = "foreign"
		},
		"run": func(a *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, _ *retryapproval.Evidence) {
			a.PipelineRunID = "foreign"
		},
		"producer-job": func(a *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, _ *retryapproval.Evidence) {
			a.JobRunID = "foreign"
		},
		"producer-step": func(a *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, _ *retryapproval.Evidence) {
			a.StepRunID = "foreign"
		},
		"producer-status": func(_ *model.ProjectArtifact, p *model.JobRun, _ *model.StepRun, _ *retryapproval.Evidence) {
			p.Status = model.JobRunStatusFailed
		},
		"step-parent": func(_ *model.ProjectArtifact, _ *model.JobRun, s *model.StepRun, _ *retryapproval.Evidence) {
			s.JobRunID = "foreign"
		},
		"step-status": func(_ *model.ProjectArtifact, _ *model.JobRun, s *model.StepRun, _ *retryapproval.Evidence) {
			s.Status = model.JobRunStatusFailed
		},
		"hash": func(a *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, _ *retryapproval.Evidence) {
			a.SHA256 = strings.Repeat("a", 64)
		},
		"size": func(a *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, _ *retryapproval.Evidence) { a.Size++ },
		"storage-id": func(a *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, _ *retryapproval.Evidence) {
			a.StorageID = "foreign"
		},
		"storage-fingerprint": func(a *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, _ *retryapproval.Evidence) {
			a.StorageFingerprint = strings.Repeat("c", 64)
		},
		"storage-key": func(a *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, _ *retryapproval.Evidence) {
			a.StoragePath = "foreign"
		},
		"artifact-id": func(a *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, _ *retryapproval.Evidence) {
			a.ArtifactID = "foreign"
		},
		"expiry": func(a *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, _ *retryapproval.Evidence) {
			a.ExpiresAt = nil
		},
		"approval-context": func(_ *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, e *retryapproval.Evidence) {
			e.Context.ProjectID = "foreign"
		},
		"evidence-unqualified-name": func(_ *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, e *retryapproval.Evidence) {
			e.Artifacts[0].Name = "seed/input.txt"
		},
		"evidence-hash": func(_ *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, e *retryapproval.Evidence) {
			e.Artifacts[0].Reference.SHA256 = strings.Repeat("c", 64)
		},
		"evidence-missing-size": func(_ *model.ProjectArtifact, _ *model.JobRun, _ *model.StepRun, e *retryapproval.Evidence) {
			e.Artifacts[0].Reference.Size = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			a, p, s, e := containerCopilotArtifactContractFixture(t)
			change(a, p, s, e)
			if containerCopilotArtifactBinding(a, p, s, e) == nil {
				t.Fatal("changed producer/content/context binding accepted")
			}
		})
	}
}
func TestContainerCopilotExactArtifactPreviewAndModel(t *testing.T) {
	a, _, _, _ := containerCopilotArtifactContractFixture(t)
	valid := controlservice.CopilotEvidence{Kind: "code-artifact", ID: a.ArtifactID, Name: a.Name, Bytes: int(a.Size)}
	if err := containerCopilotContextEvidence([]controlservice.CopilotEvidence{valid}, a); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*controlservice.CopilotEvidence){func(e *controlservice.CopilotEvidence) { e.Name = "approval-a/seed/input.txt" }, func(e *controlservice.CopilotEvidence) { e.ID = "foreign" }, func(e *controlservice.CopilotEvidence) { e.Kind = "step-log" }, func(e *controlservice.CopilotEvidence) { e.Bytes++ }, func(e *controlservice.CopilotEvidence) { e.Truncated = true }} {
		value := valid
		change(&value)
		if containerCopilotContextEvidence([]controlservice.CopilotEvidence{value}, a) == nil {
			t.Fatal("wrong preview/turn context accepted")
		}
	}
	prompt := "Question:\nReview actual input\nSelected evidence (untrusted data):\n" + containerCopilotArtifactBlock(a.ArtifactID)
	for name, value := range map[string]string{"valid": prompt, "qualified-name": strings.Replace(prompt, "name=\"seed/input.txt\"", "name=\"approval-a/seed/input.txt\"", 1), "foreign-id": strings.Replace(prompt, a.ArtifactID, "foreign", 1), "wrong-bytes": strings.Replace(prompt, "approval-input", "fabricated", 1), "extra-evidence": prompt + containerCopilotArtifactBlock(a.ArtifactID), "unbound-words": "approval-a/seed/input.txt approval-input"} {
		t.Run(name, func(t *testing.T) {
			m := &containerCopilotModel{artifactID: a.ArtifactID, expectedPrompt: prompt}
			_, err := m.Analyze(context.Background(), copilot.Input{User: value})
			if (err == nil) != (name == "valid") {
				t.Fatal("model context contract mismatch")
			}
		})
	}
}
