// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func historyPlanKind(s string) bool {
	switch s {
	case "code_patch", "new_run_review", "retry_approval_review", "deployment_review":
		return true
	}
	return false
}
func historyPlanReason(s string) bool {
	switch s {
	case "", "cancelled", "expired", "deleted", "scope_changed", "source_unavailable", "source_changed", "target_changed", "policy_changed", "content_unavailable", "invalidated":
		return true
	}
	return false
}
func historyFindPlan(tx *gorm.DB, c *model.CopilotConversation, id string, byRequest bool) (*model.CopilotActionPlan, error) {
	if !historyID(id) {
		return nil, ErrCopilotHistoryInvalid
	}
	var rows []model.CopilotActionPlan
	field := "plan_id"
	if byRequest {
		field = "request_id"
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("owner_user_id = ? AND project_id = ? AND "+field+" = ?", c.OwnerUserID, c.ProjectID, id).Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrCopilotHistoryNotFound
	}
	p := &rows[0]
	match := p.PlanID
	if byRequest {
		match = p.RequestID
	}
	if len(rows) != 1 || p.OwnerUserID != c.OwnerUserID || p.ProjectID != c.ProjectID || p.ConversationID != c.ConversationID || p.PipelineID != c.PipelineID || p.RunID != c.RunID || match != id {
		return nil, ErrCopilotHistoryNotFound
	}
	historyTrackExpiry(tx, p.ExpiresAt)
	return p, nil
}
func historyPlanParent(tx *gorm.DB, c *model.CopilotConversation, p *model.CopilotActionPlan) error {
	t, err := historyFindTurn(tx, c, p.TurnID, false)
	if err != nil {
		return err
	}
	if t.State != "completed" || !historyNow().Before(t.ExpiresAt) || !historyNow().Before(p.ExpiresAt) || p.ExpiresAt.After(t.ExpiresAt) {
		return ErrCopilotHistoryNotFound
	}
	return nil
}
func (r *Repositories) CopilotHistoryCreatePlan(ctx context.Context, in CopilotHistoryPlanInput, authorize CopilotHistoryAuthorize) (*model.CopilotActionPlan, bool, error) {
	if !historyID(in.PlanID) || !historyID(in.RequestID) || !historyID(in.TurnID) || !historyPlanKind(in.Kind) || !historyBinding(in.PlanDigest) || in.SchemaVersion == 0 || in.ConversationVersion == 0 || in.TurnVersion == 0 || !historyPayload(in.PayloadCiphertext, in.CipherVersion, true) || len(in.PayloadCiphertext) > 64*1024 {
		return nil, false, ErrCopilotHistoryInvalid
	}
	var out *model.CopilotActionPlan
	created := false
	err := r.historyConversationTx(ctx, in.OwnerUserID, in.ProjectID, in.ConversationID, authorize, func(tx *gorm.DB, c *model.CopilotConversation) error {
		t, e := historyFindTurn(tx, c, in.TurnID, false)
		if e != nil {
			return e
		}
		old, e := historyFindPlan(tx, c, in.RequestID, true)
		if e == nil {
			if old.Kind != in.Kind || old.TurnID != in.TurnID || old.PlanDigest != in.PlanDigest || old.SchemaVersion != in.SchemaVersion {
				return ErrCopilotHistoryConflict
			}
			if e = historyPlanParent(tx, c, old); e != nil {
				return e
			}
			out = old
			return nil
		}
		if e != ErrCopilotHistoryNotFound {
			return e
		}
		if t.State != "completed" || !historyNow().Before(t.ExpiresAt) || t.Version != in.TurnVersion || c.Version != in.ConversationVersion {
			return ErrCopilotHistoryConflict
		}
		var n int64
		if e = tx.Model(&model.CopilotActionPlan{}).Where("conversation_id = ?", c.ConversationID).Count(&n).Error; e != nil {
			return e
		}
		if n >= CopilotHistoryMaxPlans {
			return ErrCopilotHistoryCapacity
		}
		now := historyNow()
		expires := now.Add(CopilotHistoryPlanTTL)
		if t.ExpiresAt.Before(expires) {
			expires = t.ExpiresAt
		}
		p := &model.CopilotActionPlan{PlanID: in.PlanID, RequestID: in.RequestID, ConversationID: c.ConversationID, TurnID: t.TurnID, OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, PipelineID: c.PipelineID, RunID: c.RunID, Kind: in.Kind, State: "proposed", Version: 1, PlanDigest: in.PlanDigest, SchemaVersion: in.SchemaVersion, PayloadCiphertext: append([]byte(nil), in.PayloadCiphertext...), CipherVersion: in.CipherVersion, PayloadPresent: true, CreatedAt: now, ExpiresAt: expires}
		if e = tx.Create(p).Error; e != nil {
			return ErrCopilotHistoryConflict
		}
		out = p
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}
func (r *Repositories) CopilotHistoryGetPlan(ctx context.Context, owner, project, conversation, id string, authorize CopilotHistoryAuthorize) (*model.CopilotActionPlan, error) {
	var out *model.CopilotActionPlan
	err := r.historyConversationTx(ctx, owner, project, conversation, authorize, func(tx *gorm.DB, c *model.CopilotConversation) error {
		p, e := historyFindPlan(tx, c, id, false)
		if e != nil {
			return e
		}
		if e = historyPlanParent(tx, c, p); e != nil {
			return e
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
func (r *Repositories) CopilotHistoryPlans(ctx context.Context, owner, project, conversation string, after uint64, limit int, authorize CopilotHistoryAuthorize) (*CopilotHistoryPlanPage, error) {
	if limit < 1 || limit > 20 {
		return nil, ErrCopilotHistoryInvalid
	}
	page := &CopilotHistoryPlanPage{List: []*model.CopilotActionPlan{}}
	err := r.historyConversationTx(ctx, owner, project, conversation, authorize, func(tx *gorm.DB, c *model.CopilotConversation) error {
		var rows []model.CopilotActionPlan
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("conversation_id = ? AND id > ? AND expires_at > ?", c.ConversationID, after, historyNow()).Order("id ASC").Limit(limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		size := 0
		for i := range rows {
			p := &rows[i]
			if p.OwnerUserID != owner || p.ProjectID != project || p.ConversationID != c.ConversationID || p.PipelineID != c.PipelineID || p.RunID != c.RunID {
				return ErrCopilotHistoryNotFound
			}
			if len(page.List) >= limit || size+len(p.PayloadCiphertext) > CopilotHistoryMaxPageBytes {
				if len(page.List) > 0 {
					page.NextAfter = page.List[len(page.List)-1].ID
				}
				break
			}
			if e := historyPlanParent(tx, c, p); e != nil {
				return e
			}
			historyTrackExpiry(tx, p.ExpiresAt)
			size += len(p.PayloadCiphertext)
			page.List = append(page.List, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}
func (r *Repositories) CopilotHistoryTransitionPlan(ctx context.Context, in CopilotHistoryPlanTransitionInput, authorize CopilotHistoryAuthorize) (*model.CopilotActionPlan, error) {
	if in.Version == 0 || !historyBinding(in.PlanDigest) || !historyPlanReason(in.ReasonCode) || (in.State != "reviewed" && in.State != "cancelled" && in.State != "invalidated") {
		return nil, ErrCopilotHistoryInvalid
	}
	var out *model.CopilotActionPlan
	err := r.historyConversationTx(ctx, in.OwnerUserID, in.ProjectID, in.ConversationID, authorize, func(tx *gorm.DB, c *model.CopilotConversation) error {
		p, e := historyFindPlan(tx, c, in.PlanID, false)
		if e != nil {
			return e
		}
		if e = historyPlanParent(tx, c, p); e != nil {
			return e
		}
		if p.Version != in.Version || p.PlanDigest != in.PlanDigest || (p.State != "proposed" && p.State != "reviewed") || in.State == "reviewed" && p.State != "proposed" {
			return ErrCopilotHistoryConflict
		}
		updates := map[string]any{"state": in.State, "version": p.Version + 1, "reason_code": in.ReasonCode}
		if in.State == "reviewed" {
			now := historyNow()
			updates["reviewed_at"] = now
			p.ReviewedAt = &now
		}
		res := tx.Model(&model.CopilotActionPlan{}).Where("id = ? AND version = ? AND state = ?", p.ID, p.Version, p.State).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrCopilotHistoryConflict
		}
		p.State = in.State
		p.Version++
		p.ReasonCode = in.ReasonCode
		out = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
