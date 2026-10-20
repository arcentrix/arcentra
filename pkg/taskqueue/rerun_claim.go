// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
)

// ErrRerunClaimRejected may be returned only for a proven rejection. Database
// commit/ACK ambiguity and arbitrary callback errors must never wrap it.
// Rejection never proves absence of an earlier committed claim.
var ErrRerunClaimRejected = errors.New("protected rerun claim rejected")

// MaxRerunClaimPayloadBytes matches the bounded sealed dispatch envelope.
const MaxRerunClaimPayloadBytes = 512 * 1024

// RerunClaimReference is the reference-only notification. No executable bytes or
// nonce belong in a persistent queue notification.
type RerunClaimReference struct {
	AgentID       string `json:"agentId"`
	PipelineID    string `json:"pipelineId"`
	RunID         string `json:"runId"`
	JobRunID      string `json:"jobRunId"`
	GateID        string `json:"gateId"`
	ScheduleID    string `json:"scheduleId"`
	SealID        string `json:"sealId"`
	SealDigest    string `json:"sealDigest"`
	PayloadDigest string `json:"payloadDigest"`
}

func (r RerunClaimReference) Validate() error {
	for _, id := range []string{r.AgentID, r.PipelineID} {
		if id == "" || len(id) > 64 || strings.TrimSpace(id) != id {
			return fmt.Errorf("invalid rerun reference")
		}
		for _, c := range id {
			if c < 33 || c > 126 {
				return fmt.Errorf("invalid rerun reference")
			}
		}
	}
	seen := map[string]bool{}
	for _, id := range []string{r.RunID, r.JobRunID, r.GateID, r.ScheduleID, r.SealID} {
		if !canonicalUUID(id) || seen[id] {
			return fmt.Errorf("invalid rerun reference")
		}
		seen[id] = true
	}
	if !sourceprovenance.ValidBindingDigest(r.SealDigest) || !sourceprovenance.ValidBindingDigest(r.PayloadDigest) {
		return fmt.Errorf("invalid rerun reference")
	}
	return nil
}

// RerunClaimRequest is transient and must never be logged or persisted. The
// generated protobuf wrapper does NOT share this formatting protection.
type RerunClaimRequest struct {
	Reference                  RerunClaimReference
	ClaimID                    string
	AdmissionNonce             []byte `json:"-"`
	ArtifactStorageID          string
	ArtifactStorageFingerprint string
}

func (r RerunClaimRequest) Validate() error {
	if r.Reference.Validate() != nil || !canonicalUUID(r.ClaimID) || len(r.AdmissionNonce) != 32 || ValidateRerunArtifactStorage(r.ArtifactStorageID, r.ArtifactStorageFingerprint) != nil {
		return fmt.Errorf("invalid rerun claim")
	}
	for _, id := range []string{r.Reference.RunID, r.Reference.JobRunID, r.Reference.GateID, r.Reference.ScheduleID, r.Reference.SealID} {
		if id == r.ClaimID {
			return fmt.Errorf("invalid rerun claim")
		}
	}
	return nil
}
func (RerunClaimRequest) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[private rerun claim]")
}

// Receipt is evidence of controlled preparation admission, not permission to
// execute user commands or fetch payload after current authority is revoked.
type RerunClaimReceipt struct {
	Reference                  RerunClaimReference
	ClaimID                    string
	ClaimedAtUnixMilli         int64
	ArtifactStorageID          string
	ArtifactStorageFingerprint string
	ArtifactStorageType        string
}

func (r RerunClaimReceipt) Matches(in RerunClaimRequest) bool {
	return r.Reference == in.Reference && r.ClaimID == in.ClaimID && r.ClaimedAtUnixMilli > 0 && r.ArtifactStorageID == in.ArtifactStorageID && r.ArtifactStorageFingerprint == in.ArtifactStorageFingerprint && ((r.ArtifactStorageID == "" && r.ArtifactStorageType == "") || (r.ArtifactStorageID != "" && validRerunStorageType(r.ArtifactStorageType)))
}

type RerunClaimResult struct {
	Receipt     RerunClaimReceipt
	PayloadJSON []byte `json:"-"`
}

func (RerunClaimResult) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[private rerun claim result]")
}

// RerunPayloadBytesDigest hashes the original sealed bytes without serializing
// a decoded object. Domain matches the existing canonical Seal payload digest.
func RerunPayloadBytesDigest(body []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte("arcentra-copilot-rerun-payload-v1\x00"))
	_, _ = h.Write(body)
	return "v1:" + hex.EncodeToString(h.Sum(nil))
}

const TaskTypeCopilotRerunReference = "pipeline.copilot_rerun_reference"

// ParseRerunClaimReference rejects ambiguous or oversized notification bodies.
func ParseRerunClaimReference(body []byte) (*RerunClaimReference, error) {
	if len(body) == 0 || len(body) > 4096 {
		return nil, fmt.Errorf("invalid rerun reference")
	}
	var ref RerunClaimReference
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&ref) != nil || d.Decode(new(any)) != io.EOF || ref.Validate() != nil {
		return nil, fmt.Errorf("invalid rerun reference")
	}
	canonical, err := json.Marshal(ref)
	if err != nil || !bytes.Equal(canonical, body) {
		return nil, fmt.Errorf("noncanonical rerun reference")
	}
	return &ref, nil
}

// ParseRerunClaimPayload validates the exact original sealed bytes. Its decoded
// result is transient executable material and must never be queued or logged.
func ParseRerunClaimPayload(ref RerunClaimReference, body []byte) (*JobRunTaskPayload, error) {
	if len(body) == 0 || len(body) > MaxRerunClaimPayloadBytes || RerunPayloadBytesDigest(body) != ref.PayloadDigest {
		return nil, fmt.Errorf("invalid sealed rerun payload")
	}
	var p JobRunTaskPayload
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid sealed rerun payload")
	}
	canonical, err := json.Marshal(&p)
	if err != nil || !bytes.Equal(canonical, body) || p.AgentID != ref.AgentID || p.PipelineID != ref.PipelineID || p.PipelineRunID != ref.RunID || p.JobRunID != ref.JobRunID || p.SourceExpectation == nil || p.SourceExpectation.ScheduleID != ref.ScheduleID || p.SourceExpectation.SealID != ref.SealID || p.Runtime == nil || p.Runtime.Type != RuntimeContainerdOfflineV1 || p.Source == nil || p.Source.Type != "git" || p.Source.Commit != "" || len(p.Source.Auth) != 0 || len(p.ArtifactURIs) != 0 || len(p.ArtifactReferences) != 0 || !p.RetryApprovalRequired || ValidateWholeJobRetryPolicy(p.Retry) != nil || ValidateJobRetryPlan(&p) != nil {
		return nil, fmt.Errorf("invalid sealed rerun payload")
	}
	if len(p.Steps) < 1 || len(p.Steps) > 32 || p.Workspace != "" {
		return nil, fmt.Errorf("unsupported rerun layout")
	}
	hasShell, hasOutput := false, false
	for _, step := range p.Steps {
		shell, output := RerunShellStep(step.Uses, step.Action), RerunArtifactUploadStep(step.Uses, step.Action)
		if (!shell && !output) || (shell && hasOutput) || (output && !hasShell) || step.When != "" || step.ContinueOnError {
			return nil, fmt.Errorf("unsupported rerun layout")
		}
		hasShell = hasShell || shell
		hasOutput = hasOutput || output
	}
	if !hasShell {
		return nil, fmt.Errorf("unsupported rerun layout")
	}
	runtimeDigest, runtimeErr := RuntimeBindingDigest(p.PipelineID, p.PipelineRunID, p.JobRunID, p.AgentID, p.Runtime)
	source, sourceErr := sourceprovenance.NewBinding(p.PipelineID, p.PipelineRunID, p.JobRunID, p.Source.Type, p.Source.Repo, p.Source.Branch)
	if runtimeErr != nil || runtimeDigest != p.Runtime.BindingDigest || sourceErr != nil || source.Digest != p.Source.BindingDigest {
		return nil, fmt.Errorf("invalid sealed rerun payload")
	}
	_, err = p.SourceExpectation.Canonical()
	if err != nil {
		return nil, fmt.Errorf("invalid sealed rerun payload")
	}
	return &p, nil
}

// ValidateRerunArtifactStorage binds a configured output destination, never credentials.
func ValidateRerunArtifactStorage(id, fingerprint string) error {
	if id == "" && fingerprint == "" {
		return nil
	}
	if artifactutil.Identity(id) != nil || artifactutil.Digest(fingerprint) != nil {
		return fmt.Errorf("invalid rerun artifact storage")
	}
	return nil
}
func validRerunStorageType(t string) bool {
	switch t {
	case "s3", "minio", "oss", "gcs", "cos":
		return true
	}
	return false
}
