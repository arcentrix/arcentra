// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package model

import "time"

// CopilotConversation contains identifiers only. All user/model authored content
// belongs in a separately authenticated encrypted envelope, never in these rows.
type CopilotConversation struct {
	ID                uint64     `gorm:"primaryKey;autoIncrement;index:idx_copilot_conversation_owner,priority:3;index:idx_copilot_conversation_expiry,priority:2;index:idx_copilot_conversation_tombstone,priority:2" json:"-"`
	ConversationID    string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_conversation" json:"-"`
	OwnerUserID       string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_conversation_request,priority:1;index:idx_copilot_conversation_owner,priority:1" json:"-"`
	ProjectID         string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_conversation_request,priority:2;index:idx_copilot_conversation_owner,priority:2" json:"-"`
	PipelineID        string     `gorm:"type:varchar(64);not null" json:"-"`
	RunID             string     `gorm:"type:varchar(64);not null" json:"-"`
	CreateRequestID   string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_conversation_request,priority:3" json:"-"`
	CreateFingerprint string     `gorm:"type:varchar(128);not null" json:"-"`
	State             string     `gorm:"type:varchar(16);not null" json:"-"`
	Version           uint64     `gorm:"not null" json:"-"`
	NextSequence      uint64     `gorm:"not null" json:"-"`
	CreatedAt         time.Time  `gorm:"not null" json:"-"`
	UpdatedAt         time.Time  `gorm:"not null" json:"-"`
	ExpiresAt         time.Time  `gorm:"not null;index:idx_copilot_conversation_expiry,priority:1" json:"-"`
	TombstoneUntil    time.Time  `gorm:"not null;index:idx_copilot_conversation_tombstone,priority:1" json:"-"`
	DeletedAt         *time.Time `json:"-"`
}

func (CopilotConversation) TableName() string { return "copilot_conversation" }

type CopilotTurn struct {
	ID                     uint64     `gorm:"primaryKey;autoIncrement;index:idx_copilot_turn_lease,priority:3;index:idx_copilot_turn_purge,priority:3;index:idx_copilot_turn_tombstone,priority:2" json:"-"`
	TurnID                 string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_turn" json:"-"`
	ConversationID         string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_turn_sequence,priority:1" json:"-"`
	Sequence               uint64     `gorm:"not null;uniqueIndex:uk_copilot_turn_sequence,priority:2" json:"-"`
	OwnerUserID            string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_turn_request,priority:1;index:idx_copilot_turn_active,priority:1" json:"-"`
	ProjectID              string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_turn_request,priority:2" json:"-"`
	RequestID              string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_turn_request,priority:3" json:"-"`
	State                  string     `gorm:"type:varchar(24);not null;index:idx_copilot_turn_lease,priority:1;index:idx_copilot_turn_active,priority:2" json:"-"`
	CancelRequested        bool       `gorm:"not null;default:false" json:"-"`
	Version                uint64     `gorm:"not null" json:"-"`
	ProviderID             string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	Model                  string     `gorm:"type:varchar(160);not null;default:''" json:"-"`
	ProviderBinding        string     `gorm:"type:varchar(128);not null;default:''" json:"-"`
	InputFingerprint       string     `gorm:"type:varchar(128);not null;default:''" json:"-"`
	ContextDigest          string     `gorm:"type:varchar(128);not null;default:''" json:"-"`
	RedactionPolicyVersion string     `gorm:"type:varchar(32);not null;default:''" json:"-"`
	PayloadCiphertext      []byte     `gorm:"type:mediumblob" json:"-"`
	CipherVersion          uint32     `gorm:"not null;default:0" json:"-"`
	PayloadPresent         bool       `gorm:"not null;default:false;index:idx_copilot_turn_purge,priority:1" json:"-"`
	CreatedAt              time.Time  `gorm:"not null" json:"-"`
	UpdatedAt              time.Time  `gorm:"not null" json:"-"`
	ExpiresAt              time.Time  `gorm:"not null;index:idx_copilot_turn_purge,priority:2" json:"-"`
	TombstoneUntil         time.Time  `gorm:"not null;index:idx_copilot_turn_tombstone,priority:1" json:"-"`
	LeaseUntil             time.Time  `gorm:"not null;index:idx_copilot_turn_lease,priority:2" json:"-"`
	DispatchedAt           *time.Time `json:"-"`
	FinishedAt             *time.Time `json:"-"`
	ErrorCode              string     `gorm:"type:varchar(32);not null;default:''" json:"-"`
}

func (CopilotTurn) TableName() string { return "copilot_turn" }

type CopilotActionPlan struct {
	ID                uint64     `gorm:"primaryKey;autoIncrement;index:idx_copilot_plan_purge,priority:3;index:idx_copilot_plan_conversation,priority:2;index:idx_copilot_plan_expiry,priority:3" json:"-"`
	PlanID            string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_plan" json:"-"`
	RequestID         string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_plan_request,priority:3" json:"-"`
	ConversationID    string     `gorm:"type:varchar(64);not null;index:idx_copilot_plan_conversation,priority:1" json:"-"`
	TurnID            string     `gorm:"type:varchar(64);not null" json:"-"`
	OwnerUserID       string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_plan_request,priority:1" json:"-"`
	ProjectID         string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_plan_request,priority:2" json:"-"`
	PipelineID        string     `gorm:"type:varchar(64);not null" json:"-"`
	RunID             string     `gorm:"type:varchar(64);not null" json:"-"`
	Kind              string     `gorm:"type:varchar(32);not null" json:"-"`
	State             string     `gorm:"type:varchar(16);not null;index:idx_copilot_plan_expiry,priority:1" json:"-"`
	Version           uint64     `gorm:"not null" json:"-"`
	PlanDigest        string     `gorm:"type:varchar(128);not null" json:"-"`
	SchemaVersion     uint32     `gorm:"not null" json:"-"`
	PayloadCiphertext []byte     `gorm:"type:mediumblob" json:"-"`
	CipherVersion     uint32     `gorm:"not null" json:"-"`
	PayloadPresent    bool       `gorm:"not null;index:idx_copilot_plan_purge,priority:1" json:"-"`
	CreatedAt         time.Time  `gorm:"not null" json:"-"`
	ExpiresAt         time.Time  `gorm:"not null;index:idx_copilot_plan_purge,priority:2;index:idx_copilot_plan_expiry,priority:2" json:"-"`
	ReviewedAt        *time.Time `json:"-"`
	ReasonCode        string     `gorm:"type:varchar(32);not null;default:''" json:"-"`
}

func (CopilotActionPlan) TableName() string { return "copilot_action_plan" }
