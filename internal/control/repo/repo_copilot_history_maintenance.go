// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Recovery records only what is known. It never acquires domain locks after a
// history claim and cannot return a reservation that permits another dispatch.
func historyReconcileTurn(tx *gorm.DB, v *model.CopilotTurn) error {
	now := historyNow()
	if !historyActive(v.State) || now.Before(v.LeaseUntil) {
		return nil
	}
	state, code := "interrupted", "interrupted"
	if v.State == "dispatching" {
		state = "outcome_unknown"
		code = "outcome_unknown"
	}
	if v.CancelRequested {
		state = "cancelled"
		code = "cancelled"
	}
	res := tx.Model(&model.CopilotTurn{}).Where("id = ? AND version = ? AND state = ? AND lease_until <= ?", v.ID, v.Version, v.State, now).Updates(map[string]any{"state": state, "version": v.Version + 1, "updated_at": now, "finished_at": now, "error_code": code})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrCopilotHistoryConflict
	}
	v.State = state
	v.Version++
	v.UpdatedAt = now
	v.FinishedAt = &now
	v.ErrorCode = code
	return nil
}
func historyMaintenanceConversation(tx *gorm.DB, id string) (*model.CopilotConversation, error) {
	var rows []model.CopilotConversation
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("conversation_id = ?", id).Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != 1 || rows[0].ConversationID != id {
		return nil, ErrCopilotHistoryNotFound
	}
	return &rows[0], nil
}
func (r *Repositories) CopilotHistoryReconcile(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 200 {
		return 0, ErrCopilotHistoryInvalid
	}
	db, err := r.historyDB(ctx)
	if err != nil {
		return 0, historyError(err)
	}
	processed := 0
	for _, state := range []string{"reserved", "dispatching"} {
		if processed >= limit {
			break
		}
		var rows []model.CopilotTurn
		if err = db.Select("id", "conversation_id").Where("state = ? AND lease_until <= ?", state, historyNow()).Order("lease_until ASC, id ASC").Limit(limit - processed).Find(&rows).Error; err != nil {
			return processed, historyError(err)
		}
		for _, hint := range rows {
			err = db.Transaction(func(tx *gorm.DB) error {
				c, e := historyMaintenanceConversation(tx, hint.ConversationID)
				if e != nil {
					return e
				}
				var v model.CopilotTurn
				if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", hint.ID).First(&v).Error; e != nil {
					return e
				}
				if v.ConversationID != c.ConversationID {
					return ErrCopilotHistoryConflict
				}
				return historyReconcileTurn(tx, &v)
			})
			if err != nil {
				return processed, historyError(err)
			}
			processed++
		}
	}
	// Plan expiry is an inert state change and never schedules execution.
	for _, state := range []string{"proposed", "reviewed"} {
		if processed >= limit {
			break
		}
		var plans []model.CopilotActionPlan
		if err = db.Select("id", "conversation_id").Where("state = ? AND expires_at <= ?", state, historyNow()).Order("expires_at ASC,id ASC").Limit(limit - processed).Find(&plans).Error; err != nil {
			return processed, historyError(err)
		}
		for _, p := range plans {
			err = db.Transaction(func(tx *gorm.DB) error {
				if _, e := historyMaintenanceConversation(tx, p.ConversationID); e != nil {
					return e
				}
				return tx.Model(&model.CopilotActionPlan{}).Where("id = ? AND state IN ? AND expires_at <= ?", p.ID, []string{"proposed", "reviewed"}, historyNow()).Updates(map[string]any{"state": "expired", "reason_code": "expired", "version": gorm.Expr("version + 1")}).Error
			})
			if err != nil {
				return processed, historyError(err)
			}
			processed++
		}
	}

	return processed, nil
}

// Purge uses content-present/expiry indexes, so already erased rows do not
// starve later ciphertext. Every selected row consumes this shared batch budget.
// Request metadata is retained through both its 90-day horizon and the entire
// parent lifetime. No provider request or historic consent is ever replayed.
func (r *Repositories) CopilotHistoryPurge(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 200 {
		return 0, ErrCopilotHistoryInvalid
	}
	db, err := r.historyDB(ctx)
	if err != nil {
		return 0, historyError(err)
	}
	processed := 0
	var turns []model.CopilotTurn
	if err = db.Select("id", "conversation_id").Where("payload_present = ? AND expires_at <= ?", true, historyNow()).Order("expires_at ASC,id ASC").Limit(limit).Find(&turns).Error; err != nil {
		return 0, historyError(err)
	}
	for _, v := range turns {
		err = db.Transaction(func(tx *gorm.DB) error {
			if _, e := historyMaintenanceConversation(tx, v.ConversationID); e != nil {
				return e
			}
			return tx.Model(&model.CopilotTurn{}).Where("id = ? AND payload_present = ? AND expires_at <= ?", v.ID, true, historyNow()).Updates(map[string]any{"payload_ciphertext": nil, "cipher_version": 0, "payload_present": false, "version": gorm.Expr("version + 1"), "updated_at": historyNow()}).Error
		})
		if err != nil {
			return processed, historyError(err)
		}
		processed++
	}
	if processed < limit {
		var plans []model.CopilotActionPlan
		if err = db.Select("id", "conversation_id").Where("payload_present = ? AND expires_at <= ?", true, historyNow()).Order("expires_at ASC,id ASC").Limit(limit - processed).Find(&plans).Error; err != nil {
			return processed, historyError(err)
		}
		for _, p := range plans {
			err = db.Transaction(func(tx *gorm.DB) error {
				if _, e := historyMaintenanceConversation(tx, p.ConversationID); e != nil {
					return e
				}
				return tx.Model(&model.CopilotActionPlan{}).Where("id = ? AND payload_present = ? AND expires_at <= ?", p.ID, true, historyNow()).Updates(map[string]any{"payload_ciphertext": nil, "cipher_version": 0, "payload_present": false, "version": gorm.Expr("version + 1")}).Error
			})
			if err != nil {
				return processed, historyError(err)
			}
			processed++
		}
	}
	// Parent deletion is bounded by the per-conversation 200-turn/100-plan cap.
	// A parent with any retained tombstone cannot disappear before those keys.
	if processed < limit {
		var cs []model.CopilotConversation
		if err = db.Select("id", "conversation_id").Where("tombstone_until <= ? AND expires_at <= ?", historyNow(), historyNow()).Order("tombstone_until ASC,id ASC").Limit(limit - processed).Find(&cs).Error; err != nil {
			return processed, historyError(err)
		}
		for _, hint := range cs {
			err = db.Transaction(func(tx *gorm.DB) error {
				c, e := historyMaintenanceConversation(tx, hint.ConversationID)
				if e != nil {
					return e
				}
				now := historyNow()
				if c.ExpiresAt.After(now) || c.TombstoneUntil.After(now) {
					return nil
				}
				var count int64
				if e = tx.Model(&model.CopilotTurn{}).Where("conversation_id = ? AND tombstone_until > ?", c.ConversationID, now).Count(&count).Error; e != nil {
					return e
				}
				if count != 0 {
					return nil
				}
				if e = tx.Where("conversation_id = ?", c.ConversationID).Delete(&model.CopilotActionPlan{}).Error; e != nil {
					return e
				}
				if e = tx.Where("conversation_id = ?", c.ConversationID).Delete(&model.CopilotTurn{}).Error; e != nil {
					return e
				}
				return tx.Where("id = ?", c.ID).Delete(&model.CopilotConversation{}).Error
			})
			if err != nil {
				return processed, historyError(err)
			}
			processed++
		}
	}
	return processed, nil
}
