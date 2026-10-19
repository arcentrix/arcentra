// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var (
	ErrInboxInvalid     = errors.New("invalid inbox request")
	ErrInboxCursor      = errors.New("invalid inbox cursor")
	ErrInboxForbidden   = errors.New("current inbox access denied")
	ErrInboxSource      = errors.New("inbox source invalid")
	ErrInboxUnavailable = errors.New("inbox unavailable")
)

const InboxScanLimit = 200

// A nil project requests only enabled, exact human identity validation.
type InboxAuthorize func(context.Context, *Repositories, string, *model.Project, string, string) error

type InboxTarget struct {
	Type       string `json:"type"`
	RunID      string `json:"runId"`
	PipelineID string `json:"pipelineId"`
	ProjectID  string `json:"projectId"`
	ApprovalID string `json:"approvalId,omitempty"`
}
type InboxItem struct {
	ID           string       `json:"id"`
	Kind         string       `json:"kind"`
	Status       string       `json:"status"`
	OccurredAt   time.Time    `json:"occurredAt"`
	DeliveredAt  time.Time    `json:"deliveredAt"`
	ReadAt       *time.Time   `json:"readAt"`
	Target       *InboxTarget `json:"target"`
	Availability string       `json:"availability"`
}
type InboxPage struct {
	List           []InboxItem `json:"list"`
	NextCursor     *string     `json:"nextCursor"`
	ScanIncomplete bool        `json:"scanIncomplete"`
}
type InboxCount struct {
	Count      int  `json:"count"`
	Capped     bool `json:"capped"`
	Incomplete bool `json:"incomplete"`
}
type InboxRead struct {
	ID     string    `json:"id"`
	ReadAt time.Time `json:"readAt"`
}

func (r *Repositories) inboxDB(ctx context.Context) (*gorm.DB, error) {
	if r == nil || r.database == nil || r.database.Database() == nil {
		return nil, ErrInboxUnavailable
	}
	return r.database.Database().Session(&gorm.Session{NewDB: true, Logger: logger.Default.LogMode(logger.Silent)}).WithContext(ctx).Clauses(database.Write()).Session(&gorm.Session{}), nil
}
func inboxIdentity(ctx context.Context, tx *gorm.DB, owner string, authorize InboxAuthorize) error {
	if !validApprovalID(owner) || authorize == nil {
		return ErrInboxForbidden
	}
	view, current := NewScopedAuthorizationView(tx)
	if err := authorize(ctx, view, owner, nil, "", ""); err != nil {
		return err
	}
	return current(ctx)
}

// Locks are always pipeline -> pinned project -> subjects/catalog/grants. No
// work/delivery/cursor lock is held on entry. Historical archive is not denial.
func inboxAccess(ctx context.Context, tx *gorm.DB, owner string, run *model.PipelineRun, approvalID string, authorize InboxAuthorize) (bool, func(context.Context) error, error) {
	if !admittedInboxRun(run) || authorize == nil {
		return false, nil, ErrInboxSource
	}
	var pipelines []model.Pipeline
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("pipeline_id", "project_id", "is_deleted").Where("LOWER(pipeline_id) = LOWER(?)", run.PipelineID).Limit(2).Find(&pipelines).Error; err != nil {
		return false, nil, err
	}
	if len(pipelines) > 1 || len(pipelines) == 1 && pipelines[0].PipelineID != run.PipelineID {
		return false, nil, ErrInboxSource
	}
	target := len(pipelines) == 1 && pipelines[0].ProjectID == run.ProjectID && pipelines[0].IsDeleted == 0
	var projects []model.Project
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("project_id", "org_id").Where("LOWER(project_id) = LOWER(?)", run.ProjectID).Limit(2).Find(&projects).Error; err != nil {
		return false, nil, err
	}
	if len(projects) != 1 || projects[0].ProjectID != run.ProjectID {
		return false, nil, ErrInboxForbidden
	}
	view, current := NewScopedAuthorizationView(tx)
	if err := authorize(ctx, view, owner, &projects[0], run.PipelineID, approvalID); err != nil {
		return false, nil, err
	}
	return target, current, current(ctx)
}

func inboxVisible(ctx context.Context, tx *gorm.DB, owner string, d *model.InboxDelivery, authorize InboxAuthorize, checks ...*func(context.Context) error) (*InboxItem, error) {
	if d.RecipientID != owner {
		return nil, gorm.ErrRecordNotFound
	}
	var run *model.PipelineRun
	var a *model.ApprovalRequest
	var err error
	kind, status := "", d.Status
	switch d.SourceKind {
	case "run":
		run, err = inboxRun(tx, d.SourceID)
		kind = "run_result"
		if err == nil && (run.InboxActorID != owner || d.EventKind != "terminal" || inboxRunStatus(run.Status) == "" || run.EndTime == nil || !admittedInboxRun(run) || run.EndTime.Before(*run.StartTime) || d.Status != inboxRunStatus(run.Status)) {
			err = ErrInboxSource
		}
	case "approval":
		var candidates []string
		a, run, candidates, err = inboxApproval(ctx, tx, d.SourceID)
		if err == nil {
			status = inboxApprovalStatus(a.Status)
			switch d.EventKind {
			case "requested":
				kind = "approval_requested"
				found := false
				for _, id := range candidates {
					if id == owner {
						found = true
					}
				}
				if !found {
					err = ErrInboxSource
				}
			case "terminal":
				kind = "approval_result"
				if run.InboxActorID != owner || !model.IsApprovalTerminal(a.Status) || d.Status != status {
					err = ErrInboxSource
				}
			default:
				err = ErrInboxSource
			}
		}
	default:
		err = ErrInboxSource
	}
	if err != nil {
		return nil, err
	}
	if run.RunID != d.RunID || run.ProjectID != d.ProjectID || run.PipelineID != d.PipelineID {
		return nil, ErrInboxSource
	}
	approvalID := ""
	if a != nil {
		approvalID = a.ApprovalID
	}
	target, current, err := inboxAccess(ctx, tx, owner, run, approvalID, authorize)
	if err != nil {
		return nil, err
	}
	if len(checks) > 0 {
		*checks[0] = current
	}
	item := &InboxItem{ID: strconv.FormatUint(d.ID, 10), Kind: kind, Status: status, OccurredAt: d.OccurredAt, DeliveredAt: d.DeliveredAt, ReadAt: d.ReadAt, Availability: "unavailable"}
	if target {
		item.Target = &InboxTarget{Type: d.SourceKind, RunID: run.RunID, PipelineID: run.PipelineID, ProjectID: run.ProjectID}
		item.Availability = "available"
		if a != nil {
			item.Target.ApprovalID = a.ApprovalID
		}
	}
	if a != nil && a.Status == model.ApprovalStatusPending && a.ExpiresAt != nil && !time.Now().Before(*a.ExpiresAt) {
		item.Availability = "awaiting_resolution"
	}
	return item, nil
}
func hiddenInboxError(err error) bool {
	return errors.Is(err, ErrInboxForbidden) || errors.Is(err, ErrInboxSource) || errors.Is(err, gorm.ErrRecordNotFound)
}

func (r *Repositories) InboxList(ctx context.Context, owner, filter, cursor string, limit int, authorize InboxAuthorize) (*InboxPage, error) {
	if (filter != "all" && filter != "unread") || limit < 1 || limit > 50 {
		return nil, ErrInboxInvalid
	}
	db, err := r.inboxDB(ctx)
	if err != nil {
		return nil, err
	}
	before := uint64(0)
	if err = db.Transaction(func(tx *gorm.DB) error {
		if err := inboxIdentity(ctx, tx, owner, authorize); err != nil {
			return err
		}
		if cursor != "" {
			if len(cursor) != 64 {
				return ErrInboxCursor
			}
			var rows []model.InboxCursor
			if err := tx.Where("token = ?", cursor).Limit(2).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) != 1 || rows[0].Token != cursor || rows[0].OwnerID != owner || rows[0].Filter != filter || !time.Now().Before(rows[0].ExpiresAt) {
				return ErrInboxCursor
			}
			before = rows[0].BeforeID
		}
		return nil
	}); err != nil {
		return nil, err
	}
	q := db.Where("recipient_id = ?", owner)
	if filter == "unread" {
		q = q.Where("read_at IS NULL")
	}
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	var rows []model.InboxDelivery
	if err = q.Order("id DESC").Limit(InboxScanLimit + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	page := &InboxPage{List: make([]InboxItem, 0, limit)}
	scanned := 0
	last := uint64(0)
	for i := 0; i < len(rows) && i < InboxScanLimit; i++ {
		d := rows[i]
		scanned++
		last = d.ID
		var item *InboxItem
		err = db.Transaction(func(tx *gorm.DB) error { var e error; item, e = inboxVisible(ctx, tx, owner, &d, authorize); return e })
		if err != nil {
			if hiddenInboxError(err) {
				continue
			}
			return nil, err
		}
		page.List = append(page.List, *item)
		if len(page.List) == limit {
			break
		}
	}
	more := scanned < len(rows)
	page.ScanIncomplete = more && scanned == InboxScanLimit && len(page.List) < limit
	if more && last > 0 {
		token, e := r.inboxNewCursor(ctx, owner, filter, last, authorize)
		if e != nil {
			return nil, e
		}
		page.NextCursor = &token
	}
	// Recheck disabled/deleted identity even when every row was filtered.
	if err = db.Transaction(func(tx *gorm.DB) error { return inboxIdentity(ctx, tx, owner, authorize) }); err != nil {
		return nil, err
	}
	return page, nil
}
func (r *Repositories) inboxNewCursor(ctx context.Context, owner, filter string, before uint64, authorize InboxAuthorize) (string, error) {
	db, err := r.inboxDB(ctx)
	if err != nil {
		return "", err
	}
	var bytes [32]byte
	if _, err = rand.Read(bytes[:]); err != nil {
		return "", ErrInboxUnavailable
	}
	token := hex.EncodeToString(bytes[:])
	now := time.Now().UTC()
	err = db.Transaction(func(tx *gorm.DB) error {
		// The exact user row serializes capacity across Controls, before cursor locks.
		if err := inboxIdentity(ctx, tx, owner, authorize); err != nil {
			return err
		}
		var own []model.InboxCursor
		if err := tx.Where("owner_id = ?", owner).Order("id DESC").Limit(33).Find(&own).Error; err != nil {
			return err
		}
		ids := []uint64{}
		for i, c := range own {
			if i >= 31 || !now.Before(c.ExpiresAt) {
				ids = append(ids, c.ID)
			}
		}
		if len(ids) > 0 {
			if err := tx.Where("owner_id = ? AND id IN ?", owner, ids).Delete(&model.InboxCursor{}).Error; err != nil {
				return err
			}
		}
		return tx.Create(&model.InboxCursor{Token: token, OwnerID: owner, Filter: filter, BeforeID: before, ExpiresAt: now.Add(15 * time.Minute)}).Error
	})
	return token, err
}
func (r *Repositories) InboxUnreadCount(ctx context.Context, owner string, authorize InboxAuthorize) (*InboxCount, error) {
	db, err := r.inboxDB(ctx)
	if err != nil {
		return nil, err
	}
	if err = db.Transaction(func(tx *gorm.DB) error { return inboxIdentity(ctx, tx, owner, authorize) }); err != nil {
		return nil, err
	}
	var rows []model.InboxDelivery
	if err = db.Where("recipient_id = ? AND read_at IS NULL", owner).Order("id DESC").Limit(InboxScanLimit + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := &InboxCount{}
	for i, d := range rows {
		if i == InboxScanLimit {
			out.Incomplete = true
			break
		}
		err = db.Transaction(func(tx *gorm.DB) error { _, e := inboxVisible(ctx, tx, owner, &d, authorize); return e })
		if err != nil {
			if hiddenInboxError(err) {
				continue
			}
			return nil, err
		}
		out.Count++
		if out.Count > 99 {
			out.Count = 99
			out.Capped = true
			break
		}
	}
	if err = db.Transaction(func(tx *gorm.DB) error { return inboxIdentity(ctx, tx, owner, authorize) }); err != nil {
		return nil, err
	}
	return out, nil
}
func (r *Repositories) InboxMarkRead(ctx context.Context, owner, id string, authorize InboxAuthorize) (*InboxRead, error) {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != id {
		return nil, gorm.ErrRecordNotFound
	}
	db, err := r.inboxDB(ctx)
	if err != nil {
		return nil, err
	}
	var out InboxRead
	err = db.Transaction(func(tx *gorm.DB) error {
		// Unlocked lookup precedes domain/auth locks. Ownership is checked again in
		// the mutation predicate; only this route changes read_at and only NULL->time.
		var rows []model.InboxDelivery
		if err := tx.Where("id = ? AND recipient_id = ?", n, owner).Limit(2).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].RecipientID != owner {
			return gorm.ErrRecordNotFound
		}
		d := rows[0]
		var current func(context.Context) error
		if _, err := inboxVisible(ctx, tx, owner, &d, authorize, &current); err != nil {
			if hiddenInboxError(err) {
				return gorm.ErrRecordNotFound
			}
			return err
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		if err := tx.Model(&model.InboxDelivery{}).Where("id = ? AND recipient_id = ? AND read_at IS NULL", n, owner).UpdateColumn("read_at", now).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("read_at").Where("id = ? AND recipient_id = ?", n, owner).First(&d).Error; err != nil {
			return err
		}
		if d.ReadAt == nil {
			return ErrInboxUnavailable
		}
		out = InboxRead{ID: id, ReadAt: *d.ReadAt}
		return current(ctx)
	})
	return &out, err
}
