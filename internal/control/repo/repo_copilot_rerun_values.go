// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/google/uuid"
	"time"
)

type CopilotRerunStepIdentities struct {
	Name     string   `json:"name"`
	Index    int32    `json:"index"`
	Attempts []string `json:"attempts"`
}
type CopilotRerunExecutionIdentities struct {
	GateID     string                       `json:"gateId"`
	RunID      string                       `json:"runId"`
	JobRunID   string                       `json:"jobRunId"`
	ScheduleID string                       `json:"scheduleId"`
	Steps      []CopilotRerunStepIdentities `json:"steps"`
}

func NewCopilotRerunExecutionIdentities(layout *CopilotRerunLayout) (*CopilotRerunExecutionIdentities, error) {
	if _, e := layout.Canonical(); e != nil {
		return nil, e
	}
	ids := &CopilotRerunExecutionIdentities{GateID: uuid.NewString(), RunID: uuid.NewString(), JobRunID: uuid.NewString(), ScheduleID: uuid.NewString(), Steps: make([]CopilotRerunStepIdentities, len(layout.Steps))}
	for i, s := range layout.Steps {
		ids.Steps[i] = CopilotRerunStepIdentities{Name: s.Name, Index: s.Index, Attempts: make([]string, int(layout.Retry.MaxAttempts))}
		for j := range ids.Steps[i].Attempts {
			ids.Steps[i].Attempts[j] = uuid.NewString()
		}
	}
	return ids, nil
}
func (v *CopilotRerunExecutionIdentities) Validate(layout *CopilotRerunLayout) error {
	if _, e := layout.Canonical(); e != nil || v == nil || len(v.Steps) != len(layout.Steps) {
		return ErrCopilotRerunInvalid
	}
	seen := map[string]bool{}
	add := func(id string) bool {
		if !rerunUUID(id) || seen[id] {
			return false
		}
		seen[id] = true
		return true
	}
	if !add(v.GateID) || !add(v.RunID) || !add(v.JobRunID) || !add(v.ScheduleID) {
		return ErrCopilotRerunInvalid
	}
	for i, s := range v.Steps {
		if s.Name != layout.Steps[i].Name || s.Index != layout.Steps[i].Index || len(s.Attempts) != int(layout.Retry.MaxAttempts) {
			return ErrCopilotRerunInvalid
		}
		for _, id := range s.Attempts {
			if !add(id) {
				return ErrCopilotRerunInvalid
			}
		}
	}
	return nil
}

// This immutable seal excludes the controller lease epoch intentionally. A
// replacement controller republishes identical ciphertext/IDs. A late identical
// publication is not invalidated merely because another controller owns a lease.
// Validation is integrity-only, never current first-claim authorization.
type CopilotRerunSealBinding struct {
	Version              int    `json:"version"`
	SealID               string `json:"sealId"`
	ScheduleID           string `json:"scheduleId"`
	RunID                string `json:"runId"`
	JobRunID             string `json:"jobRunId"`
	AgentID              string `json:"agentId"`
	PayloadDigest        string `json:"payloadDigest"`
	CipherVersion        uint32 `json:"cipherVersion"`
	CiphertextDigest     string `json:"ciphertextDigest"`
	SecretVersionsDigest string `json:"secretVersionsDigest"`
	ExpiresAtUnixMilli   int64  `json:"expiresAtUnixMilli"`
}

// NewCopilotRerunSealMetadata allocates the seal identity before encryption.
// Input must not carry a caller-selected seal identity or ciphertext hash.
func NewCopilotRerunSealMetadata(in CopilotRerunSealBinding) (*CopilotRerunSealBinding, error) {
	if in.SealID != "" || in.CiphertextDigest != "" {
		return nil, ErrCopilotRerunInvalid
	}
	in.SealID = uuid.NewString()
	if _, err := in.ContentBinding(); err != nil {
		return nil, err
	}
	return &in, nil
}

// NewCopilotRerunSealIdentity allocates identity before payload construction.
// The payload then embeds this SealID before its full digest is calculated.
func NewCopilotRerunSealIdentity(in CopilotRerunSealBinding) (*CopilotRerunSealBinding, error) {
	if in.SealID != "" || in.PayloadDigest != "" || in.CiphertextDigest != "" {
		return nil, ErrCopilotRerunInvalid
	}
	in.SealID = uuid.NewString()
	if !rerunSealIdentityValid(&in) {
		return nil, ErrCopilotRerunInvalid
	}
	return &in, nil
}
func BindCopilotRerunSealPayload(identity *CopilotRerunSealBinding, digest string) (*CopilotRerunSealBinding, error) {
	if identity == nil || identity.PayloadDigest != "" || identity.CiphertextDigest != "" {
		return nil, ErrCopilotRerunInvalid
	}
	out := *identity
	out.PayloadDigest = digest
	if _, err := out.ContentBinding(); err != nil {
		return nil, err
	}
	return &out, nil
}

func rerunSealIdentityValid(v *CopilotRerunSealBinding) bool {
	return v != nil && v.Version == 1 && rerunUniqueUUIDs(v.SealID, v.ScheduleID, v.RunID, v.JobRunID) && historyID(v.AgentID) && v.CipherVersion == 1 && sourceprovenance.ValidBindingDigest(v.SecretVersionsDigest) && v.ExpiresAtUnixMilli > 0
}
func (v *CopilotRerunSealBinding) ContentBinding() (string, error) {
	if !rerunSealIdentityValid(v) || !sourceprovenance.ValidBindingDigest(v.PayloadDigest) {
		return "", ErrCopilotRerunInvalid
	}
	return rerunDigest("arcentra-copilot-rerun-dispatch-content-v1", []any{v.Version, v.SealID, v.ScheduleID, v.RunID, v.JobRunID, v.AgentID, v.PayloadDigest, v.CipherVersion, v.SecretVersionsDigest, v.ExpiresAtUnixMilli}), nil
}
func FinalizeCopilotRerunSeal(metadata *CopilotRerunSealBinding, expectedContentBinding string, ciphertext []byte) (*CopilotRerunSealBinding, error) {
	content, err := metadata.ContentBinding()
	if err != nil || content != expectedContentBinding || metadata.CiphertextDigest != "" || len(ciphertext) < CopilotRerunMinCiphertextBytes || len(ciphertext) > CopilotRerunMaxDefinitionBytes {
		return nil, ErrCopilotRerunInvalid
	}
	out := *metadata
	out.CiphertextDigest = rerunCipherDigest(ciphertext)
	return &out, nil
}

func (v *CopilotRerunSealBinding) Digest() (string, error) {
	if v == nil || v.Version != 1 || !rerunUniqueUUIDs(v.SealID, v.ScheduleID, v.RunID, v.JobRunID) || !historyID(v.AgentID) || !sourceprovenance.ValidBindingDigest(v.PayloadDigest) || v.CipherVersion != 1 || !rerunHash(v.CiphertextDigest) || !sourceprovenance.ValidBindingDigest(v.SecretVersionsDigest) || v.ExpiresAtUnixMilli <= 0 {
		return "", ErrCopilotRerunInvalid
	}
	return rerunDigest("arcentra-copilot-rerun-seal-v1", v), nil
}
func ValidateCopilotRerunSealedBinding(schedule *model.CopilotRerunSchedule, binding *CopilotRerunSealBinding) error {
	digest, e := binding.Digest()
	if e != nil || schedule == nil || schedule.SealID == nil || *schedule.SealID != binding.SealID || schedule.ScheduleID != binding.ScheduleID || schedule.RunID != binding.RunID || schedule.JobRunID != binding.JobRunID || schedule.AgentID != binding.AgentID || schedule.PayloadDigest != binding.PayloadDigest || schedule.CipherVersion != binding.CipherVersion || schedule.CiphertextDigest != binding.CiphertextDigest || schedule.SecretVersionsDigest != binding.SecretVersionsDigest || schedule.ExpiresAt.UnixMilli() != binding.ExpiresAtUnixMilli || schedule.SealDigest != digest || len(schedule.PayloadCiphertext) < CopilotRerunMinCiphertextBytes || len(schedule.PayloadCiphertext) > CopilotRerunMaxDefinitionBytes || rerunCipherDigest(schedule.PayloadCiphertext) != binding.CiphertextDigest {
		return ErrCopilotRerunInvalid
	}
	return nil
}

// Controller lease checks fence controller writes only. A true result is not
// permission to publish, assign an Agent, claim a job or resolve any secret.
func CopilotRerunControllerLeaseCurrent(schedule *model.CopilotRerunSchedule, holder string, epoch uint64, now time.Time) bool {
	return schedule != nil && (schedule.State == "pending" || schedule.State == "sealed") && !schedule.CancelRequested && schedule.ClaimedID == "" && schedule.ClaimedAt == nil && schedule.CompletedAt == nil && historyID(holder) && epoch > 0 && schedule.LeaseOwner == holder && schedule.LeaseEpoch == epoch && schedule.LeaseUntil != nil && now.Before(*schedule.LeaseUntil) && now.Before(schedule.ExpiresAt)
}

// ValidateCopilotRerunGate checks value integrity only. Neither unclaimed nor a
// matching historical receipt grants permission to execute or bypass live checks.
func ValidateCopilotRerunGate(gate *model.CopilotRerunAdmissionGate) error {
	if gate == nil || !rerunUniqueUUIDs(gate.GateID, gate.ScheduleID, gate.RunID, gate.JobRunID) || !historyID(gate.ProjectID) || !historyID(gate.PipelineID) || gate.Version == 0 {
		return ErrCopilotRerunInvalid
	}
	sealed := gate.SealID != "" || gate.SealDigest != "" || gate.AgentID != ""
	if sealed && (!rerunUniqueUUIDs(gate.SealID, gate.GateID, gate.ScheduleID, gate.RunID, gate.JobRunID) || !sourceprovenance.ValidBindingDigest(gate.SealDigest) || !historyID(gate.AgentID)) {
		return ErrCopilotRerunInvalid
	}
	switch gate.State {
	case "unclaimed":
		if gate.AdmissionNonceDigest != "" || gate.ClaimID != "" || gate.ClaimedAt != nil || gate.ClosedAt != nil {
			return ErrCopilotRerunInvalid
		}
	case "claimed":
		if !sealed || !sourceprovenance.ValidBindingDigest(gate.AdmissionNonceDigest) || !rerunUUID(gate.ClaimID) || gate.ClaimedAt == nil || gate.ClosedAt != nil {
			return ErrCopilotRerunInvalid
		}
	case "closed":
		if gate.AdmissionNonceDigest != "" || gate.ClaimID != "" || gate.ClaimedAt != nil || gate.ClosedAt == nil {
			return ErrCopilotRerunInvalid
		}
	default:
		return ErrCopilotRerunInvalid
	}
	return nil
}
func CopilotRerunGateReceiptMatches(gate *model.CopilotRerunAdmissionGate, runID, jobID, agentID, sealID, sealDigest, claimID string) bool {
	return ValidateCopilotRerunGate(gate) == nil && gate.State == "claimed" && gate.RunID == runID && gate.JobRunID == jobID && gate.AgentID == agentID && gate.SealID == sealID && gate.SealDigest == sealDigest && gate.ClaimID == claimID
}
