// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const CopilotRerunExpiredBeforeClaimReason = "rerun admission expired before claim"

type CopilotRerunExpiryReceipt struct {
	IntentID, RunID, JobRunID, GateID, ScheduleID string
	GateVersion, ScheduleVersion                  uint64
	ExpiredAt                                     time.Time
}

// Internal deadline enforcement only. A locked unclaimed Gate, exact committed
// deadline and absence of execution evidence are required. Queue ACK/timeout and
// lease expiry are never evidence of nonexecution. Claimed Gates cannot enter.
func (r *Repositories) CopilotRerunExpireUnclaimed(ctx context.Context, scheduleID string) (*CopilotRerunExpiryReceipt, error) {
	if r == nil || r.database == nil || !rerunUUID(scheduleID) {
		return nil, ErrCopilotRerunInvalid
	}
	db := r.database.Database().Clauses(database.Write())
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		return nil, ErrCopilotRerunInvalid
	}
	var hints []model.CopilotRerunIntent
	if err := db.WithContext(ctx).Where("schedule_id = ?", scheduleID).Limit(2).Find(&hints).Error; err != nil {
		return nil, rerunError(err)
	}
	if len(hints) != 1 {
		return nil, ErrCopilotRerunForbidden
	}
	hint := hints[0]
	if hint.ScheduleID != scheduleID || hint.State != CopilotRerunConfirmed {
		return nil, ErrCopilotRerunConflict
	}

	var receipt *CopilotRerunExpiryReceipt
	err := executionTransaction(ctx, db, hint.NewJobRunID, func(tx *gorm.DB) error {
		gate, err := lockCopilotRerunTargetGateMode(ctx, tx, &hint, true)
		if err != nil {
			return err
		}
		if gate.State == "claimed" || gate.ClaimID != "" || gate.ClaimedAt != nil {
			return ErrCopilotRerunConflict
		}
		// Deadline cleanup is not a human cancellation request. Identity prefix
		// locks remain mandatory, but revoked owners cannot prevent expiry.
		job, err := lockExecutionJob(ctx, tx, hint.NewJobRunID, executionLockRerunSeal)
		if err != nil {
			return err
		}
		// Prefix projections establish identity locks; reload the complete already-held Job.
		var completeJob model.JobRun
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_run_id = ?", job.JobRunID).First(&completeJob).Error; err != nil {
			return err
		}
		job = &completeJob
		var targetRun model.PipelineRun
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("run_id = ?", hint.NewRunID).First(&targetRun).Error; err != nil {
			return err
		}
		scope := &CopilotRerunScope{Job: job, Run: &targetRun}
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

		// Current bounded existence reads are serialized by the parent Job and
		// Step locks. Orphan durable output/admission evidence forbids closure.
		for _, probe := range []struct {
			model any
			field string
			value any
		}{
			{&model.JobRunGeneration{}, "job_run_id", hint.NewJobRunID},
			{&model.StepRunArtifact{}, "job_run_id", hint.NewJobRunID},
			{&model.ApprovalRequest{}, "retry_job_run_id", hint.NewJobRunID},
		} {
			var rows []struct{ ID uint64 }
			if err := lock().Model(probe.model).Select("id").Where(probe.field+" = ?", probe.value).Limit(1).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) != 0 {
				return ErrCopilotRerunConflict
			}
		}

		// Both artifact projections require exact indexed Job identity. Even a
		// malformed Run backlink must not hide durable output evidence.
		var published []struct{ ID uint64 }
		if err := lock().Model(&model.ProjectArtifact{}).Select("id").Where("job_run_id = ?", hint.NewJobRunID).Limit(1).Find(&published).Error; err != nil {
			return err
		}
		if len(published) != 0 {
			return ErrCopilotRerunConflict
		}
		for _, step := range steps {
			var rows []struct{ ID uint64 }
			if err := lock().Model(&model.StepRunLog{}).Select("id").Where("step_run_id = ?", step.StepRunID).Limit(1).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) != 0 {
				return ErrCopilotRerunConflict
			}
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
		if schedule.ScheduleID != scheduleID || !schedule.ExpiresAt.Equal(saved.ExpiresAt) || time.Now().Before(schedule.ExpiresAt) || gate.Version >= math.MaxInt64 || schedule.Version >= math.MaxInt64 {
			return ErrCopilotRerunConflict
		}
		j, run := scope.Job, scope.Run
		if schedule.ClaimedID != "" || schedule.ClaimedAt != nil {
			return ErrCopilotRerunConflict
		}

		if j.RetryApprovalID != "" || j.RetryApprovalEvidenceHash != "" || j.SourceResolvedCommit != "" || j.InputManifest != "" || j.InputManifestDigest != "" || j.CurrentGenerationID != "" || j.CurrentGeneration != 0 || j.TerminalReportDigest != "" || j.CancellationClaimID != "" || j.SourceProvenanceState != "unknown" || j.InputManifestState != "unknown" || j.ArtifactStorageID != "" || j.ArtifactStorageFingerprint != "" || j.ArtifactStorageType != "" || j.ArtifactReferences != "{}" || j.ArtifactURIs != "{}" || j.Env != "{}" || run.Env != "{}" || j.Duration != 0 || j.Workspace != "" {
			return ErrCopilotRerunConflict
		}
		for _, step := range steps {
			if step.ClaimID != "" || step.CancellationClaimID != "" || step.GenerationID != "" || step.GenerationBindingDigest != "" || step.TerminalReportDigest != "" || step.AttemptAdmissionRequest != "" || step.AttemptAdmissionRequestDigest != "" || step.AttemptAdmission != "" || step.ExitCode != nil || step.ErrorMessage != "" || step.Duration != 0 || step.Workspace != "" || step.RetryableShellExit || step.Args != "{}" || step.Env != "{}" || step.Secrets != "{}" {
				return ErrCopilotRerunConflict
			}
		}
		if gate.State == "closed" {
			if j.ErrorMessage != CopilotRerunExpiredBeforeClaimReason || schedule.State != CopilotRerunScheduleCancelled || !schedule.CancelRequested || schedule.CompletedAt == nil || gate.ClosedAt == nil || !schedule.CompletedAt.Equal(*gate.ClosedAt) || j.Status != model.JobRunStatusCancelled || run.Status != model.PipelineStatusCancelled || j.EndTime == nil || run.EndTime == nil || !j.EndTime.Equal(*gate.ClosedAt) || !run.EndTime.Equal(*gate.ClosedAt) || j.ClaimID != "" || j.StartTime != nil || run.StartTime != nil || run.CompletedJobs != 1 || run.FailedJobs != 0 || run.RunningJobs != 0 || j.CompletedSteps != len(layout.Steps) || j.FailedSteps != 0 {
				return ErrCopilotRerunConflict
			}
			for _, step := range steps {
				if step.Status != model.JobRunStatusCancelled || step.ClaimID != "" || step.StartTime != nil || step.EndTime == nil || !step.EndTime.Equal(*gate.ClosedAt) {
					return ErrCopilotRerunConflict
				}
			}
			receipt = &CopilotRerunExpiryReceipt{saved.IntentID, ids.RunID, ids.JobRunID, ids.GateID, ids.ScheduleID, gate.Version, schedule.Version, *gate.ClosedAt}
			return nil
		}
		if (schedule.State != CopilotRerunSchedulePending && schedule.State != CopilotRerunScheduleSealed) || schedule.CancelRequested || schedule.ClaimedID != "" || schedule.ClaimedAt != nil || j.ClaimID != "" || j.Status != model.JobRunStatusPending || j.StartTime != nil || j.EndTime != nil || run.Status != model.PipelineStatusPending || run.StartTime != nil || run.EndTime != nil || j.RetryApprovalID != "" || j.SourceResolvedCommit != "" || j.InputManifest != "" || j.CurrentGenerationID != "" || j.CurrentGeneration != 0 || j.TerminalReportDigest != "" || j.CancellationClaimID != "" || j.InputManifestDigest != "" || j.SourceProvenanceState != "unknown" || j.InputManifestState != "unknown" || j.ArtifactStorageID != "" || j.ArtifactStorageFingerprint != "" || j.ArtifactStorageType != "" || j.Env != "{}" || run.Env != "{}" || j.RetryApprovalEvidenceHash != "" || j.ArtifactReferences != "{}" || j.ArtifactURIs != "{}" || j.CompletedSteps != 0 || j.FailedSteps != 0 || j.Duration != 0 || j.Workspace != "" || j.ErrorMessage != "" || run.CompletedJobs != 0 || run.FailedJobs != 0 || run.RunningJobs != 0 {
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
		now := time.Now().UTC().Truncate(time.Millisecond)
		for _, step := range steps {
			if step.Status != model.JobRunStatusPending || step.ClaimID != "" || step.StartTime != nil || step.EndTime != nil || step.TerminalReportDigest != "" || step.Args != "{}" || step.Env != "{}" || step.Secrets != "{}" || step.CancellationClaimID != "" || step.GenerationID != "" || step.GenerationBindingDigest != "" || step.AttemptAdmissionRequest != "" || step.AttemptAdmissionRequestDigest != "" || step.AttemptAdmission != "" || step.ExitCode != nil || step.ErrorMessage != "" || step.Duration != 0 || step.Workspace != "" || step.RetryableShellExit {
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
			{&model.JobRun{}, "id = ? AND job_run_id = ? AND status = ? AND claim_id = ?", []any{j.ID, j.JobRunID, model.JobRunStatusPending, ""}, map[string]any{"status": model.JobRunStatusCancelled, "end_time": now, "completed_steps": len(layout.Steps), "failed_steps": 0, "error_message": CopilotRerunExpiredBeforeClaimReason}},
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
		receipt = &CopilotRerunExpiryReceipt{saved.IntentID, ids.RunID, ids.JobRunID, ids.GateID, ids.ScheduleID, gate.Version + 1, schedule.Version + 1, now}
		return nil
	})
	if err != nil {
		return nil, rerunError(err)
	}
	return receipt, nil
}
