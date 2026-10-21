// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"math"
	"reflect"
	"time"
)

type CopilotRerunTargetCancelInput struct {
	OwnerUserID, ProjectID, IntentID, ExpectedIntentDigest              string
	ExpectedIntentVersion, ExpectedGateVersion, ExpectedScheduleVersion uint64
}
type CopilotRerunTargetCancelReceipt struct {
	IntentID, RunID, JobRunID, GateID, ScheduleID string
	GateVersion, ScheduleVersion                  uint64
	CancelledAt                                   time.Time
}

// Cancellation applies only before a dedicated claim. Current cancel permission
// remains required; no generic cancellation helper is treated as authorization.
func (r *Repositories) CopilotRerunCancelUnclaimed(ctx context.Context, in CopilotRerunTargetCancelInput, authorize CopilotRerunAuthorize) (*CopilotRerunTargetCancelReceipt, error) {
	return r.copilotRerunCancelUnclaimedAs(ctx, in.OwnerUserID, in, authorize, false)
}

func (r *Repositories) copilotRerunCancelUnclaimedAs(ctx context.Context, actor string, in CopilotRerunTargetCancelInput, authorize CopilotRerunAuthorize, inspectOnly bool) (*CopilotRerunTargetCancelReceipt, error) {
	if r == nil || r.database == nil || authorize == nil || !historyID(actor) || !historyID(in.OwnerUserID) || !historyID(in.ProjectID) || !rerunUUID(in.IntentID) || in.ExpectedIntentVersion == 0 || in.ExpectedGateVersion == 0 || in.ExpectedScheduleVersion == 0 || in.ExpectedGateVersion >= math.MaxInt64 || in.ExpectedScheduleVersion >= math.MaxInt64 {
		return nil, ErrCopilotRerunInvalid
	}
	db := r.database.Database().Clauses(database.Write())
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		return nil, ErrCopilotRerunInvalid
	}
	var hints []model.CopilotRerunIntent
	if err := db.WithContext(ctx).Where("intent_id = ? AND owner_user_id = ? AND project_id = ?", in.IntentID, in.OwnerUserID, in.ProjectID).Limit(2).Find(&hints).Error; err != nil {
		return nil, rerunError(err)
	}
	if len(hints) != 1 {
		return nil, ErrCopilotRerunForbidden
	}
	hint := hints[0]
	if hint.IntentID != in.IntentID || hint.OwnerUserID != in.OwnerUserID || hint.ProjectID != in.ProjectID || hint.State != CopilotRerunConfirmed || hint.IntentDigest != in.ExpectedIntentDigest || hint.Version != in.ExpectedIntentVersion {
		return nil, ErrCopilotRerunConflict
	}
	var receipt *CopilotRerunTargetCancelReceipt
	err := executionTransaction(ctx, db, hint.NewJobRunID, func(tx *gorm.DB) error {
		gate, err := lockCopilotRerunTargetGateMode(ctx, tx, &hint, true)
		if err != nil {
			return err
		}
		if gate.State == "claimed" {
			// Release this Gate-first transaction before a caller may locate the
			// claimed branch. Never enter a postclaim domain while holding Gate.
			return ErrCopilotRerunConflict
		}
		scope, current, err := lockCopilotRerunTargetScopeAs(ctx, tx, &hint, actor, "cancel", authorize, executionLockRerunSeal)
		if err != nil {
			return err
		}
		var snapshot model.CopilotDefinitionSnapshot
		if err := tx.WithContext(ctx).Where("snapshot_id = ?", hint.SnapshotID).First(&snapshot).Error; err != nil {
			return err
		}
		if ValidateCopilotDefinitionSnapshot(&snapshot) != nil || snapshot.SnapshotID != hint.SnapshotID || snapshot.BindingDigest != hint.SnapshotDigest {
			return ErrCopilotRerunConflict
		}
		layout, err := ParseCopilotRerunLayout(snapshot.LayoutJSON)
		if err != nil {
			return err
		}
		var ids CopilotRerunExecutionIdentities
		var evidence CopilotRerunOriginalEvidence
		if json.Unmarshal([]byte(hint.StepIdentitiesJSON), &ids) != nil || ids.Validate(layout) != nil || json.Unmarshal([]byte(hint.OriginalEvidenceJSON), &evidence) != nil {
			return ErrCopilotRerunConflict
		}
		digest, err := evidence.Digest()
		if err != nil || digest != hint.OriginalEvidenceDigest {
			return ErrCopilotRerunConflict
		}
		count := 0
		for _, step := range ids.Steps {
			count += len(step.Attempts)
		}
		lock := func() *gorm.DB { return tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}) }
		var steps []model.StepRun
		if err := lock().Where("job_run_id = ?", hint.NewJobRunID).Order("id").Limit(count + 1).Find(&steps).Error; err != nil {
			return err
		}
		if len(steps) != count {
			return ErrCopilotRerunConflict
		}
		var schedule model.CopilotRerunSchedule
		if err := lock().Where("schedule_id = ?", hint.ScheduleID).First(&schedule).Error; err != nil {
			return err
		}
		var saved model.CopilotRerunIntent
		if err := lock().Where("intent_id = ?", hint.IntentID).First(&saved).Error; err != nil {
			return err
		}
		if !reflect.DeepEqual(saved, hint) || saved.IntentDigest != rerunIntentDigest(&saved) {
			return ErrCopilotRerunConflict
		}
		if err := inspectCopilotRerunTuple(ctx, tx, &saved, &ids, &snapshot, &evidence); err != nil {
			return err
		}
		j, run := scope.Job, scope.Run
		if schedule.ClaimedID != "" || schedule.ClaimedAt != nil {
			return ErrCopilotRerunConflict
		}
		if gate.State == "closed" {
			if gate.Version != in.ExpectedGateVersion+1 || schedule.Version != in.ExpectedScheduleVersion+1 || schedule.State != CopilotRerunScheduleCancelled || !schedule.CancelRequested || schedule.CompletedAt == nil || gate.ClosedAt == nil || !schedule.CompletedAt.Equal(*gate.ClosedAt) || j.Status != model.JobRunStatusCancelled || run.Status != model.PipelineStatusCancelled || j.EndTime == nil || run.EndTime == nil || !j.EndTime.Equal(*gate.ClosedAt) || !run.EndTime.Equal(*gate.ClosedAt) || j.ClaimID != "" || j.StartTime != nil || run.StartTime != nil || run.CompletedJobs != 1 || run.FailedJobs != 0 || run.RunningJobs != 0 || j.CompletedSteps != len(layout.Steps) || j.FailedSteps != 0 {
				return ErrCopilotRerunConflict
			}
			for _, step := range steps {
				if step.Status != model.JobRunStatusCancelled || step.ClaimID != "" || step.StartTime != nil || step.EndTime == nil || !step.EndTime.Equal(*gate.ClosedAt) {
					return ErrCopilotRerunConflict
				}
			}
			receipt = &CopilotRerunTargetCancelReceipt{saved.IntentID, ids.RunID, ids.JobRunID, ids.GateID, ids.ScheduleID, gate.Version, schedule.Version, *gate.ClosedAt}
			return current(ctx)
		}
		if gate.Version != in.ExpectedGateVersion || schedule.Version != in.ExpectedScheduleVersion || (schedule.State != CopilotRerunSchedulePending && schedule.State != CopilotRerunScheduleSealed) || schedule.CancelRequested || schedule.ClaimedID != "" || schedule.ClaimedAt != nil || j.ClaimID != "" || j.Status != model.JobRunStatusPending || j.StartTime != nil || j.EndTime != nil || run.Status != model.PipelineStatusPending || run.StartTime != nil || run.EndTime != nil || j.RetryApprovalID != "" || j.SourceResolvedCommit != "" || j.InputManifest != "" || j.CurrentGenerationID != "" {
			return ErrCopilotRerunConflict
		}
		if schedule.State == CopilotRerunScheduleSealed {
			if schedule.SealID == nil {
				return ErrCopilotRerunConflict
			}
			binding := CopilotRerunSealBinding{Version: 1, SealID: *schedule.SealID, ScheduleID: schedule.ScheduleID, RunID: schedule.RunID, JobRunID: schedule.JobRunID, AgentID: schedule.AgentID, PayloadDigest: schedule.PayloadDigest, CipherVersion: schedule.CipherVersion, CiphertextDigest: schedule.CiphertextDigest, SecretVersionsDigest: schedule.SecretVersionsDigest, ExpiresAtUnixMilli: schedule.ExpiresAt.UnixMilli()}
			if ValidateCopilotRerunSealedBinding(&schedule, &binding) != nil || gate.SealID != binding.SealID || gate.SealDigest != schedule.SealDigest || gate.AgentID != binding.AgentID || j.AgentID != binding.AgentID {
				return ErrCopilotRerunConflict
			}
			if err := validateCopilotRerunStoredDispatch(ctx, tx, scope, &schedule); err != nil {
				return err
			}
		} else if gate.SealID != "" || gate.AgentID != "" || gate.SealDigest != "" || schedule.SealID != nil || schedule.AgentID != "" || schedule.SealDigest != "" || len(schedule.PayloadCiphertext) != 0 || j.AgentID != "" || j.RetryPlan != "" || j.RuntimeBindingDigest != "" {
			return ErrCopilotRerunConflict
		}
		if inspectOnly {
			return ErrCopilotRerunConflict
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		for _, step := range steps {
			if step.Status != model.JobRunStatusPending || step.ClaimID != "" || step.StartTime != nil || step.EndTime != nil {
				return ErrCopilotRerunConflict
			}
			result := tx.Model(&model.StepRun{}).Where("id = ? AND step_run_id = ? AND job_run_id = ? AND status = ? AND claim_id = ?", step.ID, step.StepRunID, j.JobRunID, model.JobRunStatusPending, "").Updates(map[string]any{"status": model.JobRunStatusCancelled, "end_time": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrCopilotRerunConflict
			}
		}
		for _, update := range []struct {
			row       any
			predicate string
			args      []any
			values    map[string]any
		}{
			{&model.JobRun{}, "id = ? AND job_run_id = ? AND status = ? AND claim_id = ?", []any{j.ID, j.JobRunID, model.JobRunStatusPending, ""}, map[string]any{"status": model.JobRunStatusCancelled, "end_time": now, "completed_steps": len(layout.Steps), "failed_steps": 0}},
			{&model.PipelineRun{}, "id = ? AND run_id = ? AND status = ?", []any{run.ID, run.RunID, model.PipelineStatusPending}, map[string]any{"status": model.PipelineStatusCancelled, "end_time": now, "completed_jobs": 1, "failed_jobs": 0, "running_jobs": 0}},
			{&model.CopilotRerunAdmissionGate{}, "id = ? AND gate_id = ? AND version = ? AND state = ? AND claim_id = ?", []any{gate.ID, gate.GateID, gate.Version, "unclaimed", ""}, map[string]any{"state": "closed", "closed_at": now, "version": gate.Version + 1}},
			{&model.CopilotRerunSchedule{}, "id = ? AND schedule_id = ? AND version = ? AND state = ? AND claimed_id = ?", []any{schedule.ID, schedule.ScheduleID, schedule.Version, schedule.State, ""}, map[string]any{"state": CopilotRerunScheduleCancelled, "cancel_requested": true, "completed_at": now, "version": schedule.Version + 1}},
		} {
			result := tx.Model(update.row).Where(update.predicate, update.args...).Updates(update.values)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrCopilotRerunConflict
			}
		}
		if err := current(ctx); err != nil {
			return err
		}
		receipt = &CopilotRerunTargetCancelReceipt{saved.IntentID, ids.RunID, ids.JobRunID, ids.GateID, ids.ScheduleID, gate.Version + 1, schedule.Version + 1, now}
		return nil
	})
	if err != nil {
		return nil, rerunError(err)
	}
	return receipt, nil
}
