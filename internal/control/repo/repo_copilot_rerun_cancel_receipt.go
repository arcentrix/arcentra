// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// InspectCopilotRerunCancellation 只读恢复当前已授权的固定取消回执，不补写终态
// A missing/changed receipt remains unknown; this method never performs a
// cancellation, finalization, approval closure, or notification.
func (r *Repositories) InspectCopilotRerunCancellation(ctx context.Context, actor string, hint *CopilotRerunCancellationLocator, authorize CopilotRerunAuthorize) (*CopilotRerunTerminalReceipt, error) {
	if r == nil || r.database == nil || hint == nil || !hint.Protected || !historyID(actor) || authorize == nil {
		return nil, ErrCopilotRerunInvalid
	}
	if !hint.Claimed {
		receipt, err := r.copilotRerunCancelUnclaimedAs(ctx, actor, hint.Unclaimed, authorize, true)
		if err != nil {
			return nil, err
		}
		if receipt.IntentID != hint.IntentID || receipt.ScheduleID != hint.ScheduleID || receipt.RunID != hint.RunID || receipt.JobRunID != hint.JobRunID {
			return nil, ErrCopilotRerunConflict
		}
		return &CopilotRerunTerminalReceipt{IntentID: receipt.IntentID, ScheduleID: receipt.ScheduleID, RunID: receipt.RunID, JobRunID: receipt.JobRunID, RunStatus: model.PipelineStatusCancelled, JobStatus: model.JobRunStatusCancelled, CompletedAtUnixMilli: receipt.CancelledAt.UnixMilli()}, nil
	}
	if hint.Identity.PipelineID != hint.PipelineID || hint.Identity.PipelineRunID != hint.RunID || hint.Identity.JobRunID != hint.JobRunID {
		return nil, ErrCopilotRerunConflict
	}
	var result *CopilotRerunTerminalReceipt
	err := executionTransaction(ctx, r.database.Database(), hint.JobRunID, func(tx *gorm.DB) error {
		rows, err := loadCopilotRerunTerminalRows(ctx, tx, hint.Identity, executionLockRerunPostCancel)
		if err != nil {
			return err
		}
		if rows.intent.IntentID != hint.IntentID || rows.schedule.ScheduleID != hint.ScheduleID {
			return ErrCopilotRerunConflict
		}
		scope := &CopilotRerunScope{Job: rows.job, Run: &rows.run, Pipeline: &model.Pipeline{}, Project: &model.Project{}, JobCount: 1}
		lock := func() *gorm.DB { return tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}) }
		if err := lock().Where("pipeline_id = ?", hint.PipelineID).First(scope.Pipeline).Error; err != nil {
			return err
		}
		if scope.Pipeline.PipelineID != hint.PipelineID || scope.Pipeline.ProjectID != rows.run.ProjectID {
			return ErrCopilotRerunConflict
		}
		if err := lock().Where("project_id = ?", rows.run.ProjectID).First(scope.Project).Error; err != nil {
			return err
		}
		if scope.Project.ProjectID != rows.run.ProjectID {
			return ErrCopilotRerunConflict
		}
		view, current := NewScopedAuthorizationView(tx)
		user, err := view.User.(IInboxActorReader).GetInboxActor(ctx, actor)
		if err != nil || user == nil || user.UserID != actor || user.IsEnabled != 1 || authorize(ctx, view, scope, actor, "cancel") != nil {
			return ErrCopilotRerunForbidden
		}
		if err := lockCopilotRerunTerminalSteps(ctx, tx, rows); err != nil {
			return err
		}
		if err := finishCopilotRerunOperation(ctx, tx, rows.job, rows.intent, rows.schedule, executionLockRerunPostCancel); err != nil {
			return err
		}
		status, failed, err := validateCopilotRerunTerminalRows(rows)
		if err != nil || status != model.PipelineStatusCancelled || rows.job.CancellationClaimID != hint.Identity.ClaimID || rows.run.Status != status || rows.run.CompletedJobs != 1 || rows.run.RunningJobs != 0 || rows.run.FailedJobs != 0 || rows.job.FailedSteps != failed || rows.job.CompletedSteps != len(rows.layout.Steps) || rows.run.EndTime == nil || rows.job.EndTime == nil || !rows.run.EndTime.Equal(*rows.job.EndTime) || rows.schedule.State != CopilotRerunScheduleCancelled || !rows.schedule.CancelRequested || rows.schedule.CompletedAt == nil || !rows.schedule.CompletedAt.Equal(*rows.job.EndTime) {
			return ErrCopilotRerunConflict
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
