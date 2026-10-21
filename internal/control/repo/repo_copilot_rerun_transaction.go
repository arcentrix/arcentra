// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"database/sql"

	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
)

type rerunAttachmentTransactionKey struct{}
type rerunAttachmentTransactionProof struct{ pool gorm.ConnPool }

// Only this controlled outer transaction creates the proof. MySQL's pinned
// driver applies the requested isolation to this transaction before BEGIN;
// errors propagate. No global or session-default isolation is changed. SQLite
// is retained for ordinary semantics tests, not a MySQL gap-lock proof.
func rerunAttachmentTransaction(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	if db == nil || db.Statement == nil {
		return ErrCopilotRerunInvalid
	}
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		// Existing-parent record locks work at any isolation. Unknown nested callers
		// may retain that compatibility, but absence is denied by the proof guard.
		return db.WithContext(ctx).Transaction(fn)
	}
	if db.Dialector.Name() != "mysql" && db.Dialector.Name() != "sqlite" {
		return ErrCopilotRerunUnsupported
	}
	return db.WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		tx.Statement.Settings.Store(rerunAttachmentTransactionKey{}, rerunAttachmentTransactionProof{pool: tx.Statement.ConnPool})
		return fn(tx)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
}
func requireRerunAttachmentTransaction(tx *gorm.DB) error {
	if tx == nil || tx.Statement == nil {
		return ErrCopilotRerunUnsupported
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrCopilotRerunUnsupported
	}
	value, ok := tx.Statement.Settings.Load(rerunAttachmentTransactionKey{})
	proof, valid := value.(rerunAttachmentTransactionProof)
	if !ok || !valid || proof.pool != tx.Statement.ConnPool {
		return ErrCopilotRerunUnsupported
	}
	return nil
}
