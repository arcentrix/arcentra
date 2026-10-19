// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Fixed health enums only. Callers must not log returned SQL/source errors.
type InboxTickResult struct {
	Sources    int
	Recipients int
	Deferred   int
	Invalid    int
}

var errInboxFence = errors.New("inbox work lease superseded")

func (r *Repositories) claimInbox(ctx context.Context, kind string) (*model.InboxWork, error) {
	db, err := r.inboxDB(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var selected *model.InboxWork
	err = db.Transaction(func(tx *gorm.DB) error {
		var rows []model.InboxWork
		if err := tx.Where("source_kind = ? AND state = 0 AND next_check_at <= ?", kind, now).Order("next_check_at ASC, id ASC").Limit(1).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		w := rows[0]
		result := tx.Model(&model.InboxWork{}).Where("id = ? AND revision = ? AND state = 0 AND next_check_at <= ?", w.ID, w.Revision, now).Updates(map[string]any{"revision": w.Revision + 1, "next_check_at": now.Add(30 * time.Second)})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		w.Revision++
		w.NextCheckAt = now.Add(30 * time.Second)
		selected = &w
		return nil
	})
	return selected, err
}
func inboxFence(tx *gorm.DB, w *model.InboxWork, patch map[string]any) error {
	patch["revision"] = w.Revision + 1
	result := tx.Model(&model.InboxWork{}).Where("id = ? AND revision = ? AND state = 0", w.ID, w.Revision).Updates(patch)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errInboxFence
	}
	return nil
}
func (r *Repositories) finishInbox(ctx context.Context, w *model.InboxWork, requested, terminal bool, code string) error {
	db, err := r.inboxDB(ctx)
	if err != nil {
		return err
	}
	done := terminal && (w.SourceKind == "run" || requested)
	state := 0
	if done {
		state = 1
	}
	delay := 10 * time.Second
	attempts := 0
	if code != "" {
		attempts = w.Attempts + 1
		if attempts > 8 {
			attempts = 8
		}
		delay = time.Duration(1<<attempts) * 5 * time.Second
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
	}
	if requested && !terminal && code == "" {
		delay = 30 * time.Second
	}
	return db.Transaction(func(tx *gorm.DB) error {
		return inboxFence(tx, w, map[string]any{"state": state, "next_check_at": time.Now().UTC().Add(delay), "requested_done": requested, "terminal_done": terminal, "attempts": attempts, "error_code": code})
	})
}

func (r *Repositories) deliverInbox(ctx context.Context, w *model.InboxWork, recipient, event string, authorize InboxAuthorize, completeRequest ...bool) error {
	db, err := r.inboxDB(ctx)
	if err != nil {
		return err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		var run *model.PipelineRun
		var a *model.ApprovalRequest
		var e error
		status := ""
		occurred := time.Time{}
		if w.SourceKind == "run" {
			run, e = inboxRun(tx, w.SourceID)
			if e != nil {
				return e
			}
			status = inboxRunStatus(run.Status)
			if recipient != run.InboxActorID || !admittedInboxRun(run) || status == "" || run.EndTime == nil || run.EndTime.Before(*run.StartTime) {
				return ErrInboxSource
			}
			occurred = *run.EndTime
		} else {
			var candidates []string
			a, run, candidates, e = inboxApproval(ctx, tx, w.SourceID)
			if e != nil {
				return e
			}
			status = inboxApprovalStatus(a.Status)
			if event == "requested" {
				found := false
				for _, v := range candidates {
					if v == recipient {
						found = true
					}
				}
				if !found {
					return ErrInboxSource
				}
				occurred = a.CreatedAt
				status = "pending"
			} else {
				if !model.IsApprovalTerminal(a.Status) || recipient != run.InboxActorID {
					return ErrInboxSource
				}
				occurred = a.UpdatedAt
			}
		}
		d := model.InboxDelivery{RecipientID: recipient, SourceKind: w.SourceKind, SourceID: w.SourceID, EventKind: event, RunID: run.RunID, PipelineID: run.PipelineID, ProjectID: run.ProjectID, Status: status, OccurredAt: occurred, DeliveredAt: time.Now().UTC().Truncate(time.Millisecond)}
		exists, e := existingInboxDelivery(tx, &d, false)
		if e != nil {
			return e
		}
		current := func(context.Context) error { return nil }
		if !exists {
			approvalID := ""
			if a != nil {
				approvalID = a.ApprovalID
			}
			_, current, e = inboxAccess(ctx, tx, recipient, run, approvalID, authorize)
			if e != nil {
				return e
			}
			// Unique insertion cannot overwrite delivery/read history. A racing
			// conflict must still match all immutable typed event identity.
			if e = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&d).Error; e != nil {
				return e
			}
			if exists, e = existingInboxDelivery(tx, &d, true); e != nil {
				return e
			}
			if !exists {
				return ErrInboxSource
			}
		}

		patch := map[string]any{"next_check_at": time.Now().UTC().Add(30 * time.Second)}
		if event == "terminal" {
			patch["terminal_done"] = true
		}
		if event == "requested" && len(completeRequest) > 0 && completeRequest[0] {
			patch["requested_done"] = true
		}
		if e := inboxFence(tx, w, patch); e != nil {
			return e
		}
		return current(ctx)
	})
	if err == nil {
		w.Revision++
	}
	return err
}

// InboxReconcile bounds both source and recipient work and alternates source
// types, independent of browser traffic. It never changes domain lifecycle.
func (r *Repositories) InboxReconcile(ctx context.Context, authorize InboxAuthorize) (InboxTickResult, error) {
	out := InboxTickResult{}
	if authorize == nil {
		return out, ErrInboxUnavailable
	}
	if err := r.repairInbox(ctx, "run"); err != nil {
		return out, err
	}
	if err := r.repairInbox(ctx, "approval"); err != nil {
		return out, err
	}
	if err := r.cleanupInboxCursors(ctx); err != nil {
		return out, err
	}
	for i := 0; i < 100 && out.Recipients < 200; i++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		kind := "run"
		if i%2 == 1 {
			kind = "approval"
		}
		w, err := r.claimInbox(ctx, kind)
		if err != nil {
			return out, err
		}
		if w == nil {
			continue
		}
		out.Sources++
		db, err := r.inboxDB(ctx)
		if err != nil {
			return out, err
		}
		requested, terminal := w.RequestedDone, w.TerminalDone
		code := ""
		recipients := []string{}
		candidatePositions := map[string]int{}
		candidateCount := 0
		requester := ""
		if kind == "run" {
			run, e := inboxRun(db, w.SourceID)
			if e != nil {
				if !errors.Is(e, ErrInboxSource) {
					return out, e
				}
				code = "invalid_source"
			} else if admittedInboxRun(run) && inboxRunStatus(run.Status) != "" && run.EndTime != nil {
				requester = run.InboxActorID
			}
		} else {
			a, run, candidates, e := inboxApproval(ctx, db, w.SourceID)
			if e != nil {
				if !errors.Is(e, ErrInboxSource) {
					return out, e
				}
				code = "invalid_source"
			} else {
				if !requested {
					candidateCount = len(candidates)
					for index, id := range candidates {
						candidatePositions[id] = index
					}
					recipients, e = pendingInboxRecipients(db, a, run, candidates, w.RecipientOffset)
					if e != nil {
						if errors.Is(e, ErrInboxSource) {
							code = "invalid_source"
						} else {
							return out, e
						}
					} else if len(recipients) == 0 {
						requested = true
					}
				}
				if model.IsApprovalTerminal(a.Status) && !terminal {
					requester = run.InboxActorID
				}
			}
		}
		need := len(recipients)
		if requester != "" {
			need++
		}
		if out.Recipients+need > 200 {
			if err := r.finishInbox(ctx, w, requested, terminal, "budget"); err != nil && !errors.Is(err, errInboxFence) {
				return out, err
			}
			continue
		}
		all := true
		for index, recipient := range recipients {
			out.Recipients++
			next := (candidatePositions[recipient] + 1) % candidateCount
			if e := r.advanceInboxRecipient(ctx, w, next); e != nil {
				if !errors.Is(e, errInboxFence) {
					return out, e
				}
				code = "superseded"
				all = false
				break
			}
			e := r.deliverInbox(ctx, w, recipient, "requested", authorize, all && index == len(recipients)-1)
			if e != nil {
				all = false
				if errors.Is(e, errInboxFence) {
					code = "superseded"
					break
				}
				code = inboxProjectionCode(e)
				out.Deferred++
			}
		}
		if len(recipients) > 0 && all {
			requested = true
		}
		if requester != "" && code != "superseded" {
			out.Recipients++
			e := r.deliverInbox(ctx, w, requester, "terminal", authorize)
			if e == nil {
				terminal = true
			} else {
				if errors.Is(e, errInboxFence) {
					code = "superseded"
				} else {
					code = inboxProjectionCode(e)
				}
				out.Deferred++
			}
		}
		if code == "invalid_source" {
			out.Invalid++
		}
		if code != "superseded" {
			if err := r.finishInbox(ctx, w, requested, terminal, code); err != nil && !errors.Is(err, errInboxFence) {
				return out, err
			}
		}
	}
	return out, nil
}

func (r *Repositories) repairInbox(ctx context.Context, kind string) error {
	db, err := r.inboxDB(ctx)
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.InboxSweep{Kind: kind}).Error; err != nil {
			return err
		}
		var s model.InboxSweep
		if err := tx.Where("kind = ?", kind).First(&s).Error; err != nil {
			return err
		}
		table := "pipeline_run"
		business := "run_id"
		if kind == "approval" {
			table = "approval_request"
			business = "approval_id"
		}
		if s.UpperID == 0 {
			var upper struct{ ID uint64 }
			if err := tx.Table(table).Select("id").Where("inbox_origin = 1").Order("id DESC").Limit(1).Find(&upper).Error; err != nil {
				return err
			}
			s.UpperID = upper.ID
		}
		var rows []struct {
			ID       uint64
			SourceID string
		}
		if s.UpperID > 0 {
			if err := tx.Table(table).Select("id, "+business+" AS source_id").Where("inbox_origin = 1 AND id > ? AND id <= ?", s.AfterID, s.UpperID).Order("id ASC").Limit(100).Find(&rows).Error; err != nil {
				return err
			}
		}
		for _, row := range rows {
			if validApprovalID(row.SourceID) {
				if err := enrollInboxWork(tx, kind, row.SourceID); err != nil {
					return err
				}
			}
			s.AfterID = row.ID
		}
		if len(rows) < 100 || s.AfterID >= s.UpperID {
			s.AfterID = 0
			s.UpperID = 0
		}
		result := tx.Model(&model.InboxSweep{}).Where("kind = ? AND revision = ?", kind, s.Revision).Updates(map[string]any{"after_id": s.AfterID, "upper_id": s.UpperID, "revision": s.Revision + 1})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errInboxFence
		}
		return nil
	})
}
func (r *Repositories) cleanupInboxCursors(ctx context.Context) error {
	db, err := r.inboxDB(ctx)
	if err != nil {
		return err
	}
	var ids []uint64
	if err := db.Model(&model.InboxCursor{}).Where("expires_at <= ?", time.Now().UTC()).Order("expires_at ASC").Limit(100).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) > 0 {
		return db.Where("id IN ?", ids).Delete(&model.InboxCursor{}).Error
	}
	return nil
}

// Existing durable delivery is progress, even after its recipient loses access.
// Re-authorizing an already delivered prefix can starve later recipients. Check
// complete immutable identity against the freshly validated source; never trust
// mere existence and never change read_at or timestamps on replay.
func pendingInboxRecipients(tx *gorm.DB, a *model.ApprovalRequest, run *model.PipelineRun, candidates []string, offsets ...int) ([]string, error) {
	var rows []model.InboxDelivery
	if err := tx.Select("recipient_id", "source_kind", "source_id", "event_kind", "run_id", "pipeline_id", "project_id", "status", "occurred_at").Where("recipient_id IN ? AND source_kind = ? AND source_id = ? AND event_kind = ?", candidates, "approval", a.ApprovalID, "requested").Limit(101).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > 100 {
		return nil, ErrInboxSource
	}
	candidateSet := map[string]bool{}
	for _, id := range candidates {
		candidateSet[id] = true
	}
	done := map[string]bool{}
	for _, d := range rows {
		if !candidateSet[d.RecipientID] || done[d.RecipientID] || d.SourceKind != "approval" || d.SourceID != a.ApprovalID || d.EventKind != "requested" || d.RunID != run.RunID || d.PipelineID != run.PipelineID || d.ProjectID != run.ProjectID || d.Status != "pending" || !d.OccurredAt.Equal(a.CreatedAt) {
			return nil, ErrInboxSource
		}
		done[d.RecipientID] = true
	}
	pending := make([]string, 0, len(candidates))
	offset := 0
	if len(offsets) > 0 && offsets[0] >= 0 {
		offset = offsets[0] % len(candidates)
	}
	for index := range candidates {
		id := candidates[(index+offset)%len(candidates)]
		if !done[id] {
			pending = append(pending, id)
		}
	}
	return pending, nil
}

// Checkpoint the next candidate before attempting authorization. A slow or
// failing recipient cannot monopolize every restarted five-second tick. This
// work-only transaction ends before any domain lock is acquired.
func (r *Repositories) advanceInboxRecipient(ctx context.Context, w *model.InboxWork, next int) error {
	db, err := r.inboxDB(ctx)
	if err != nil {
		return err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		return inboxFence(tx, w, map[string]any{"recipient_offset": next, "next_check_at": time.Now().UTC().Add(30 * time.Second)})
	})
	if err == nil {
		w.Revision++
		w.RecipientOffset = next
	}
	return err
}

func sameInboxDelivery(a, b *model.InboxDelivery) bool {
	return a.RecipientID == b.RecipientID && a.SourceKind == b.SourceKind && a.SourceID == b.SourceID && a.EventKind == b.EventKind && a.RunID == b.RunID && a.PipelineID == b.PipelineID && a.ProjectID == b.ProjectID && a.Status == b.Status && a.OccurredAt.Equal(b.OccurredAt)
}
func existingInboxDelivery(tx *gorm.DB, expected *model.InboxDelivery, current bool) (bool, error) {
	q := tx.Select("recipient_id", "source_kind", "source_id", "event_kind", "run_id", "pipeline_id", "project_id", "status", "occurred_at").Where("recipient_id = ? AND source_kind = ? AND source_id = ? AND event_kind = ?", expected.RecipientID, expected.SourceKind, expected.SourceID, expected.EventKind)
	if current {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []model.InboxDelivery
	if err := q.Limit(2).Find(&rows).Error; err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}
	if len(rows) != 1 || !sameInboxDelivery(expected, &rows[0]) {
		return false, ErrInboxSource
	}
	return true, nil
}

func inboxProjectionCode(err error) string {
	if errors.Is(err, ErrInboxSource) {
		return "invalid_source"
	}
	if errors.Is(err, ErrInboxForbidden) || errors.Is(err, gorm.ErrRecordNotFound) {
		return "access_deferred"
	}
	return "source_unavailable"
}
