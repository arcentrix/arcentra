// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func historyError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrCopilotHistoryInvalid, ErrCopilotHistoryUnavailable, ErrCopilotHistoryNotFound, ErrCopilotHistoryForbidden, ErrCopilotHistoryConflict, ErrCopilotHistoryCapacity, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrCopilotHistoryUnavailable
}
func historyID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':') {
			return false
		}
	}
	return true
}
func historyBinding(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\r\n\x00")
}
func historyPayload(p []byte, v uint32, required bool) bool {
	return len(p) <= CopilotHistoryMaxCiphertext && ((len(p) == 0 && !required && v == 0) || (len(p) > 0 && v > 0))
}
func historyNow() time.Time { return time.Now().UTC() }
func historyMaxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func historyActive(s string) bool { return s == "reserved" || s == "dispatching" }
func historyScope(c *model.CopilotConversation) CopilotHistoryScope {
	return CopilotHistoryScope{c.OwnerUserID, c.ProjectID, c.PipelineID, c.RunID}
}
func historyScopeValid(s CopilotHistoryScope) bool {
	return historyID(s.OwnerUserID) && historyID(s.ProjectID) && historyID(s.PipelineID) && historyID(s.RunID)
}
func (r *Repositories) historyDB(ctx context.Context) (*gorm.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil || r.database == nil || r.database.Database() == nil {
		return nil, ErrCopilotHistoryUnavailable
	}
	return r.database.Database().Session(&gorm.Session{NewDB: true, Logger: logger.Default.LogMode(logger.Silent)}).WithContext(ctx).Clauses(database.Write()).Session(&gorm.Session{}), nil
}

// No history locks are held on entry. Current reads enforce domain -> actor /
// grants -> conversation -> turn/plan. Exact user locking serializes admission
// across projects, conversations and Control instances (including zero actives).
func historyAccess(ctx context.Context, tx *gorm.DB, s CopilotHistoryScope, authorize CopilotHistoryAuthorize) (func(context.Context) error, error) {
	if !historyScopeValid(s) || authorize == nil {
		return nil, ErrCopilotHistoryForbidden
	}
	if tx.Dialector.Name() == "sqlite" {
		if err := tx.Model(&model.Pipeline{}).Where("pipeline_id = ?", s.PipelineID).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
			return nil, err
		}
	}
	var pipes []model.Pipeline
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("pipeline_id", "project_id", "is_enabled", "is_deleted").Where("pipeline_id = ?", s.PipelineID).Limit(2).Find(&pipes).Error; err != nil {
		return nil, err
	}
	if len(pipes) != 1 || pipes[0].PipelineID != s.PipelineID || pipes[0].ProjectID != s.ProjectID || pipes[0].IsEnabled != 1 || pipes[0].IsDeleted != 0 {
		return nil, ErrCopilotHistoryNotFound
	}
	var ps []model.Project
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("project_id", "org_id", "is_enabled", "status").Where("project_id = ?", s.ProjectID).Limit(2).Find(&ps).Error; err != nil {
		return nil, err
	}
	if len(ps) != 1 || ps[0].ProjectID != s.ProjectID || ps[0].IsEnabled != 1 || ps[0].Status != model.ProjectStatusActive {
		return nil, ErrCopilotHistoryNotFound
	}
	var runs []model.PipelineRun
	// Run scope is immutable under normal admission. A current read still rejects
	// legacy/unpinned or externally modified run identities before any history lock.
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("run_id", "pipeline_id", "project_id").Where("run_id = ?", s.RunID).Limit(2).Find(&runs).Error; err != nil {
		return nil, err
	}
	if len(runs) != 1 || runs[0].RunID != s.RunID || runs[0].PipelineID != s.PipelineID || runs[0].ProjectID != s.ProjectID {
		return nil, ErrCopilotHistoryNotFound
	}
	view, current := NewScopedAuthorizationView(tx)
	actor, err := view.User.(IExecutionActorReader).GetExecutionActor(ctx, s.OwnerUserID)
	if err != nil || actor.UserID != s.OwnerUserID || actor.IsEnabled != 1 {
		return nil, ErrCopilotHistoryForbidden
	}
	if err = authorize(ctx, view, s.OwnerUserID, &ps[0], s.PipelineID); err != nil {
		return nil, ErrCopilotHistoryForbidden
	}
	return current, nil
}
func historyFindConversation(db *gorm.DB, owner, project, id string, lock bool) (*model.CopilotConversation, error) {
	if !historyID(owner) || !historyID(project) || !historyID(id) {
		return nil, ErrCopilotHistoryInvalid
	}
	var rows []model.CopilotConversation
	q := db.Where("owner_user_id = ? AND project_id = ? AND conversation_id = ?", owner, project, id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != 1 || rows[0].OwnerUserID != owner || rows[0].ProjectID != project || rows[0].ConversationID != id {
		return nil, ErrCopilotHistoryNotFound
	}
	return &rows[0], nil
}

type historyExpiryKey struct{}

// Remember the earliest child expiry observed by this transaction and recheck
// it at commit, alongside live grant expiry. No child TTL is extended by reads.
func historyTrackExpiry(tx *gorm.DB, until time.Time) {
	if expiry, ok := tx.Statement.Context.Value(historyExpiryKey{}).(*time.Time); ok && (expiry.IsZero() || until.Before(*expiry)) {
		*expiry = until
	}
}

func historyLive(c *model.CopilotConversation) bool {
	return c.State == "active" && historyNow().Before(c.ExpiresAt)
}
func (r *Repositories) historyConversationTx(ctx context.Context, owner, project, id string, authorize CopilotHistoryAuthorize, fn func(*gorm.DB, *model.CopilotConversation) error) error {
	db, err := r.historyDB(ctx)
	if err != nil {
		return historyError(err)
	}
	hint, err := historyFindConversation(db, owner, project, id, false)
	if err != nil {
		return historyError(err)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		current, e := historyAccess(ctx, tx, historyScope(hint), authorize)
		if e != nil {
			return e
		}
		c, e := historyFindConversation(tx, owner, project, id, true)
		if e != nil {
			return e
		}
		if c.PipelineID != hint.PipelineID || c.RunID != hint.RunID || !historyLive(c) {
			return ErrCopilotHistoryNotFound
		}
		expiry := c.ExpiresAt
		tx = tx.WithContext(context.WithValue(tx.Statement.Context, historyExpiryKey{}, &expiry))
		if e = fn(tx, c); e != nil {
			return e
		}
		if !historyLive(c) || !historyNow().Before(expiry) {
			return ErrCopilotHistoryNotFound
		}
		return current(ctx)
	})
	return historyError(err)
}
func (r *Repositories) CopilotHistoryCreate(ctx context.Context, s CopilotHistoryScope, id, request, fingerprint string, authorize CopilotHistoryAuthorize) (*model.CopilotConversation, error) {
	if !historyScopeValid(s) || !historyID(id) || !historyID(request) || !historyBinding(fingerprint) {
		return nil, ErrCopilotHistoryInvalid
	}
	db, err := r.historyDB(ctx)
	if err != nil {
		return nil, historyError(err)
	}
	var result *model.CopilotConversation
	err = db.Transaction(func(tx *gorm.DB) error {
		current, e := historyAccess(ctx, tx, s, authorize)
		if e != nil {
			return e
		}
		var matches []model.CopilotConversation
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("owner_user_id = ? AND project_id = ? AND create_request_id = ?", s.OwnerUserID, s.ProjectID, request).Limit(2).Find(&matches).Error; e != nil {
			return e
		}
		if len(matches) > 0 {
			c := &matches[0]
			if len(matches) != 1 || c.OwnerUserID != s.OwnerUserID || c.ProjectID != s.ProjectID || c.CreateRequestID != request || c.PipelineID != s.PipelineID || c.RunID != s.RunID || c.CreateFingerprint != fingerprint {
				return ErrCopilotHistoryConflict
			}
			if !historyLive(c) {
				return ErrCopilotHistoryNotFound
			}
			result = c
			return current(ctx)
		}
		var count int64
		if e = tx.Model(&model.CopilotConversation{}).Where("owner_user_id = ?", s.OwnerUserID).Count(&count).Error; e != nil {
			return e
		}
		if count >= 1000 {
			return ErrCopilotHistoryCapacity
		}
		now := historyNow()
		c := &model.CopilotConversation{ConversationID: id, OwnerUserID: s.OwnerUserID, ProjectID: s.ProjectID, PipelineID: s.PipelineID, RunID: s.RunID, CreateRequestID: request, CreateFingerprint: fingerprint, State: "active", Version: 1, NextSequence: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(CopilotHistoryBodyTTL), TombstoneUntil: now.Add(CopilotHistoryTombstoneTTL)}
		if e = tx.Create(c).Error; e != nil {
			return ErrCopilotHistoryConflict
		}
		result = c
		return current(ctx)
	})
	if err != nil {
		return nil, historyError(err)
	}
	return result, nil
}
func (r *Repositories) CopilotHistoryGet(ctx context.Context, owner, project, id string, authorize CopilotHistoryAuthorize) (*model.CopilotConversation, error) {
	var result *model.CopilotConversation
	err := r.historyConversationTx(ctx, owner, project, id, authorize, func(tx *gorm.DB, c *model.CopilotConversation) error { result = c; return nil })
	if err != nil {
		return nil, err
	}
	return result, nil
}
func historyCursor(owner, project string, before uint64) string {
	sum := sha256.Sum256([]byte(owner + "\x00" + project))
	return base64.RawURLEncoding.EncodeToString([]byte(hex.EncodeToString(sum[:]) + ":" + strconv.FormatUint(before, 10)))
}
func historyParseCursor(owner, project, cursor string) (uint64, error) {
	if cursor == "" {
		return 0, nil
	}
	if len(cursor) > 160 {
		return 0, ErrCopilotHistoryInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, ErrCopilotHistoryInvalid
	}
	p := strings.Split(string(raw), ":")
	if len(p) != 2 {
		return 0, ErrCopilotHistoryInvalid
	}
	before, err := strconv.ParseUint(p[1], 10, 64)
	if err != nil || before == 0 || historyCursor(owner, project, before) != cursor {
		return 0, ErrCopilotHistoryInvalid
	}
	return before, nil
}
func (r *Repositories) CopilotHistoryList(ctx context.Context, owner, project, cursor string, limit int, authorize CopilotHistoryAuthorize) (*CopilotHistoryConversationPage, error) {
	if !historyID(owner) || !historyID(project) || limit < 1 || limit > 50 || authorize == nil {
		return nil, ErrCopilotHistoryInvalid
	}
	before, err := historyParseCursor(owner, project, cursor)
	if err != nil {
		return nil, err
	}
	db, err := r.historyDB(ctx)
	if err != nil {
		return nil, historyError(err)
	}
	// Validate live project and actor even when the result is empty. No pipeline
	// scope is inferred from a cursor or from previously visible conversations.
	err = db.Transaction(func(tx *gorm.DB) error {
		var ps []model.Project
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("project_id", "org_id", "status", "is_enabled").Where("project_id = ?", project).Limit(2).Find(&ps).Error; e != nil {
			return e
		}
		if len(ps) != 1 || ps[0].ProjectID != project || ps[0].IsEnabled != 1 || ps[0].Status != model.ProjectStatusActive {
			return ErrCopilotHistoryForbidden
		}
		view, current := NewScopedAuthorizationView(tx)
		u, e := view.User.(IExecutionActorReader).GetExecutionActor(ctx, owner)
		if e != nil || u.UserID != owner || u.IsEnabled != 1 {
			return ErrCopilotHistoryForbidden
		}
		if e = authorize(ctx, view, owner, &ps[0], ""); e != nil {
			return ErrCopilotHistoryForbidden
		}
		return current(ctx)
	})
	if err != nil {
		return nil, historyError(err)
	}
	var rows []model.CopilotConversation
	q := db.Where("owner_user_id = ? AND project_id = ? AND state = ? AND expires_at > ?", owner, project, "active", historyNow())
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	if err = q.Order("id DESC").Limit(201).Find(&rows).Error; err != nil {
		return nil, historyError(err)
	}
	page := &CopilotHistoryConversationPage{List: []*model.CopilotConversation{}}
	for i := 0; i < len(rows) && i < 200; i++ {
		row := rows[i]
		if row.OwnerUserID != owner || row.ProjectID != project {
			continue
		}
		value, e := r.CopilotHistoryGet(ctx, owner, project, row.ConversationID, authorize)
		if e == nil {
			page.List = append(page.List, value)
		} else if !errors.Is(e, ErrCopilotHistoryNotFound) && !errors.Is(e, ErrCopilotHistoryForbidden) {
			return nil, e
		}
		if len(page.List) == limit || i == 199 {
			if i+1 < len(rows) {
				page.NextCursor = historyCursor(owner, project, row.ID)
			}
			page.ScanIncomplete = i == 199 && len(rows) > 200
			break
		}
	}
	return page, nil
}
