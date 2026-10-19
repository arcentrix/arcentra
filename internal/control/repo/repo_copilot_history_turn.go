// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func historyRandomID() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", ErrCopilotHistoryUnavailable
	}
	return hex.EncodeToString(b[:]), nil
}
func historyFindTurn(tx *gorm.DB, c *model.CopilotConversation, id string, byRequest bool) (*model.CopilotTurn, error) {
	if !historyID(id) {
		return nil, ErrCopilotHistoryInvalid
	}
	var rows []model.CopilotTurn
	field := "turn_id"
	if byRequest {
		field = "request_id"
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("owner_user_id = ? AND project_id = ? AND "+field+" = ?", c.OwnerUserID, c.ProjectID, id).Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrCopilotHistoryNotFound
	}
	v := &rows[0]
	match := v.TurnID
	if byRequest {
		match = v.RequestID
	}
	if len(rows) != 1 || v.OwnerUserID != c.OwnerUserID || v.ProjectID != c.ProjectID || v.ConversationID != c.ConversationID || match != id {
		return nil, ErrCopilotHistoryConflict
	}
	historyTrackExpiry(tx, v.ExpiresAt)
	return v, nil
}
func historyLease(until time.Time) (time.Time, error) {
	now := historyNow()
	if until.IsZero() {
		return now.Add(time.Minute), nil
	}
	if !until.After(now) || until.After(now.Add(2*time.Minute)) {
		return time.Time{}, ErrCopilotHistoryInvalid
	}
	return until.UTC(), nil
}
func historyAllocate(tx *gorm.DB, c *model.CopilotConversation, expires, tombstone time.Time) error {
	if c.NextSequence > CopilotHistoryMaxTurns {
		return ErrCopilotHistoryCapacity
	}
	result := tx.Model(&model.CopilotConversation{}).Where("id = ? AND version = ? AND next_sequence = ? AND state = ?", c.ID, c.Version, c.NextSequence, "active").Updates(map[string]any{"next_sequence": c.NextSequence + 1, "updated_at": historyNow(), "expires_at": historyMaxTime(c.ExpiresAt, expires), "tombstone_until": historyMaxTime(c.TombstoneUntil, tombstone)})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCopilotHistoryConflict
	}
	return nil
}
func (r *Repositories) CopilotHistoryReserve(ctx context.Context, in CopilotHistoryReserveInput, authorize CopilotHistoryAuthorize) (*model.CopilotTurn, bool, error) {
	if !historyID(in.TurnID) || !historyID(in.RequestID) || !historyID(in.ProviderID) || len(in.Model) == 0 || len(in.Model) > 160 || !historyBinding(in.ProviderBinding) || !historyBinding(in.InputFingerprint) || !historyBinding(in.ContextDigest) || len(in.RedactionPolicyVersion) == 0 || len(in.RedactionPolicyVersion) > 32 || !historyPayload(in.PayloadCiphertext, in.CipherVersion, false) || in.ConversationVersion == 0 {
		return nil, false, ErrCopilotHistoryInvalid
	}
	lease, err := historyLease(in.LeaseUntil)
	if err != nil {
		return nil, false, err
	}
	var out *model.CopilotTurn
	created := false
	err = r.historyConversationTx(ctx, in.OwnerUserID, in.ProjectID, in.ConversationID, authorize, func(tx *gorm.DB, c *model.CopilotConversation) error {
		existing, e := historyFindTurn(tx, c, in.RequestID, true)
		if e == nil {
			if !historyNow().Before(existing.ExpiresAt) {
				return ErrCopilotHistoryNotFound
			}
			if existing.State == "cancelled" && existing.InputFingerprint == "" {
				out = existing
				return nil
			}
			if existing.ProviderID != in.ProviderID || existing.Model != in.Model || existing.ProviderBinding != in.ProviderBinding || existing.InputFingerprint != in.InputFingerprint || existing.ContextDigest != in.ContextDigest || existing.RedactionPolicyVersion != in.RedactionPolicyVersion {
				return ErrCopilotHistoryConflict
			}
			out = existing
			return nil
		}
		if e != ErrCopilotHistoryNotFound {
			return e
		}
		if c.Version != in.ConversationVersion {
			return ErrCopilotHistoryConflict
		}
		// The exact actor row is held even when no active row exists; row count by
		// itself would not serialize independent instances or distinct projects.
		var actives []model.CopilotTurn
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "owner_user_id").Where("owner_user_id = ? AND state IN ?", c.OwnerUserID, []string{"reserved", "dispatching"}).Limit(3).Find(&actives).Error; e != nil {
			return e
		}
		for _, a := range actives {
			if a.OwnerUserID != c.OwnerUserID {
				return ErrCopilotHistoryConflict
			}
		}
		if len(actives) >= 2 {
			return ErrCopilotHistoryCapacity
		}
		var count int64
		if e = tx.Model(&model.CopilotTurn{}).Where("owner_user_id = ?", c.OwnerUserID).Count(&count).Error; e != nil {
			return e
		}
		if count >= 20000 {
			return ErrCopilotHistoryCapacity
		}
		now := historyNow()
		v := &model.CopilotTurn{TurnID: in.TurnID, ConversationID: c.ConversationID, Sequence: c.NextSequence, OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, RequestID: in.RequestID, State: "reserved", Version: 1, ProviderID: in.ProviderID, Model: in.Model, ProviderBinding: in.ProviderBinding, InputFingerprint: in.InputFingerprint, ContextDigest: in.ContextDigest, RedactionPolicyVersion: in.RedactionPolicyVersion, PayloadCiphertext: append([]byte(nil), in.PayloadCiphertext...), CipherVersion: in.CipherVersion, PayloadPresent: len(in.PayloadCiphertext) > 0, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(CopilotHistoryBodyTTL), TombstoneUntil: now.Add(CopilotHistoryTombstoneTTL), LeaseUntil: lease}
		if e = historyAllocate(tx, c, v.ExpiresAt, v.TombstoneUntil); e != nil {
			return e
		}
		if e = tx.Create(v).Error; e != nil {
			return ErrCopilotHistoryConflict
		}
		out = v
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}
func (r *Repositories) CopilotHistoryDispatch(ctx context.Context, in CopilotHistoryDispatchInput, authorize CopilotHistoryAuthorize) (*model.CopilotTurn, error) {
	if in.Version == 0 || !historyPayload(in.PayloadCiphertext, in.CipherVersion, true) {
		return nil, ErrCopilotHistoryInvalid
	}
	lease, err := historyLease(in.LeaseUntil)
	if err != nil {
		return nil, err
	}
	var out *model.CopilotTurn
	err = r.historyConversationTx(ctx, in.OwnerUserID, in.ProjectID, in.ConversationID, authorize, func(tx *gorm.DB, c *model.CopilotConversation) error {
		v, e := historyFindTurn(tx, c, in.TurnID, false)
		if e != nil {
			return e
		}
		now := historyNow()
		if v.Version != in.Version || v.State != "reserved" || v.CancelRequested || !now.Before(v.ExpiresAt) || !now.Before(v.LeaseUntil) {
			return ErrCopilotHistoryConflict
		}
		updates := map[string]any{"state": "dispatching", "version": v.Version + 1, "dispatched_at": now, "updated_at": now, "lease_until": lease, "payload_ciphertext": in.PayloadCiphertext, "cipher_version": in.CipherVersion, "payload_present": true}
		res := tx.Model(&model.CopilotTurn{}).Where("id = ? AND version = ? AND state = ? AND cancel_requested = ?", v.ID, v.Version, "reserved", false).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrCopilotHistoryConflict
		}
		v.State = "dispatching"
		v.Version++
		v.DispatchedAt = &now
		v.UpdatedAt = now
		v.LeaseUntil = lease
		v.PayloadCiphertext = append([]byte(nil), in.PayloadCiphertext...)
		v.CipherVersion = in.CipherVersion
		v.PayloadPresent = true
		out = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
func historyTerminalState(s string) bool {
	return s == "completed" || s == "failed" || s == "cancelled" || s == "withheld" || s == "interrupted" || s == "outcome_unknown"
}
func historyErrorCode(s string) bool {
	switch s {
	case "", "provider_failed", "provider_unavailable", "cancelled", "interrupted", "outcome_unknown", "withheld", "content_unavailable", "history_unavailable", "deadline_exceeded", "deleted", "expired", "preparation_failed":
		return true
	}
	return false
}
func (r *Repositories) CopilotHistoryFinalize(ctx context.Context, in CopilotHistoryFinalizeInput, authorize CopilotHistoryAuthorize) (*model.CopilotTurn, error) {
	if in.Version == 0 || !historyTerminalState(in.State) || !historyErrorCode(in.ErrorCode) || !historyPayload(in.PayloadCiphertext, in.CipherVersion, in.State == "completed") || in.State == "withheld" && len(in.PayloadCiphertext) > 0 {
		return nil, ErrCopilotHistoryInvalid
	}
	var out *model.CopilotTurn
	err := r.historyConversationTx(ctx, in.OwnerUserID, in.ProjectID, in.ConversationID, authorize, func(tx *gorm.DB, c *model.CopilotConversation) error {
		v, e := historyFindTurn(tx, c, in.TurnID, false)
		if e != nil {
			return e
		}
		now := historyNow()
		if v.Version != in.Version || !historyActive(v.State) || v.CancelRequested || !now.Before(v.ExpiresAt) || !now.Before(v.LeaseUntil) || in.State == "completed" && v.State != "dispatching" || in.State == "interrupted" && v.State != "reserved" || in.State == "outcome_unknown" && v.State != "dispatching" {
			return ErrCopilotHistoryConflict
		}
		payload, version := in.PayloadCiphertext, in.CipherVersion
		if len(payload) == 0 && in.State != "withheld" {
			payload = v.PayloadCiphertext
			version = v.CipherVersion
		}
		res := tx.Model(&model.CopilotTurn{}).Where("id = ? AND version = ? AND state = ? AND cancel_requested = ?", v.ID, v.Version, v.State, false).Updates(map[string]any{"state": in.State, "version": v.Version + 1, "finished_at": now, "updated_at": now, "payload_ciphertext": payload, "cipher_version": version, "payload_present": len(payload) > 0, "error_code": in.ErrorCode})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrCopilotHistoryConflict
		}
		v.State = in.State
		v.Version++
		v.FinishedAt = &now
		v.UpdatedAt = now
		v.ErrorCode = in.ErrorCode
		v.PayloadCiphertext = append([]byte(nil), payload...)
		v.CipherVersion = version
		v.PayloadPresent = len(payload) > 0
		out = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
func (r *Repositories) CopilotHistoryCancel(ctx context.Context, owner, project, conversation, request string, authorize CopilotHistoryAuthorize) (*model.CopilotTurn, error) {
	if !historyID(request) {
		return nil, ErrCopilotHistoryInvalid
	}
	var out *model.CopilotTurn
	err := r.historyConversationTx(ctx, owner, project, conversation, authorize, func(tx *gorm.DB, c *model.CopilotConversation) error {
		v, e := historyFindTurn(tx, c, request, true)
		now := historyNow()
		if e == ErrCopilotHistoryNotFound {
			id, e := historyRandomID()
			if e != nil {
				return e
			}
			v = &model.CopilotTurn{TurnID: id, ConversationID: c.ConversationID, Sequence: c.NextSequence, OwnerUserID: owner, ProjectID: project, RequestID: request, State: "cancelled", CancelRequested: true, Version: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: c.ExpiresAt, TombstoneUntil: now.Add(CopilotHistoryTombstoneTTL), LeaseUntil: now, FinishedAt: &now, ErrorCode: "cancelled"}
			var count int64
			if e = tx.Model(&model.CopilotTurn{}).Where("owner_user_id = ?", owner).Count(&count).Error; e != nil {
				return e
			}
			if count >= 20000 {
				return ErrCopilotHistoryCapacity
			}
			if e = historyAllocate(tx, c, c.ExpiresAt, v.TombstoneUntil); e != nil {
				return e
			}
			if e = tx.Create(v).Error; e != nil {
				return ErrCopilotHistoryConflict
			}
			out = v
			return nil
		}
		if e != nil {
			return e
		}
		if historyActive(v.State) {
			res := tx.Model(&model.CopilotTurn{}).Where("id = ? AND version = ?", v.ID, v.Version).Updates(map[string]any{"state": "cancelled", "cancel_requested": true, "version": v.Version + 1, "updated_at": now, "finished_at": now, "error_code": "cancelled"})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return ErrCopilotHistoryConflict
			}
			v.State = "cancelled"
			v.Version++
			v.CancelRequested = true
			v.UpdatedAt = now
			v.FinishedAt = &now
			v.ErrorCode = "cancelled"
		}
		out = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
func (r *Repositories) CopilotHistoryGetTurn(ctx context.Context, owner, project, conversation, id string, authorize CopilotHistoryAuthorize) (*model.CopilotTurn, error) {
	return r.historyGetTurn(ctx, owner, project, conversation, id, false, authorize)
}
func (r *Repositories) CopilotHistoryGetRequest(ctx context.Context, owner, project, conversation, id string, authorize CopilotHistoryAuthorize) (*model.CopilotTurn, error) {
	return r.historyGetTurn(ctx, owner, project, conversation, id, true, authorize)
}
func (r *Repositories) historyGetTurn(ctx context.Context, owner, project, conversation, id string, byRequest bool, authorize CopilotHistoryAuthorize) (*model.CopilotTurn, error) {
	var out *model.CopilotTurn
	err := r.historyConversationTx(ctx, owner, project, conversation, authorize, func(tx *gorm.DB, c *model.CopilotConversation) error {
		v, e := historyFindTurn(tx, c, id, byRequest)
		if e != nil {
			return e
		}
		if !historyNow().Before(v.ExpiresAt) {
			return ErrCopilotHistoryNotFound
		}
		if e = historyReconcileTurn(tx, v); e != nil {
			return e
		}
		out = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
func (r *Repositories) CopilotHistoryTurns(ctx context.Context, owner, project, conversation string, after uint64, limit int, authorize CopilotHistoryAuthorize) (*CopilotHistoryTurnPage, error) {
	if limit < 1 || limit > 20 {
		return nil, ErrCopilotHistoryInvalid
	}
	page := &CopilotHistoryTurnPage{List: []*model.CopilotTurn{}}
	err := r.historyConversationTx(ctx, owner, project, conversation, authorize, func(tx *gorm.DB, c *model.CopilotConversation) error {
		var rows []model.CopilotTurn
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("conversation_id = ? AND sequence > ? AND expires_at > ?", c.ConversationID, after, historyNow()).Order("sequence ASC").Limit(limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		size := 0
		for i := range rows {
			v := &rows[i]
			if v.ConversationID != c.ConversationID || v.OwnerUserID != owner || v.ProjectID != project {
				return ErrCopilotHistoryNotFound
			}
			if len(page.List) >= limit || size+len(v.PayloadCiphertext) > CopilotHistoryMaxPageBytes {
				if len(page.List) > 0 {
					page.NextAfter = page.List[len(page.List)-1].Sequence
				}
				break
			}
			if e := historyReconcileTurn(tx, v); e != nil {
				return e
			}
			historyTrackExpiry(tx, v.ExpiresAt)
			size += len(v.PayloadCiphertext)
			page.List = append(page.List, v)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}
func (r *Repositories) CopilotHistoryDelete(ctx context.Context, owner, project, conversation string, expectedVersion uint64, authorize CopilotHistoryAuthorize) error {
	if expectedVersion == 0 {
		return ErrCopilotHistoryInvalid
	}
	return r.historyConversationTx(ctx, owner, project, conversation, authorize, func(tx *gorm.DB, c *model.CopilotConversation) error {
		if c.Version != expectedVersion {
			return ErrCopilotHistoryConflict
		}
		now := historyNow()
		result := tx.Model(&model.CopilotConversation{}).Where("id = ? AND version = ? AND state = ?", c.ID, c.Version, "active").Updates(map[string]any{"state": "deleted", "version": c.Version + 1, "deleted_at": now, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrCopilotHistoryConflict
		}
		// Conversation bounds make immediate deletion bounded (200 turns/100 plans).
		if err := tx.Model(&model.CopilotTurn{}).Where("conversation_id = ?", c.ConversationID).Updates(map[string]any{"payload_ciphertext": nil, "cipher_version": 0, "payload_present": false, "cancel_requested": true, "version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.CopilotTurn{}).Where("conversation_id = ? AND state IN ?", c.ConversationID, []string{"reserved", "dispatching"}).Updates(map[string]any{"state": "cancelled", "finished_at": now, "error_code": "deleted"}).Error; err != nil {
			return err
		}
		return tx.Model(&model.CopilotActionPlan{}).Where("conversation_id = ?", c.ConversationID).Updates(map[string]any{"payload_ciphertext": nil, "cipher_version": 0, "payload_present": false, "state": "cancelled", "version": gorm.Expr("version + 1"), "reason_code": "deleted"}).Error
	})
}
