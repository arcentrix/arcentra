// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CopilotRerunInspectClaim resolves uncertain commit on primary. It requires
// current owner read permission, returns no nonce or executable bytes, and does
// not treat a durable historical claim as a grant to restart preparation.
func (r *Repositories) CopilotRerunInspectClaim(ctx context.Context, owner, project, intent string, authorize CopilotRerunAuthorize) (*CopilotRerunClaimReceipt, error) {
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
	var receipt *CopilotRerunClaimReceipt
	err := executionTransaction(ctx, db, hint.NewJobRunID, func(tx *gorm.DB) error {
		gate, err := lockCopilotRerunGateForOperation(ctx, tx, &hint, false, executionLockRerunClaim)
		if err != nil {
			return err
		}

		if gate.State != "claimed" || gate.ClaimedAt == nil {
			return ErrCopilotRerunConflict
		}
		scope, current, err := lockCopilotRerunTargetScope(ctx, tx, &hint, "inspect", authorize, executionLockRerunClaim)
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
		var saved model.CopilotRerunIntent
		if err := lock().Where("schedule_id = ?", hint.ScheduleID).First(&schedule).Error; err != nil {
			return err
		}
		if err := lock().Where("intent_id = ?", hint.IntentID).First(&saved).Error; err != nil {
			return err
		}
		if !reflect.DeepEqual(hint, saved) || saved.IntentDigest != rerunIntentDigest(&saved) {
			return ErrCopilotRerunConflict
		}
		if err := inspectCopilotRerunTuple(ctx, tx, &saved, &ids, &snapshot, &evidence); err != nil {
			return err
		}
		if schedule.SealID == nil {
			return ErrCopilotRerunConflict
		}
		binding := CopilotRerunSealBinding{Version: 1, SealID: *schedule.SealID, ScheduleID: schedule.ScheduleID, RunID: schedule.RunID, JobRunID: schedule.JobRunID, AgentID: schedule.AgentID, PayloadDigest: schedule.PayloadDigest, CipherVersion: schedule.CipherVersion, CiphertextDigest: schedule.CiphertextDigest, SecretVersionsDigest: schedule.SecretVersionsDigest, ExpiresAtUnixMilli: schedule.ExpiresAt.UnixMilli()}
		if ValidateCopilotRerunSealedBinding(&schedule, &binding) != nil || gate.SealID != binding.SealID || gate.SealDigest != schedule.SealDigest || gate.AgentID != binding.AgentID || scope.Job.AgentID != binding.AgentID || scope.Job.ClaimID != gate.ClaimID || schedule.ClaimedID != gate.ClaimID || schedule.ClaimedAt == nil || !schedule.ClaimedAt.Equal(*gate.ClaimedAt) || scope.Job.StartTime == nil || !scope.Job.StartTime.Equal(*gate.ClaimedAt) || scope.Run.StartTime == nil || !scope.Run.StartTime.Equal(*gate.ClaimedAt) {
			return ErrCopilotRerunConflict
		}
		if err := validateCopilotRerunClaimedReceipt(scope, gate, &schedule); err != nil {
			return err
		}
		if err := validateCopilotRerunClaimDispatch(scope, &ids); err != nil {
			return err
		}
		receipt = &CopilotRerunClaimReceipt{gate.AgentID, saved.PipelineID, ids.RunID, ids.JobRunID, ids.GateID, ids.ScheduleID, gate.SealID, gate.SealDigest, schedule.PayloadDigest, gate.ClaimID, scope.Job.ArtifactStorageID, scope.Job.ArtifactStorageFingerprint, scope.Job.ArtifactStorageType, gate.ClaimedAt.UnixMilli()}
		return current(ctx)
	})
	if err != nil {
		return nil, rerunError(err)
	}
	return receipt, nil
}

func validateCopilotRerunClaimedReceipt(scope *CopilotRerunScope, gate *model.CopilotRerunAdmissionGate, schedule *model.CopilotRerunSchedule) error {
	if !rerunClaimStorageIdentityValid(scope.Job.ArtifactStorageID, scope.Job.ArtifactStorageFingerprint) || ((scope.Job.ArtifactStorageID == "") != (scope.Job.ArtifactStorageType == "")) || (scope.Job.ArtifactStorageID != "" && !rerunClaimStorageTypeValid(scope.Job.ArtifactStorageType)) {
		return ErrCopilotRerunConflict
	}
	if gate.State != "claimed" || gate.ClaimedAt == nil || scope.Job.ClaimID != gate.ClaimID || scope.Job.AgentID != gate.AgentID || schedule.ClaimedID != gate.ClaimID || schedule.ClaimedAt == nil || !schedule.ClaimedAt.Equal(*gate.ClaimedAt) || scope.Job.StartTime == nil || !scope.Job.StartTime.Equal(*gate.ClaimedAt) || scope.Run.StartTime == nil || !scope.Run.StartTime.Equal(*gate.ClaimedAt) {
		return ErrCopilotRerunConflict
	}
	if schedule.State != CopilotRerunScheduleClaimed && schedule.State != CopilotRerunScheduleCompleted && schedule.State != CopilotRerunScheduleCancelled {
		return ErrCopilotRerunConflict
	}
	return nil
}
func validateCopilotRerunClaimDispatch(scope *CopilotRerunScope, ids *CopilotRerunExecutionIdentities) error {
	runtime := &taskqueue.RuntimeRequest{Type: scope.Job.RuntimeType, Image: scope.Job.RuntimeImage, Resources: taskqueue.RuntimeResources{CPU: scope.Job.RuntimeCPU, Memory: scope.Job.RuntimeMemory}}
	runtimeDigest, e := taskqueue.RuntimeBindingDigest(scope.Job.PipelineID, ids.RunID, ids.JobRunID, scope.Job.AgentID, runtime)
	if e != nil || runtimeDigest != scope.Job.RuntimeBindingDigest {
		return ErrCopilotRerunConflict
	}
	plan, e := taskqueue.ParseJobRetryPlan(scope.Job.RetryPlan)
	if e != nil {
		return ErrCopilotRerunConflict
	}
	planDigest, e := plan.Digest(scope.Job.PipelineID, ids.RunID, ids.JobRunID, scope.Job.AgentID, runtimeDigest)
	if e != nil || planDigest != scope.Job.RetryPlanDigest || len(plan.Steps) != len(ids.Steps) {
		return ErrCopilotRerunConflict
	}
	for i, step := range plan.Steps {
		if step.Name != ids.Steps[i].Name || step.StepIndex != ids.Steps[i].Index || !reflect.DeepEqual(step.AttemptRunIDs, ids.Steps[i].Attempts) {
			return ErrCopilotRerunConflict
		}
	}

	return nil
}
