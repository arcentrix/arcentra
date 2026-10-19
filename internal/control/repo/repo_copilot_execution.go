// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CopilotExecutionPrepareInput struct {
	OwnerUserID, ProjectID, ConversationID, PlanID, PlanDigest, RequestID string
	PlanVersion                                                           uint64
	Review                                                                *retryapproval.Review
}
type CopilotExecutionMutationInput struct {
	OwnerUserID, ProjectID, IntentID, IntentDigest string
	Version                                        uint64
}

func executionError(err error) error {
	switch {
	case errors.Is(err, ErrApprovalForbidden):
		return ErrCopilotHistoryForbidden
	case errors.Is(err, ErrApprovalConflict), errors.Is(err, ErrApprovalExpired):
		return ErrCopilotHistoryConflict
	case errors.Is(err, ErrApprovalInvalid):
		return ErrCopilotHistoryInvalid
	default:
		return historyError(err)
	}
}
func executionDigest(i *model.CopilotExecutionIntent) string {
	// Explicit immutable fields: mutable receipt state must not alter the binding.
	b, _ := json.Marshal([]any{i.IntentID, i.OwnerUserID, i.ProjectID, i.RequestID, i.PipelineID, i.RunID, i.JobRunID, i.ApprovalID, i.ConversationID, i.PlanID, i.PlanVersion, i.PlanDigest, i.EvidenceHash, i.EvidenceDigest, i.DecisionID, i.ExpiresAt.UnixMilli()})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func executionFind(db *gorm.DB, owner, project, id string, lock bool) (*model.CopilotExecutionIntent, error) {
	if !historyID(owner) || !historyID(project) || !historyID(id) {
		return nil, ErrCopilotHistoryInvalid
	}
	q := db.Where("owner_user_id = ? AND project_id = ? AND intent_id = ?", owner, project, id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []model.CopilotExecutionIntent
	if err := q.Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, ErrCopilotHistoryNotFound
	}
	i := &rows[0]
	if i.OwnerUserID != owner || i.ProjectID != project || i.IntentID != id || i.IntentDigest != executionDigest(i) || i.Version == 0 {
		return nil, ErrCopilotHistoryConflict
	}
	return i, nil
}
func executionScope(i *model.CopilotExecutionIntent) CopilotHistoryScope {
	return CopilotHistoryScope{i.OwnerUserID, i.ProjectID, i.PipelineID, i.RunID}
}
func executionMatches(i *model.CopilotExecutionIntent, in CopilotExecutionPrepareInput, digest string) bool {
	return i.OwnerUserID == in.OwnerUserID && i.ProjectID == in.ProjectID && i.RequestID == in.RequestID && i.ConversationID == in.ConversationID && i.PlanID == in.PlanID && i.PlanVersion == in.PlanVersion && i.PlanDigest == in.PlanDigest && i.ApprovalID == in.Review.ApprovalID && i.JobRunID == in.Review.RetryJobRunID && i.EvidenceHash == in.Review.EvidenceHash && i.EvidenceDigest == digest && i.IntentDigest == executionDigest(i)
}
func (r *Repositories) CopilotExecutionPrepare(ctx context.Context, in CopilotExecutionPrepareInput, authorize CopilotHistoryAuthorize) (*model.CopilotExecutionIntent, bool, error) {
	if !historyID(in.RequestID) || !historyBinding(in.PlanDigest) || in.PlanVersion == 0 || in.Review == nil || in.Review.Evidence == nil {
		return nil, false, ErrCopilotHistoryInvalid
	}
	e := in.Review.Evidence
	digest, err := e.Digest()
	if err != nil || digest != in.Review.EvidenceHash || e.ApprovalID != in.Review.ApprovalID || e.Identity.JobRunID != in.Review.RetryJobRunID || e.Context.ProjectID != in.ProjectID || in.Review.Status != model.ApprovalStatusPending {
		return nil, false, ErrCopilotHistoryInvalid
	}
	var out *model.CopilotExecutionIntent
	created := false
	err = r.historyConversationTx(ctx, in.OwnerUserID, in.ProjectID, in.ConversationID, authorize, func(tx *gorm.DB, c *model.CopilotConversation) error {
		p, err := historyFindPlan(tx, c, in.PlanID, false)
		if err != nil {
			return err
		}
		if err = historyPlanParent(tx, c, p); err != nil {
			return err
		}
		if p.State != "reviewed" || p.Kind != "retry_approval_review" || p.Version != in.PlanVersion || p.PlanDigest != in.PlanDigest || !p.PayloadPresent || e.Context.PipelineID != c.PipelineID || e.Context.PipelineRunID != c.RunID {
			return ErrCopilotHistoryConflict
		}
		var old []model.CopilotExecutionIntent
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("owner_user_id = ? AND project_id = ? AND request_id = ?", in.OwnerUserID, in.ProjectID, in.RequestID).Limit(2).Find(&old).Error; err != nil {
			return err
		}
		if len(old) > 0 {
			if len(old) != 1 || !executionMatches(&old[0], in, digest) {
				return ErrCopilotHistoryConflict
			}
			out = &old[0]
			return nil
		}
		// Durable dedup rows are not silently purged; bound new per-owner/project
		// admissions instead of allowing unbounded identifier retention.
		var count int64
		if err = tx.Model(&model.CopilotExecutionIntent{}).Where("owner_user_id = ? AND project_id = ?", in.OwnerUserID, in.ProjectID).Count(&count).Error; err != nil {
			return err
		}
		if count >= 1000 {
			return ErrCopilotHistoryCapacity
		}
		expires := time.UnixMilli(e.ExpiresAtUnixMilli).UTC()
		if p.ExpiresAt.Before(expires) {
			expires = p.ExpiresAt
		}
		if c.ExpiresAt.Before(expires) {
			expires = c.ExpiresAt
		}
		if !historyNow().Before(expires) {
			return ErrCopilotHistoryConflict
		}
		historyTrackExpiry(tx, expires)
		now := historyNow()
		out = &model.CopilotExecutionIntent{IntentID: uuid.NewString(), DecisionID: uuid.NewString(), OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, PipelineID: c.PipelineID, RunID: c.RunID, ConversationID: c.ConversationID, PlanID: p.PlanID, PlanVersion: p.Version, PlanDigest: p.PlanDigest, RequestID: in.RequestID, JobRunID: in.Review.RetryJobRunID, ApprovalID: in.Review.ApprovalID, EvidenceHash: in.Review.EvidenceHash, EvidenceDigest: digest, State: "prepared", Version: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: expires}
		out.IntentDigest = executionDigest(out)
		if err = tx.Create(out).Error; err != nil {
			return ErrCopilotHistoryConflict
		}
		created = true
		return nil
	})
	if err != nil {
		return nil, false, executionError(err)
	}
	return out, created, nil
}
func (r *Repositories) CopilotExecutionGet(ctx context.Context, owner, project, id string, authorize CopilotHistoryAuthorize) (*model.CopilotExecutionIntent, error) {
	db, err := r.historyDB(ctx)
	if err != nil {
		return nil, executionError(err)
	}
	hint, err := executionFind(db, owner, project, id, false)
	if err != nil {
		return nil, executionError(err)
	}
	var out *model.CopilotExecutionIntent
	err = db.Transaction(func(tx *gorm.DB) error {
		current, err := historyAccess(ctx, tx, executionScope(hint), authorize)
		if err != nil {
			return err
		}
		out, err = executionFind(tx, owner, project, id, false)
		if err != nil {
			return err
		}
		// Primary snapshot reads observe the atomic intent+decision commit together.
		// No vote locks are acquired after an intent lock.
		if out.State == "submitted" {
			var votes []model.ApprovalDecision
			if err = tx.Select(approvalDecisionColumns).Where("decision_id = ? AND approval_id = ? AND user_id = ?", out.DecisionID, out.ApprovalID, out.OwnerUserID).Limit(2).Find(&votes).Error; err != nil {
				return err
			}
			if len(votes) != 1 || votes[0].Decision != model.ApprovalDecisionApprove || out.SubmittedAt == nil {
				return ErrCopilotHistoryConflict
			}
		}
		return current(ctx)
	})
	if err != nil {
		return nil, executionError(err)
	}
	return out, nil
}
func executionMutation(i *model.CopilotExecutionIntent, in CopilotExecutionMutationInput) error {
	if in.Version == 0 || in.IntentDigest != i.IntentDigest {
		return ErrCopilotHistoryConflict
	}
	return nil
}
func (r *Repositories) CopilotExecutionCancel(ctx context.Context, in CopilotExecutionMutationInput, authorize CopilotHistoryAuthorize) (*model.CopilotExecutionIntent, error) {
	db, err := r.historyDB(ctx)
	if err != nil {
		return nil, executionError(err)
	}
	hint, err := executionFind(db, in.OwnerUserID, in.ProjectID, in.IntentID, false)
	if err != nil {
		return nil, executionError(err)
	}
	var out *model.CopilotExecutionIntent
	err = db.Transaction(func(tx *gorm.DB) error {
		current, err := historyAccess(ctx, tx, executionScope(hint), authorize)
		if err != nil {
			return err
		}
		out, err = executionFind(tx, in.OwnerUserID, in.ProjectID, in.IntentID, true)
		if err != nil {
			return err
		}
		if err = executionMutation(out, in); err != nil {
			return err
		}
		if out.State == "cancelled" {
			return current(ctx)
		}
		if out.State != "prepared" || out.Version != in.Version {
			return ErrCopilotHistoryConflict
		}
		now := historyNow()
		res := tx.Model(&model.CopilotExecutionIntent{}).Where("id = ? AND state = ? AND version = ?", out.ID, "prepared", in.Version).Updates(map[string]any{"state": "cancelled", "version": in.Version + 1, "cancelled_at": now, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrCopilotHistoryConflict
		}
		out.State = "cancelled"
		out.Version++
		out.CancelledAt = &now
		out.UpdatedAt = now
		return current(ctx)
	})
	if err != nil {
		return nil, executionError(err)
	}
	return out, nil
}

// Confirm never locks history. Job/domain/authority locks precede the intent
// lock. The final CAS and the stable vote insertion share one primary commit.
// Current read authorization also locks pipeline/project/run. Its pipeline-first
// order can contend with this domain path's run-first locks. MySQL may abort a
// deadlock victim; callers inspect the durable primary receipt, never blindly
// retry confirmation or infer that a returned error means no commit occurred.
func (r *Repositories) CopilotExecutionConfirm(ctx context.Context, in CopilotExecutionMutationInput, authorize CopilotHistoryAuthorize) (*model.CopilotExecutionIntent, error) {
	db, err := r.historyDB(ctx)
	if err != nil {
		return nil, executionError(err)
	}
	hint, err := executionFind(db, in.OwnerUserID, in.ProjectID, in.IntentID, false)
	if err != nil {
		return nil, executionError(err)
	}
	if err = executionMutation(hint, in); err != nil {
		return nil, err
	}
	var initial model.ApprovalRequest
	if err = db.Select(approvalColumns).Where("approval_id = ?", hint.ApprovalID).First(&initial).Error; err != nil {
		return nil, ErrCopilotHistoryUnavailable
	}
	var out *model.CopilotExecutionIntent
	err = executionTransaction(ctx, db, executionApprovalJobID(&initial), func(tx *gorm.DB) error {
		job, err := lockRetryApprovalJob(ctx, tx, &initial)
		if err != nil {
			return err
		}
		a, err := lockApproval(tx, hint.ApprovalID)
		if err != nil {
			return err
		}
		e, err := retryapproval.ParseEvidence(a.EvidenceJSON)
		if err != nil {
			return ErrCopilotHistoryConflict
		}
		digest, err := e.Digest()
		if err != nil || digest != hint.EvidenceDigest || a.EvidenceHash != hint.EvidenceHash || a.ApprovalID != hint.ApprovalID || a.RetryJobRunID == nil || *a.RetryJobRunID != hint.JobRunID || job.JobRunID != hint.JobRunID || e.Context.ProjectID != hint.ProjectID || e.Context.PipelineID != hint.PipelineID || e.Context.PipelineRunID != hint.RunID {
			return ErrCopilotHistoryConflict
		}
		var votes []model.ApprovalDecision
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(approvalDecisionColumns).Where("approval_id = ? AND user_id = ?", a.ApprovalID, hint.OwnerUserID).Limit(2).Find(&votes).Error; err != nil {
			return err
		}
		replay := len(votes) == 1 && votes[0].DecisionID == hint.DecisionID && votes[0].Decision == model.ApprovalDecisionApprove
		if len(votes) > 0 && !replay {
			return ErrCopilotHistoryConflict
		}
		if !replay {
			a, e, err = validateRetryApprovalBinding(ctx, tx, job)
			if err != nil {
				return err
			}
			if a == nil {
				return ErrCopilotHistoryConflict
			}
			if err = validateRetryApprovalDecision(ctx, tx, job, a, e, hint.OwnerUserID, model.ApprovalDecisionApprove); err != nil {
				return err
			}
		}
		current, err := historyAccess(ctx, tx, executionScope(hint), authorize)
		if err != nil {
			return err
		}
		// Last new row lock: cancellation can win until this point.
		out, err = executionFind(tx, in.OwnerUserID, in.ProjectID, in.IntentID, true)
		if err != nil {
			return err
		}
		if out.IntentDigest != hint.IntentDigest {
			return ErrCopilotHistoryConflict
		}
		if replay {
			if out.State != "submitted" || out.SubmittedAt == nil {
				return ErrCopilotHistoryConflict
			}
			return current(ctx)
		}
		if out.State != "prepared" || out.Version != in.Version || !historyNow().Before(out.ExpiresAt) {
			return ErrCopilotHistoryConflict
		}
		if err = writeRetryApprovalDecision(ctx, tx, job, a, out.OwnerUserID, model.ApprovalDecisionApprove, "", out.DecisionID); err != nil {
			return err
		}
		now := historyNow()
		res := tx.Model(&model.CopilotExecutionIntent{}).Where("id = ? AND state = ? AND version = ?", out.ID, "prepared", out.Version).Updates(map[string]any{"state": "submitted", "version": out.Version + 1, "submitted_at": now, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrCopilotHistoryConflict
		}
		if !historyNow().Before(out.ExpiresAt) || approvalDeadlinePassed(a) {
			return ErrCopilotHistoryConflict
		}
		if err = current(ctx); err != nil {
			return err
		}
		if err = checkRetryApprovalAuthorizationExpiry(ctx, tx); err != nil {
			return err
		}
		out.State = "submitted"
		out.Version++
		out.SubmittedAt = &now
		out.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, executionError(err)
	}
	return out, nil
}

type CopilotExecutionPage struct {
	List      []*model.CopilotExecutionIntent
	NextAfter uint64
}

// List uses an owner/project bounded scan; no inaccessible row contents/counts
// are exposed. The cursor is only an opaque pagination position.
func (r *Repositories) CopilotExecutionList(ctx context.Context, owner, project string, after uint64, limit int, authorize CopilotHistoryAuthorize) (*CopilotExecutionPage, error) {
	if !historyID(owner) || !historyID(project) || limit < 1 || limit > 20 {
		return nil, ErrCopilotHistoryInvalid
	}
	db, err := r.historyDB(ctx)
	if err != nil {
		return nil, executionError(err)
	}
	var rows []model.CopilotExecutionIntent
	err = db.Transaction(func(tx *gorm.DB) error {
		if authorize == nil {
			return ErrCopilotHistoryForbidden
		}
		var projects []model.Project
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("project_id", "org_id", "status", "is_enabled").Where("project_id = ?", project).Limit(2).Find(&projects).Error; e != nil {
			return e
		}
		if len(projects) != 1 || projects[0].ProjectID != project || projects[0].IsEnabled != 1 || projects[0].Status != model.ProjectStatusActive {
			return ErrCopilotHistoryForbidden
		}
		view, current := NewScopedAuthorizationView(tx)
		actor, e := view.User.(IExecutionActorReader).GetExecutionActor(ctx, owner)
		if e != nil || actor.UserID != owner || actor.IsEnabled != 1 {
			return ErrCopilotHistoryForbidden
		}
		if e = authorize(ctx, view, owner, &projects[0], ""); e != nil {
			return ErrCopilotHistoryForbidden
		}
		if e = tx.Where("owner_user_id = ? AND project_id = ? AND id > ?", owner, project, after).Order("id ASC").Limit(limit).Find(&rows).Error; e != nil {
			return e
		}
		return current(ctx)
	})
	if err != nil {
		return nil, executionError(err)
	}
	page := &CopilotExecutionPage{List: []*model.CopilotExecutionIntent{}}
	for _, row := range rows {
		v, err := r.CopilotExecutionGet(ctx, owner, project, row.IntentID, authorize)
		if errors.Is(err, ErrCopilotHistoryForbidden) || errors.Is(err, ErrCopilotHistoryNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		page.List = append(page.List, v)
	}
	if len(rows) == limit {
		page.NextAfter = rows[len(rows)-1].ID
	}
	return page, nil
}

// CopilotExecutionGeneration reads a primary snapshot of only the currently
// linked generation. It never treats a vote or receipt as execution permission.
// The caller must validate any admission receipt before displaying its presence.
func (r *Repositories) CopilotExecutionGeneration(ctx context.Context, owner, project, id string, authorize CopilotHistoryAuthorize) (*model.JobRunGeneration, error) {
	db, err := r.historyDB(ctx)
	if err != nil {
		return nil, executionError(err)
	}
	hint, err := executionFind(db, owner, project, id, false)
	if err != nil {
		return nil, executionError(err)
	}
	var out *model.JobRunGeneration
	err = db.Transaction(func(tx *gorm.DB) error {
		current, err := historyAccess(ctx, tx, executionScope(hint), authorize)
		if err != nil {
			return err
		}
		intent, err := executionFind(tx, owner, project, id, false)
		if err != nil {
			return err
		}
		var job model.JobRun
		if err = tx.Select("job_run_id", "pipeline_id", "pipeline_run_id", "retry_approval_id", "retry_approval_evidence_hash", "current_generation_id").Where("job_run_id = ?", intent.JobRunID).First(&job).Error; err != nil {
			return err
		}
		if job.PipelineID != intent.PipelineID || job.PipelineRunID != intent.RunID || job.RetryApprovalID != intent.ApprovalID || job.RetryApprovalEvidenceHash != intent.EvidenceHash {
			return ErrCopilotHistoryConflict
		}
		if job.CurrentGenerationID != "" {
			var rows []model.JobRunGeneration
			if err = tx.Where("generation_id = ? AND job_run_id = ?", job.CurrentGenerationID, job.JobRunID).Limit(2).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) != 1 {
				return ErrCopilotHistoryConflict
			}
			out = &rows[0]
		}
		return current(ctx)
	})
	if err != nil {
		return nil, executionError(err)
	}
	return out, nil
}
