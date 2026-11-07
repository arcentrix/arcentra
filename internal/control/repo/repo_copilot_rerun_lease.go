// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"math"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	CopilotRerunSchedulePending   = "pending"
	CopilotRerunScheduleSealed    = "sealed"
	CopilotRerunScheduleClaimed   = "claimed"
	CopilotRerunScheduleCompleted = "completed"
	CopilotRerunScheduleCancelled = "cancelled"
	CopilotRerunLeaseMaxDuration  = time.Minute
)

type CopilotRerunLeaseToken struct {
	ScheduleID string
	Holder     string
	Epoch      uint64
	Until      time.Time
}

// These controller-only methods lock Schedule and nothing else. Holding a lease
// is not authorization to publish or execute. Gate/domain checks remain mandatory
// for sealing/claim, and mutable lease epoch is not immutable seal identity.
func (repositories *Repositories) CopilotRerunAcquireLease(ctx context.Context, scheduleID, holder string, duration time.Duration) (*CopilotRerunLeaseToken, error) {
	return repositories.copilotRerunLease(ctx, CopilotRerunLeaseToken{ScheduleID: scheduleID, Holder: holder}, duration, false)
}
func (repositories *Repositories) CopilotRerunRenewLease(ctx context.Context, token CopilotRerunLeaseToken, duration time.Duration) (*CopilotRerunLeaseToken, error) {
	if token.Epoch == 0 {
		return nil, ErrCopilotRerunInvalid
	}
	return repositories.copilotRerunLease(ctx, token, duration, true)
}
func (repositories *Repositories) copilotRerunLease(ctx context.Context, token CopilotRerunLeaseToken, duration time.Duration, renew bool) (*CopilotRerunLeaseToken, error) {
	if repositories == nil || repositories.database == nil || !rerunUUID(token.ScheduleID) || !historyID(token.Holder) || duration <= 0 || duration > CopilotRerunLeaseMaxDuration {
		return nil, ErrCopilotRerunInvalid
	}
	db := repositories.database.Database()
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		return nil, ErrCopilotRerunInvalid
	}
	var result *CopilotRerunLeaseToken
	err := db.WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		var rows []model.CopilotRerunSchedule
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("schedule_id = ?", token.ScheduleID).Limit(2).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].ScheduleID != token.ScheduleID {
			return ErrCopilotRerunConflict
		}
		row := rows[0]
		if row.Version == 0 || row.Version >= math.MaxInt64 || row.CancelRequested || row.ClaimedID != "" || (row.State != CopilotRerunSchedulePending && row.State != CopilotRerunScheduleSealed) {
			return ErrCopilotRerunConflict
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		if !now.Before(row.ExpiresAt) {
			return ErrCopilotRerunExpired
		}
		epoch := row.LeaseEpoch
		if renew {
			if row.LeaseOwner != token.Holder || epoch != token.Epoch || row.LeaseUntil == nil || !now.Before(*row.LeaseUntil) {
				return ErrCopilotRerunConflict
			}
		} else {
			if row.LeaseUntil != nil && now.Before(*row.LeaseUntil) {
				return ErrCopilotRerunConflict
			}
			if epoch >= math.MaxInt64 {
				return ErrCopilotRerunConflict
			}
			epoch++
		}
		until := now.Add(duration)
		if until.After(row.ExpiresAt) {
			until = row.ExpiresAt
		}
		update := tx.Model(&model.CopilotRerunSchedule{}).Where("id = ? AND schedule_id = ? AND version = ? AND lease_epoch = ? AND state = ? AND cancel_requested = ? AND claimed_id = ?", row.ID, row.ScheduleID, row.Version, row.LeaseEpoch, row.State, false, "").Updates(map[string]any{"lease_owner": token.Holder, "lease_epoch": epoch, "lease_until": until, "version": row.Version + 1})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrCopilotRerunConflict
		}
		// Never return a lease whose cutoff elapsed while the update was blocked.
		if !time.Now().Before(until) {
			return ErrCopilotRerunExpired
		}
		result = &CopilotRerunLeaseToken{ScheduleID: row.ScheduleID, Holder: token.Holder, Epoch: epoch, Until: until}
		return nil
	})
	if err != nil {
		return nil, rerunError(err)
	}
	return result, nil
}
