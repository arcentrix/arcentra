// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/arcentrix/arcentra/internal/control/model"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
)

// ErrCopilotRerunClaimRejected means this call did not commit a new claim and
// cannot disclose payload. It does not prove that an earlier claim is absent.
var ErrCopilotRerunClaimRejected = errors.New("rerun claim request rejected")

// Only the locked, already-claimed Gate comparison may emit this private marker.
// It rejects this requesting incarnation; the earlier claim remains durable and
// its execution outcome is not inferred. No nonce or identity is embedded.
var errCopilotRerunIncarnationMismatch = errors.New("rerun incarnation mismatch")

// AdmissionNonce is transient authenticated request material, never a model or
// receipt field. Formatting the whole request must not disclose its contents.
type CopilotRerunClaimInput struct {
	AgentID, PipelineID, RunID, JobRunID, GateID, ScheduleID, SealID string
	SealDigest, PayloadDigest, ClaimID                               string
	ArtifactStorageID, ArtifactStorageFingerprint                    string
	AdmissionNonce                                                   []byte `json:"-"`
}

func (CopilotRerunClaimInput) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "<private rerun claim request>")
}

type CopilotRerunClaimReceipt struct {
	AgentID, PipelineID, RunID, JobRunID, GateID, ScheduleID, SealID   string
	SealDigest, PayloadDigest, ClaimID                                 string
	ArtifactStorageID, ArtifactStorageFingerprint, ArtifactStorageType string
	ClaimedAtUnixMilli                                                 int64
}
type CopilotRerunClaimResult struct {
	Receipt     *CopilotRerunClaimReceipt
	PayloadJSON []byte `json:"-"`
}

func (CopilotRerunClaimResult) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "<private rerun claim result>")
}

type CopilotRerunClaimMaterial struct {
	Reviewed    *CopilotRerunReviewedTarget
	PayloadJSON []byte `json:"-"`
}

func (CopilotRerunClaimMaterial) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "<private rerun claim material>")
}

// Validation is transaction-local and uses current actor/policy/candidates. It
// decrypts the existing immutable seal, never rebuilds or reseals executable
// bytes. A receipt alone cannot authorize this callback or payload disclosure.
type CopilotRerunClaimValidator func(context.Context, *Repositories, *CopilotRerunScope, *model.CopilotDefinitionSnapshot, *CopilotRerunOriginalEvidence, *CopilotRerunExecutionIdentities, *model.CopilotRerunIntent, *model.Agent, *model.CopilotRerunSchedule) (*CopilotRerunClaimMaterial, error)

func rerunClaimNonceDigest(in CopilotRerunClaimInput) (string, error) {
	if !historyID(in.AgentID) || !historyID(in.PipelineID) || !rerunUniqueUUIDs(in.RunID, in.JobRunID, in.GateID, in.ScheduleID, in.SealID, in.ClaimID) || !sourceprovenance.ValidBindingDigest(in.SealDigest) || !sourceprovenance.ValidBindingDigest(in.PayloadDigest) || len(in.AdmissionNonce) != 32 {
		return "", ErrCopilotRerunInvalid
	}
	if !rerunClaimStorageIdentityValid(in.ArtifactStorageID, in.ArtifactStorageFingerprint) {
		return "", ErrCopilotRerunInvalid
	}
	// JSON is unambiguous; the nonce is encoded only in this transient hash input.
	binding := struct {
		Domain                                                                            string
		Agent, Pipeline, Run, Job, Gate, Schedule, Seal, SealDigest, PayloadDigest, Claim string
		StorageID, StorageFingerprint                                                     string
		Nonce                                                                             []byte
	}{"arcentra-copilot-rerun-admission-nonce-v1", in.AgentID, in.PipelineID, in.RunID, in.JobRunID, in.GateID, in.ScheduleID, in.SealID, in.SealDigest, in.PayloadDigest, in.ClaimID, in.ArtifactStorageID, in.ArtifactStorageFingerprint, in.AdmissionNonce}
	body, err := json.Marshal(binding)
	if err != nil {
		return "", ErrCopilotRerunInvalid
	}
	digest := sha256.Sum256(body)
	for i := range body {
		body[i] = 0
	}
	return "v1:" + hex.EncodeToString(digest[:]), nil
}
func rerunClaimNonceMatches(stored string, in CopilotRerunClaimInput) bool {
	actual, err := rerunClaimNonceDigest(in)
	return err == nil && sourceprovenance.ValidBindingDigest(stored) && subtle.ConstantTimeCompare([]byte(stored), []byte(actual)) == 1
}

func rerunClaimStorageIdentityValid(id, fingerprint string) bool {
	if id == "" || fingerprint == "" {
		return id == "" && fingerprint == ""
	}
	return artifactutil.Identity(id) == nil && artifactutil.Digest(fingerprint) == nil
}
