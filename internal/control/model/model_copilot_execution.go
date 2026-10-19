// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package model

import "time"

// CopilotExecutionIntent is a detached, immutable retry-approval submission
// binding. History deletion never deletes this row or its idempotency keys.
// Submitted means a vote was recorded, not that execution was authorized.
type CopilotExecutionIntent struct {
	ID             uint64     `gorm:"primaryKey;autoIncrement;index:idx_copilot_execution_owner,priority:3;index:idx_copilot_execution_expiry,priority:2" json:"-"`
	IntentID       string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_execution_intent" json:"-"`
	OwnerUserID    string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_execution_request,priority:1;index:idx_copilot_execution_owner,priority:1" json:"-"`
	ProjectID      string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_execution_request,priority:2;index:idx_copilot_execution_owner,priority:2" json:"-"`
	RequestID      string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_execution_request,priority:3" json:"-"`
	PipelineID     string     `gorm:"type:varchar(64);not null" json:"-"`
	RunID          string     `gorm:"type:varchar(64);not null" json:"-"`
	JobRunID       string     `gorm:"type:varchar(64);not null" json:"-"`
	ApprovalID     string     `gorm:"type:varchar(64);not null" json:"-"`
	ConversationID string     `gorm:"type:varchar(64);not null" json:"-"`
	PlanID         string     `gorm:"type:varchar(64);not null" json:"-"`
	PlanVersion    uint64     `gorm:"not null" json:"-"`
	PlanDigest     string     `gorm:"type:varchar(128);not null" json:"-"`
	EvidenceHash   string     `gorm:"type:varchar(128);not null" json:"-"`
	EvidenceDigest string     `gorm:"type:varchar(128);not null" json:"-"`
	DecisionID     string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_copilot_execution_decision" json:"-"`
	IntentDigest   string     `gorm:"type:varchar(128);not null" json:"-"`
	State          string     `gorm:"type:varchar(16);not null" json:"-"`
	Version        uint64     `gorm:"not null" json:"-"`
	CreatedAt      time.Time  `gorm:"not null" json:"-"`
	UpdatedAt      time.Time  `gorm:"not null" json:"-"`
	ExpiresAt      time.Time  `gorm:"not null;index:idx_copilot_execution_expiry,priority:1" json:"-"`
	SubmittedAt    *time.Time `json:"-"`
	CancelledAt    *time.Time `json:"-"`
}

func (CopilotExecutionIntent) TableName() string { return "copilot_execution_intent" }
