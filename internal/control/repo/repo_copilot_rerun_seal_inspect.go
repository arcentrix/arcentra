// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"reflect"
)

// InspectSeal reads the committed immutable receipt on primary after an unknown
// outcome. It does not disclose ciphertext/plaintext or renew/reacquire a lease.
func (r *Repositories) CopilotRerunInspectSeal(ctx context.Context, owner, project, intent string, authorize CopilotRerunAuthorize) (*CopilotRerunSealReceipt, error) {
	if r == nil || r.database == nil || authorize == nil || !historyID(owner) || !historyID(project) || !rerunUUID(intent) {
		return nil, ErrCopilotRerunInvalid
	}
	db := r.database.Database().Clauses(database.Write())
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		return nil, ErrCopilotRerunInvalid
	}
	var hints []model.CopilotRerunIntent
	if err := db.WithContext(ctx).Where("intent_id = ? AND owner_user_id = ? AND project_id = ?", intent, owner, project).Limit(2).Find(&hints).Error; err != nil {
		return nil, rerunError(err)
	}
	if len(hints) != 1 || hints[0].IntentID != intent || hints[0].OwnerUserID != owner || hints[0].ProjectID != project {
		return nil, ErrCopilotRerunForbidden
	}
	hint := hints[0]
	var receipt *CopilotRerunSealReceipt
	err := executionTransaction(ctx, db, hint.NewJobRunID, func(tx *gorm.DB) error {
		gate, err := lockCopilotRerunTargetGateMode(ctx, tx, &hint, true)
		if err != nil {
			return err
		}
		scope, current, err := lockCopilotRerunSealScope(ctx, tx, &hint, "inspect", authorize)
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
		lock := func() *gorm.DB { return tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}) }
		var schedule model.CopilotRerunSchedule
		if err := lock().Where("schedule_id = ?", hint.ScheduleID).First(&schedule).Error; err != nil {
			return err
		}
		var saved model.CopilotRerunIntent
		if err := lock().Where("intent_id = ?", hint.IntentID).First(&saved).Error; err != nil {
			return err
		}
		if !reflect.DeepEqual(saved, hint) || saved.IntentDigest != rerunIntentDigest(&saved) || saved.State != CopilotRerunConfirmed {
			return ErrCopilotRerunConflict
		}
		if err := inspectCopilotRerunTuple(ctx, tx, &saved, &ids, &snapshot, &evidence); err != nil {
			return err
		}
		receipt = &CopilotRerunSealReceipt{State: schedule.State, GateVersion: gate.Version, ScheduleVersion: schedule.Version}
		if gate.State == "claimed" {
			if err := validateCopilotRerunClaimedReceipt(scope, gate, &schedule); err != nil {
				return err
			}
		} else if schedule.State == CopilotRerunScheduleCancelled {
			if err := validateCopilotRerunClosedTarget(ctx, tx, scope, gate, &schedule, &ids); err != nil {
				return err
			}
		} else if gate.State != "unclaimed" {
			return ErrCopilotRerunConflict
		}
		if schedule.State == CopilotRerunSchedulePending || (schedule.State == CopilotRerunScheduleCancelled && schedule.SealID == nil) {
			if schedule.SealID != nil || gate.SealID != "" || gate.AgentID != "" || scope.Job.AgentID != "" || schedule.AgentID != "" || schedule.SealDigest != "" || len(schedule.PayloadCiphertext) != 0 {
				return ErrCopilotRerunConflict
			}
		} else if (schedule.State == CopilotRerunScheduleSealed || schedule.State == CopilotRerunScheduleCancelled || schedule.State == CopilotRerunScheduleClaimed || schedule.State == CopilotRerunScheduleCompleted) && schedule.SealID != nil {
			b := CopilotRerunSealBinding{Version: 1, SealID: *schedule.SealID, ScheduleID: schedule.ScheduleID, RunID: schedule.RunID, JobRunID: schedule.JobRunID, AgentID: schedule.AgentID, PayloadDigest: schedule.PayloadDigest, CipherVersion: schedule.CipherVersion, CiphertextDigest: schedule.CiphertextDigest, SecretVersionsDigest: schedule.SecretVersionsDigest, ExpiresAtUnixMilli: schedule.ExpiresAt.UnixMilli()}
			if validateCopilotRerunSealIdentity(b.SealID, &saved, &evidence, &ids, b.AgentID) != nil || b.SecretVersionsDigest != CopilotRerunEmptySecretVersionsDigest() || ValidateCopilotRerunSealedBinding(&schedule, &b) != nil || gate.SealID != b.SealID || gate.SealDigest != schedule.SealDigest || gate.AgentID != b.AgentID || scope.Job.AgentID != b.AgentID {
				return ErrCopilotRerunConflict
			}
			if gate.State != "claimed" {
				if err := validateCopilotRerunStoredDispatch(ctx, tx, scope, &schedule); err != nil {
					return err
				}
			} else if err := validateCopilotRerunClaimDispatch(scope, &ids); err != nil {
				return err
			}
			receipt.Binding = b
		} else {
			return ErrCopilotRerunConflict
		}
		return current(ctx)
	})
	if err != nil {
		return nil, rerunError(err)
	}
	return receipt, nil
}

func validateCopilotRerunStoredDispatch(ctx context.Context, tx *gorm.DB, scope *CopilotRerunScope, schedule *model.CopilotRerunSchedule) error {
	j := scope.Job
	closed := schedule.State == CopilotRerunScheduleCancelled
	if !closed && (scope.Run.Status != model.PipelineStatusPending || scope.Run.EndTime != nil || j.Status != model.JobRunStatusPending || j.EndTime != nil || schedule.CancelRequested || schedule.CompletedAt != nil) {
		return ErrCopilotRerunConflict
	}
	if scope.Run.Env != "{}" || scope.Run.StartTime != nil || j.ClaimID != "" || j.StartTime != nil || j.SourceResolvedCommit != "" || j.SourceProvenanceState != "unknown" || j.InputManifest != "" || j.InputManifestDigest != "" || j.InputManifestState != "unknown" || j.RetryApprovalID != "" || j.RetryApprovalEvidenceHash != "" || j.CurrentGenerationID != "" || j.CurrentGeneration != 0 || j.TerminalReportDigest != "" || j.CancellationClaimID != "" || j.Env != "{}" || j.ArtifactReferences != "{}" || j.ArtifactURIs != "{}" || schedule.ClaimedID != "" || schedule.ClaimedAt != nil || schedule.SealedAt == nil {
		return ErrCopilotRerunConflict
	}
	runtime := &taskqueue.RuntimeRequest{Type: j.RuntimeType, Image: j.RuntimeImage, Resources: taskqueue.RuntimeResources{CPU: j.RuntimeCPU, Memory: j.RuntimeMemory}}
	digest, err := taskqueue.RuntimeBindingDigest(j.PipelineID, j.PipelineRunID, j.JobRunID, j.AgentID, runtime)
	if err != nil || digest != j.RuntimeBindingDigest {
		return ErrCopilotRerunConflict
	}
	plan, err := taskqueue.ParseJobRetryPlan(j.RetryPlan)
	if err != nil {
		return ErrCopilotRerunConflict
	}
	digest, err = plan.Digest(j.PipelineID, j.PipelineRunID, j.JobRunID, j.AgentID, j.RuntimeBindingDigest)
	if err != nil || digest != j.RetryPlanDigest {
		return ErrCopilotRerunConflict
	}
	expected, _ := taskqueue.NewJobInputManifest("", nil, nil)
	canonical, _ := expected.Canonical()
	if j.InputExpectation != canonical {
		return ErrCopilotRerunConflict
	}
	var steps []model.StepRun
	count := 0
	for _, step := range plan.Steps {
		count += len(step.AttemptRunIDs)
	}
	if err := tx.WithContext(ctx).Where("job_run_id = ?", j.JobRunID).Limit(count + 1).Find(&steps).Error; err != nil {
		return err
	}
	if len(steps) != count {
		return ErrCopilotRerunConflict
	}
	for _, step := range steps {
		if !closed && (step.Status != model.JobRunStatusPending || step.EndTime != nil) {
			return ErrCopilotRerunConflict
		}
		if step.AgentID != j.AgentID || step.ClaimID != "" || step.StartTime != nil || step.Args != "{}" || step.Env != "{}" || step.Secrets != "{}" || step.TerminalReportDigest != "" {
			return ErrCopilotRerunConflict
		}
	}
	return nil
}

// Closed receipts retain immutable seal evidence but never admit execution.
func validateCopilotRerunClosedTarget(ctx context.Context, tx *gorm.DB, scope *CopilotRerunScope, gate *model.CopilotRerunAdmissionGate, schedule *model.CopilotRerunSchedule, ids *CopilotRerunExecutionIdentities) error {
	j, run := scope.Job, scope.Run
	if gate.State != "closed" || gate.ClosedAt == nil || schedule.State != CopilotRerunScheduleCancelled || !schedule.CancelRequested || schedule.CompletedAt == nil || !schedule.CompletedAt.Equal(*gate.ClosedAt) || schedule.ClaimedID != "" || schedule.ClaimedAt != nil || j.Status != model.JobRunStatusCancelled || run.Status != model.PipelineStatusCancelled || j.EndTime == nil || run.EndTime == nil || !j.EndTime.Equal(*gate.ClosedAt) || !run.EndTime.Equal(*gate.ClosedAt) || j.ClaimID != "" || j.StartTime != nil || run.StartTime != nil || run.CompletedJobs != 1 || run.FailedJobs != 0 || run.RunningJobs != 0 || j.CompletedSteps != j.TotalSteps || j.FailedSteps != 0 {
		return ErrCopilotRerunConflict
	}
	var steps []model.StepRun
	count := 0
	for _, step := range ids.Steps {
		count += len(step.Attempts)
	}
	if err := tx.WithContext(ctx).Where("job_run_id = ?", j.JobRunID).Limit(count + 1).Find(&steps).Error; err != nil {
		return err
	}
	if len(steps) != count {
		return ErrCopilotRerunConflict
	}
	for _, step := range steps {
		if step.Status != model.JobRunStatusCancelled || step.ClaimID != "" || step.StartTime != nil || step.EndTime == nil || !step.EndTime.Equal(*gate.ClosedAt) {
			return ErrCopilotRerunConflict
		}
	}
	return nil
}
