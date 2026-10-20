// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CopilotRerunCancelClaimed requires the authenticated caller's current scoped
// cancellation permission. The committed Gate and original claim stay immutable.
func (r *Repositories) CopilotRerunCancelClaimed(ctx context.Context, actor string, identity taskqueue.JobInputIdentity, authorize CopilotRerunAuthorize) (*CopilotRerunTerminalReceipt, error) {
	if !historyID(actor) || authorize == nil {
		return nil, ErrCopilotRerunForbidden
	}
	var result *CopilotRerunTerminalReceipt
	err := executionTransaction(ctx, r.database.Database(), identity.JobRunID, func(tx *gorm.DB) error {
		rows, err := loadCopilotRerunTerminalRows(ctx, tx, identity, executionLockRerunPostCancel)
		if err != nil {
			return err
		}
		scope := &CopilotRerunScope{Job: rows.job, Run: &rows.run, Pipeline: &model.Pipeline{}, Project: &model.Project{}, JobCount: 1}
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("pipeline_id = ?", identity.PipelineID).First(scope.Pipeline).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("project_id = ?", rows.intent.ProjectID).First(scope.Project).Error; err != nil {
			return err
		}
		if scope.Pipeline.PipelineID != identity.PipelineID || scope.Pipeline.ProjectID != rows.intent.ProjectID || scope.Project.ProjectID != rows.intent.ProjectID {
			return ErrCopilotRerunConflict
		}
		view, current := NewScopedAuthorizationView(tx)
		user, err := view.User.(IInboxActorReader).GetInboxActor(ctx, actor)
		if err != nil || user == nil || user.UserID != actor || user.IsEnabled != 1 {
			return ErrCopilotRerunForbidden
		}
		if err := authorize(ctx, view, scope, actor, "cancel"); err != nil {
			return ErrCopilotRerunForbidden
		}
		job := rows.job
		if model.IsJobRunTerminal(job.Status) && !(job.Status == model.JobRunStatusCancelled && job.CancellationClaimID == job.ClaimID) {
			return ErrCopilotRerunConflict
		}
		if err := cancelPendingRetryApproval(ctx, tx, job, "job execution canceled"); err != nil {
			return err
		}
		if err := lockCopilotRerunTerminalSteps(ctx, tx, rows); err != nil {
			return err
		}
		if err := finishCopilotRerunOperation(ctx, tx, job, rows.intent, rows.schedule, executionLockRerunPostCancel); err != nil {
			return err
		}
		if err := current(ctx); err != nil {
			return err
		}
		if !model.IsJobRunTerminal(job.Status) {
			now := time.Now().UTC().Truncate(time.Millisecond)
			if job.Status != model.JobRunStatusRunning || job.CancellationClaimID != "" || job.TerminalReportDigest != "" || rows.schedule.State != CopilotRerunScheduleClaimed {
				return ErrCopilotRerunConflict
			}
			if err := reportedStatusResult(reportedJobQuery(tx.WithContext(ctx).Model(&model.JobRun{}), job).Updates(map[string]any{"status": model.JobRunStatusCancelled, "cancellation_claim_id": job.ClaimID, "end_time": now, "updated_at": now})); err != nil {
				return err
			}
			for i := range rows.steps {
				step := &rows.steps[i]
				if model.IsJobRunTerminal(step.Status) {
					continue
				}
				if err := reportedStatusResult(tx.WithContext(ctx).Model(&model.StepRun{}).Where("step_run_id = ? AND job_run_id = ? AND pipeline_run_id = ? AND agent_id = ? AND status = ? AND claim_id = ?", step.StepRunID, job.JobRunID, job.PipelineRunID, job.AgentID, step.Status, step.ClaimID).Updates(map[string]any{"status": model.JobRunStatusCancelled, "cancellation_claim_id": job.ClaimID, "end_time": now, "updated_at": now})); err != nil {
					return err
				}
				step.Status = model.JobRunStatusCancelled
				step.CancellationClaimID = job.ClaimID
				step.EndTime = &now
			}
			job.Status = model.JobRunStatusCancelled
			job.CancellationClaimID = job.ClaimID
			job.EndTime = &now
		}
		status, failed, err := validateCopilotRerunTerminalRows(rows)
		if err != nil {
			return err
		}
		if err := writeCopilotRerunTerminalAggregate(ctx, tx, rows, status, failed); err != nil {
			return err
		}
		if err := current(ctx); err != nil {
			return err
		}
		result = rerunTerminalReceipt(rows, status)
		return nil
	})
	if err != nil {
		return nil, rerunError(err)
	}
	return result, nil
}
