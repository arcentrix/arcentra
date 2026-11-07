// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package model

import "time"

// These schemas support prospective capture and inert durable confirmation.
// They do not enable Agent admission or dispatch; those require separately
// reviewed runtime integration and live authorization.

// CopilotDefinitionSnapshot is prospectively inserted with the verified manual
// run, before its job identities or checkout result exist. Never backfill this
// row from a terminal run or mutate it with a later checkout SHA. Its encrypted
// payload contains resolved definition and references, not delivered secrets.
type CopilotDefinitionSnapshot struct {
	ID                         uint64    `gorm:"primaryKey;autoIncrement" json:"-"`
	SnapshotID                 string    `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_definition_snapshot" json:"-"`
	RunID                      string    `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_definition_run" json:"-"`
	PipelineID                 string    `gorm:"type:varchar(64);not null" json:"-"`
	ProjectID                  string    `gorm:"type:varchar(64);not null" json:"-"`
	JobName                    string    `gorm:"type:varchar(128);not null" json:"-"`
	RepositoryConfigDigest     string    `gorm:"type:varchar(67);not null" json:"-"`
	TriggerVariablesDigest     string    `gorm:"type:varchar(67);not null" json:"-"`
	RetryApprovalContext       string    `gorm:"type:mediumtext;not null" json:"-"`
	RetryApprovalContextDigest string    `gorm:"type:varchar(67);not null" json:"-"`
	PayloadSchemaVersion       uint32    `gorm:"not null" json:"-"`
	DefinitionRepository       string    `gorm:"type:varchar(2048);not null" json:"-"`
	DefinitionPath             string    `gorm:"type:varchar(1024);not null" json:"-"`
	DefinitionRevision         string    `gorm:"type:varchar(64);not null" json:"-"`
	ResolvedSpecDigest         string    `gorm:"type:varchar(67);not null" json:"-"`
	RuntimePolicyDigest        string    `gorm:"type:varchar(67);not null" json:"-"`
	SecretReferencesDigest     string    `gorm:"type:varchar(67);not null" json:"-"`
	LayoutJSON                 string    `gorm:"type:mediumtext;not null" json:"-"`
	ContentBinding             string    `gorm:"type:varchar(67);not null" json:"-"`
	BindingDigest              string    `gorm:"type:varchar(67);not null" json:"-"`
	CipherVersion              uint32    `gorm:"not null" json:"-"`
	PayloadCiphertext          []byte    `gorm:"type:mediumblob;not null" json:"-"`
	CiphertextDigest           string    `gorm:"type:varchar(64);not null" json:"-"`
	CreatedAt                  time.Time `gorm:"not null" json:"-"`
	ExpiresAt                  time.Time `gorm:"not null;index:idx_copilot_definition_expiry" json:"-"`
}

func (CopilotDefinitionSnapshot) TableName() string { return "copilot_definition_snapshot" }

// CopilotRerunIntent combines immutable prospective definition evidence with
// later verified terminal execution evidence. All new IDs are independent of
// original run/job/attempt IDs. It contains no executable body or secret values.
type CopilotRerunIntent struct {
	ID                            uint64     `gorm:"primaryKey;autoIncrement;index:idx_copilot_rerun_owner,priority:3" json:"-"`
	IntentID                      string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_intent" json:"-"`
	OwnerUserID                   string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_request,priority:1;index:idx_copilot_rerun_owner,priority:1" json:"-"`
	ProjectID                     string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_request,priority:2;index:idx_copilot_rerun_owner,priority:2" json:"-"`
	RequestID                     string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_request,priority:3" json:"-"`
	PipelineID                    string     `gorm:"type:varchar(64);not null" json:"-"`
	OriginalRunID                 string     `gorm:"type:varchar(64);not null" json:"-"`
	OriginalJobRunID              string     `gorm:"type:varchar(64);not null" json:"-"`
	SnapshotID                    string     `gorm:"type:varchar(64);not null" json:"-"`
	SnapshotDigest                string     `gorm:"type:varchar(67);not null" json:"-"`
	OriginalEvidenceJSON          string     `gorm:"type:mediumtext;not null" json:"-"`
	OriginalEvidenceDigest        string     `gorm:"type:varchar(67);not null" json:"-"`
	NewRunID                      string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_new_run" json:"-"`
	NewJobRunID                   string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_new_job" json:"-"`
	ScheduleID                    string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_schedule_link" json:"-"`
	GateID                        string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_gate_link" json:"-"`
	ReviewedRunContext            string     `gorm:"type:mediumtext;not null" json:"-"`
	ReviewedRunContextDigest      string     `gorm:"type:varchar(67);not null" json:"-"`
	ReviewedAdmissionPolicyDigest string     `gorm:"type:varchar(67);not null" json:"-"`
	ExpectedSourceCommit          string     `gorm:"type:varchar(64);not null" json:"-"`
	StepIdentitiesJSON            string     `gorm:"type:mediumtext;not null" json:"-"`
	IntentDigest                  string     `gorm:"type:varchar(67);not null" json:"-"`
	State                         string     `gorm:"type:varchar(24);not null" json:"-"`
	Version                       uint64     `gorm:"not null" json:"-"`
	CreatedAt                     time.Time  `gorm:"not null" json:"-"`
	ExpiresAt                     time.Time  `gorm:"not null" json:"-"`
	ConfirmedAt                   *time.Time `json:"-"`
	CancelledAt                   *time.Time `json:"-"`
}

func (CopilotRerunIntent) TableName() string { return "copilot_rerun_intent" }

// CopilotRerunSchedule separates a replaceable controller lease from a sealed
// immutable delivery identity. Lease expiry permits identical retransmission,
// never replacement of an assigned Agent or an authoritative execution claim.
type CopilotRerunSchedule struct {
	ID                   uint64     `gorm:"primaryKey;autoIncrement;index:idx_copilot_rerun_schedule_due,priority:3" json:"-"`
	ScheduleID           string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_schedule" json:"-"`
	IntentID             string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_schedule_intent" json:"-"`
	OwnerUserID          string     `gorm:"type:varchar(64);not null" json:"-"`
	ProjectID            string     `gorm:"type:varchar(64);not null" json:"-"`
	PipelineID           string     `gorm:"type:varchar(64);not null" json:"-"`
	RunID                string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_schedule_run" json:"-"`
	JobRunID             string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_schedule_job" json:"-"`
	State                string     `gorm:"type:varchar(24);not null;index:idx_copilot_rerun_schedule_due,priority:1" json:"-"`
	Version              uint64     `gorm:"not null" json:"-"`
	LeaseOwner           string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	LeaseEpoch           uint64     `gorm:"not null;default:0" json:"-"`
	LeaseUntil           *time.Time `gorm:"index:idx_copilot_rerun_schedule_due,priority:2" json:"-"`
	SealID               *string    `gorm:"type:varchar(64);uniqueIndex:uk_copilot_rerun_seal" json:"-"`
	SealDigest           string     `gorm:"type:varchar(67);not null;default:''" json:"-"`
	AgentID              string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	PayloadDigest        string     `gorm:"type:varchar(67);not null;default:''" json:"-"`
	CipherVersion        uint32     `gorm:"not null;default:0" json:"-"`
	PayloadCiphertext    []byte     `gorm:"type:mediumblob" json:"-"`
	CiphertextDigest     string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	SecretVersionsDigest string     `gorm:"type:varchar(67);not null;default:''" json:"-"`
	ClaimedID            string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	CancelRequested      bool       `gorm:"not null;default:false" json:"-"`
	CreatedAt            time.Time  `gorm:"not null" json:"-"`
	ExpiresAt            time.Time  `gorm:"not null" json:"-"`
	SealedAt             *time.Time `json:"-"`
	ClaimedAt            *time.Time `json:"-"`
	CompletedAt          *time.Time `json:"-"`
}

func (CopilotRerunSchedule) TableName() string { return "copilot_rerun_schedule" }

// CopilotRerunAdmissionGate is separate from the schedule lease. Every first
// claim, preclaim seal/cancel/reject locks this row BEFORE any domain row. A
// claimed/closed gate forbids entering the first-claim domain-lock path again.
// No path may acquire this gate after holding a job/run/scope/schedule lock.
type CopilotRerunAdmissionGate struct {
	AdmissionNonceDigest string     `gorm:"type:varchar(67);not null;default:''" json:"-"`
	ID                   uint64     `gorm:"primaryKey;autoIncrement" json:"-"`
	GateID               string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_gate" json:"-"`
	ScheduleID           string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_gate_schedule" json:"-"`
	ProjectID            string     `gorm:"type:varchar(64);not null" json:"-"`
	PipelineID           string     `gorm:"type:varchar(64);not null" json:"-"`
	RunID                string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_gate_run" json:"-"`
	JobRunID             string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_rerun_gate_job" json:"-"`
	AgentID              string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	SealID               string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	SealDigest           string     `gorm:"type:varchar(67);not null;default:''" json:"-"`
	State                string     `gorm:"type:varchar(16);not null" json:"-"`
	ClaimID              string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	Version              uint64     `gorm:"not null" json:"-"`
	CreatedAt            time.Time  `gorm:"not null" json:"-"`
	ClaimedAt            *time.Time `json:"-"`
	ClosedAt             *time.Time `json:"-"`
}

func (CopilotRerunAdmissionGate) TableName() string { return "copilot_rerun_admission_gate" }
