// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package model

import "time"

// InboxDelivery is deliberately typed: never copy source names, logs, reasons,
// environment, evidence or provider payloads into a personal event.
type InboxDelivery struct {
	ID          uint64     `gorm:"primaryKey;autoIncrement;index:idx_inbox_owner_read,priority:3;index:idx_inbox_owner_id,priority:2" json:"-"`
	RecipientID string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_inbox_event,priority:1;index:idx_inbox_owner_read,priority:1;index:idx_inbox_owner_id,priority:1" json:"-"`
	SourceKind  string     `gorm:"type:varchar(16);not null;uniqueIndex:uk_inbox_event,priority:2" json:"-"`
	SourceID    string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_inbox_event,priority:3" json:"-"`
	EventKind   string     `gorm:"type:varchar(16);not null;uniqueIndex:uk_inbox_event,priority:4" json:"-"`
	RunID       string     `gorm:"type:varchar(64);not null" json:"-"`
	PipelineID  string     `gorm:"type:varchar(64);not null" json:"-"`
	ProjectID   string     `gorm:"type:varchar(64);not null" json:"-"`
	Status      string     `gorm:"type:varchar(16);not null" json:"-"`
	OccurredAt  time.Time  `gorm:"not null" json:"-"`
	DeliveredAt time.Time  `gorm:"not null" json:"-"`
	ReadAt      *time.Time `gorm:"index:idx_inbox_owner_read,priority:2" json:"-"`
}

func (InboxDelivery) TableName() string { return "inbox_delivery" }

// InboxWork is independent of authoritative run/approval lifecycle. Revision is
// a fencing token. A claim commits before any domain or authorization lock.
type InboxWork struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement;index:idx_inbox_work_due,priority:4"`
	SourceKind      string    `gorm:"type:varchar(16);not null;uniqueIndex:uk_inbox_work,priority:1;index:idx_inbox_work_due,priority:1"`
	SourceID        string    `gorm:"type:varchar(64);not null;uniqueIndex:uk_inbox_work,priority:2"`
	State           int       `gorm:"not null;default:0;index:idx_inbox_work_due,priority:2"`
	NextCheckAt     time.Time `gorm:"not null;index:idx_inbox_work_due,priority:3"`
	Revision        uint64    `gorm:"not null;default:0"`
	RecipientOffset int       `gorm:"not null;default:0"`
	RequestedDone   bool      `gorm:"not null;default:false"`
	TerminalDone    bool      `gorm:"not null;default:false"`
	Attempts        int       `gorm:"not null;default:0"`
	ErrorCode       string    `gorm:"type:varchar(24);not null;default:''"`
}

func (InboxWork) TableName() string { return "inbox_work" }

// InboxCursor is a short-lived continuation, never an authentication token.
// Owner authentication and current visibility are checked on every use.
type InboxCursor struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement"`
	Token     string    `gorm:"type:varchar(64);not null;uniqueIndex"`
	OwnerID   string    `gorm:"type:varchar(64);not null;index:idx_inbox_cursor_owner,priority:1"`
	Filter    string    `gorm:"type:varchar(8);not null"`
	BeforeID  uint64    `gorm:"not null"`
	ExpiresAt time.Time `gorm:"not null;index;index:idx_inbox_cursor_owner,priority:2"`
}

func (InboxCursor) TableName() string { return "inbox_cursor" }

// InboxSweep stores bounded round progress; upper bounds freeze each round,
// then wrap so lower-ID commits and late changes cannot be skipped forever.
type InboxSweep struct {
	Kind     string `gorm:"type:varchar(16);primaryKey"`
	AfterID  uint64 `gorm:"not null;default:0"`
	UpperID  uint64 `gorm:"not null;default:0"`
	Revision uint64 `gorm:"not null;default:0"`
}

func (InboxSweep) TableName() string { return "inbox_sweep" }
