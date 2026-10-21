// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"time"

	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
)

// Advisory controller work only: no executable bytes, credentials or authority.
// Seal/DispatchReference/finalization must independently revalidate these hints.
type CopilotRerunDispatchWork struct {
	ExpiresAt                                           time.Time
	ScheduleID, IntentID, OwnerUserID, ProjectID, State string
	RunID, JobRunID, AgentID, ClaimID                   string
	IntentDigest, ReviewedRunContextDigest              string
	IntentVersion, GateVersion, ScheduleVersion         uint64
}

const CopilotRerunDispatchBatchMax = 32

// Each state has separate NULL and non-NULL lease buckets. The cursor tracks
// inspected candidates, including expired/cancelled rows, before eligibility
// filtering. It is only observation progress, never execution authority.
// Lease changes may move rows behind the cursor; wrapping revisits them. Restart
// resets to zero and safely repeats observation, not execution or claim identity.
type CopilotRerunDispatchCursor struct {
	Bucket    uint8
	Positions [6]CopilotRerunDispatchPosition
}

// Independent keysets prevent a perpetually full bucket starving other states.
type CopilotRerunDispatchPosition struct {
	LeaseUntil time.Time
	ID         uint64
}
type CopilotRerunDispatchPage struct {
	Next      CopilotRerunDispatchCursor
	Work      []CopilotRerunDispatchWork
	Inspected int
}

func (r *Repositories) rerunDispatchDB(ctx context.Context) (*gorm.DB, error) {
	if r == nil || r.database == nil {
		return nil, ErrCopilotRerunInvalid
	}
	db := r.database.Database().WithContext(ctx).Clauses(database.Write())
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		return nil, ErrCopilotRerunInvalid
	}
	return db, nil
}
func (r *Repositories) CopilotRerunScanDispatch(ctx context.Context, cursor CopilotRerunDispatchCursor, limit int) (*CopilotRerunDispatchPage, error) {
	if limit < 1 || limit > CopilotRerunDispatchBatchMax || cursor.Bucket >= 6 {
		return nil, ErrCopilotRerunInvalid
	}
	for bucket, position := range cursor.Positions {
		if (bucket%2 == 0 && !position.LeaseUntil.IsZero()) || (position.ID == 0 && !position.LeaseUntil.IsZero()) || (position.ID != 0 && bucket%2 == 1 && position.LeaseUntil.IsZero()) {
			return nil, ErrCopilotRerunInvalid
		}
	}
	position := cursor.Positions[cursor.Bucket]
	db, err := r.rerunDispatchDB(ctx)
	if err != nil {
		return nil, err
	}
	states := []string{CopilotRerunSchedulePending, CopilotRerunScheduleSealed, CopilotRerunScheduleClaimed}
	state := states[cursor.Bucket/2]
	// No joins or post-index eligibility predicates before LIMIT: sparse expired,
	// cancelled or malformed candidates cannot turn a bounded page into a scan of
	// all history. The existing (state,lease_until,id) index matches these orders.
	q := db.Table("copilot_rerun_schedule").Select("id, schedule_id, lease_until").Where("state = ?", state)
	if cursor.Bucket%2 == 0 {
		q = q.Where("lease_until IS NULL AND id > ?", position.ID).Order("id")
	} else {
		q = q.Where("lease_until IS NOT NULL")
		if state != CopilotRerunScheduleClaimed {
			q = q.Where("lease_until <= ?", time.Now().UTC())
		}
		if position.ID != 0 {
			q = q.Where("lease_until > ? OR (lease_until = ? AND id > ?)", position.LeaseUntil, position.LeaseUntil, position.ID)
		}
		q = q.Order("lease_until, id")
	}
	var candidates []struct {
		ID         uint64
		ScheduleID string
		LeaseUntil *time.Time
	}
	if err = q.Limit(limit).Find(&candidates).Error; err != nil {
		return nil, rerunError(err)
	}
	page := &CopilotRerunDispatchPage{Next: cursor, Inspected: len(candidates)}
	// Rotate after every bounded page, including full pages. Each bucket keeps
	// its own position; exhaustion resets only that bucket for its next visit.
	page.Next.Bucket = (cursor.Bucket + 1) % 6
	if len(candidates) < limit {
		page.Next.Positions[cursor.Bucket] = CopilotRerunDispatchPosition{}
	} else {
		last := candidates[len(candidates)-1]
		next := CopilotRerunDispatchPosition{ID: last.ID}
		if last.LeaseUntil != nil {
			next.LeaseUntil = *last.LeaseUntil
		}
		page.Next.Positions[cursor.Bucket] = next
	}
	for _, candidate := range candidates {
		work, err := r.CopilotRerunReadDispatchWork(ctx, candidate.ScheduleID)
		if errors.Is(err, ErrCopilotRerunConflict) || errors.Is(err, ErrCopilotRerunInvalid) {
			continue
		}
		if err != nil {
			return nil, err
		}
		page.Work = append(page.Work, *work)
	}
	return page, nil
}

// Current primary advisory lookup. Claimed work is returned only for observation;
// it is categorically ineligible for DispatchReference and new queue publication.
func (r *Repositories) CopilotRerunReadDispatchWork(ctx context.Context, id string) (*CopilotRerunDispatchWork, error) {
	if !rerunUUID(id) {
		return nil, ErrCopilotRerunInvalid
	}
	db, err := r.rerunDispatchDB(ctx)
	if err != nil {
		return nil, err
	}
	var rows []CopilotRerunDispatchWork
	q := db.Table("copilot_rerun_schedule AS s").Select("s.schedule_id, s.intent_id, s.owner_user_id, s.project_id, s.state, s.run_id, s.job_run_id, s.agent_id, s.claimed_id AS claim_id, i.intent_digest, i.reviewed_run_context_digest, i.version AS intent_version, g.version AS gate_version, s.version AS schedule_version, s.expires_at").Joins("JOIN copilot_rerun_intent AS i ON i.intent_id = s.intent_id AND i.schedule_id = s.schedule_id").Joins("JOIN copilot_rerun_admission_gate AS g ON g.schedule_id = s.schedule_id AND g.gate_id = i.gate_id").Where("s.schedule_id = ? AND i.state = ? AND s.completed_at IS NULL", id, CopilotRerunConfirmed).Where("(s.state IN ? AND s.claimed_id = ? AND s.cancel_requested = ? AND g.state = ? AND g.claim_id = ?) OR (s.state = ? AND s.claimed_id <> ? AND g.state = ? AND g.claim_id = s.claimed_id)", []string{CopilotRerunSchedulePending, CopilotRerunScheduleSealed}, "", false, "unclaimed", "", CopilotRerunScheduleClaimed, "", "claimed")
	if err = q.Limit(2).Scan(&rows).Error; err != nil {
		return nil, rerunError(err)
	}
	if len(rows) != 1 || rows[0].ScheduleID != id {
		return nil, ErrCopilotRerunConflict
	}
	return &rows[0], nil
}
