// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CopilotRerunClaim admits controlled preparation only. The returned original
// bytes require current authority even for the exact same-incarnation replay.
// No input observation, approval, generation or user-step admission is minted.
func (r *Repositories) CopilotRerunClaim(ctx context.Context, in CopilotRerunClaimInput, authorize CopilotRerunAuthorize, validate CopilotRerunClaimValidator) (*CopilotRerunClaimResult, error) {
	nonceDigest, err := rerunClaimNonceDigest(in)
	if err != nil || r == nil || r.database == nil || authorize == nil || validate == nil {
		return nil, errors.Join(ErrCopilotRerunClaimRejected, ErrCopilotRerunInvalid)
	}
	db := r.database.Database().Clauses(database.Write())
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		return nil, errors.Join(ErrCopilotRerunClaimRejected, ErrCopilotRerunInvalid)
	}
	var hints []model.CopilotRerunIntent
	if err := db.WithContext(ctx).Where("schedule_id = ?", in.ScheduleID).Limit(2).Find(&hints).Error; err != nil {
		return nil, rerunError(err)
	}
	if len(hints) != 1 {
		return nil, ErrCopilotRerunConflict
	}
	hint := hints[0]
	if hint.ScheduleID != in.ScheduleID || hint.GateID != in.GateID || hint.NewRunID != in.RunID || hint.NewJobRunID != in.JobRunID || hint.PipelineID != in.PipelineID || hint.State != CopilotRerunConfirmed {
		return nil, ErrCopilotRerunConflict
	}
	var result *CopilotRerunClaimResult
	var callbackFailure error
	err = executionTransaction(ctx, db, in.JobRunID, func(tx *gorm.DB) (txErr error) {
		defer func() { callbackFailure = txErr }()
		gate, err := lockCopilotRerunGateForOperation(ctx, tx, &hint, false, executionLockRerunClaim)
		if err != nil {
			return err
		}
		if gate.AgentID != in.AgentID || gate.SealID != in.SealID || gate.SealDigest != in.SealDigest {
			return ErrCopilotRerunConflict
		}
		replay := gate.State == "claimed"
		if replay && (gate.ClaimID != in.ClaimID || !rerunClaimNonceMatches(gate.AdmissionNonceDigest, in)) {
			return errors.Join(errCopilotRerunIncarnationMismatch, ErrCopilotRerunConflict)
		}
		scope, current, err := lockCopilotRerunTargetScope(ctx, tx, &hint, "claim", authorize, executionLockRerunClaim)
		if err != nil {
			return err
		}
		lock := func() *gorm.DB { return tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}) }
		var snapshot model.CopilotDefinitionSnapshot
		if err := tx.WithContext(ctx).Where("snapshot_id = ?", hint.SnapshotID).First(&snapshot).Error; err != nil {
			return err
		}
		if ValidateCopilotDefinitionSnapshot(&snapshot) != nil || snapshot.SnapshotID != hint.SnapshotID || snapshot.BindingDigest != hint.SnapshotDigest || snapshot.ProjectID != hint.ProjectID || snapshot.PipelineID != hint.PipelineID {
			return ErrCopilotRerunConflict
		}
		config, err := CopilotDefinitionRepositoryConfigDigest(scope.Pipeline, scope.Project)
		if err != nil || config != snapshot.RepositoryConfigDigest {
			return ErrCopilotRerunConflict
		}
		var ids CopilotRerunExecutionIdentities
		var evidence CopilotRerunOriginalEvidence
		layout, err := ParseCopilotRerunLayout(snapshot.LayoutJSON)
		if err != nil {
			return err
		}
		if json.Unmarshal([]byte(hint.StepIdentitiesJSON), &ids) != nil || ids.Validate(layout) != nil || json.Unmarshal([]byte(hint.OriginalEvidenceJSON), &evidence) != nil {
			return ErrCopilotRerunConflict
		}
		if err := validateCopilotRerunSealIdentity(in.ClaimID, &hint, &evidence, &ids, in.AgentID); err != nil {
			return err
		}
		d, err := evidence.Digest()
		if err != nil || d != hint.OriginalEvidenceDigest {
			return ErrCopilotRerunConflict
		}
		var agents []model.Agent
		if err := lock().Where("agent_id = ?", in.AgentID).Limit(2).Find(&agents).Error; err != nil {
			return err
		}
		if len(agents) != 1 || agents[0].AgentID != in.AgentID {
			return ErrCopilotRerunConflict
		}
		agent := &agents[0]
		if err := validateCopilotRerunSealAgent(agent, time.Now()); err != nil {
			return err
		}
		storageType, err := lockCopilotRerunClaimStorage(ctx, tx, scope.Job, in, replay)
		if err != nil {
			return err
		}
		// Immutable sealed bytes may be read before the final Schedule lock. They are
		// compared again under that lock; no callback can publish a response here.
		var candidate model.CopilotRerunSchedule
		if err := tx.WithContext(ctx).Where("schedule_id = ?", in.ScheduleID).First(&candidate).Error; err != nil {
			return err
		}
		before, _ := json.Marshal([]any{scope, &snapshot, &evidence, &ids, &hint, agent, &candidate})
		view, reviewCurrent := NewScopedAuthorizationView(tx)
		material, err := validate(ctx, view, scope, &snapshot, &evidence, &ids, &hint, agent, &candidate)
		if err != nil {
			return err
		}
		after, _ := json.Marshal([]any{scope, &snapshot, &evidence, &ids, &hint, agent, &candidate})
		if !bytes.Equal(before, after) {
			return ErrCopilotRerunInvalid
		}
		var schedule model.CopilotRerunSchedule
		var saved model.CopilotRerunIntent
		if err := lock().Where("schedule_id = ?", in.ScheduleID).First(&schedule).Error; err != nil {
			return err
		}
		if err := lock().Where("intent_id = ?", hint.IntentID).First(&saved).Error; err != nil {
			return err
		}
		if !reflect.DeepEqual(saved, hint) || saved.IntentDigest != rerunIntentDigest(&saved) || !rerunClaimScheduleUnchanged(candidate, schedule) {
			return ErrCopilotRerunConflict
		}
		if err := inspectCopilotRerunTuple(ctx, tx, &saved, &ids, &snapshot, &evidence); err != nil {
			return err
		}
		binding := CopilotRerunSealBinding{Version: 1, SealID: in.SealID, ScheduleID: in.ScheduleID, RunID: in.RunID, JobRunID: in.JobRunID, AgentID: in.AgentID, PayloadDigest: in.PayloadDigest, CipherVersion: schedule.CipherVersion, CiphertextDigest: schedule.CiphertextDigest, SecretVersionsDigest: schedule.SecretVersionsDigest, ExpiresAtUnixMilli: schedule.ExpiresAt.UnixMilli()}
		if ValidateCopilotRerunSealedBinding(&schedule, &binding) != nil || schedule.SealDigest != in.SealDigest || schedule.CancelRequested || schedule.CompletedAt != nil {
			return ErrCopilotRerunConflict
		}
		payload, err := validateCopilotRerunClaimMaterial(material, &saved, &snapshot, &evidence, &ids, scope.Job, in, &binding, schedule.PayloadCiphertext)
		if err != nil {
			return err
		}
		_ = payload
		now := time.Now().UTC().Truncate(time.Millisecond)
		if !now.Before(saved.ExpiresAt) || !now.Before(snapshot.ExpiresAt) || !now.Before(schedule.ExpiresAt) {
			return ErrCopilotRerunExpired
		}
		if replay {
			if gate.ClaimedAt == nil || schedule.State != CopilotRerunScheduleClaimed || schedule.ClaimedID != in.ClaimID || schedule.ClaimedAt == nil || !schedule.ClaimedAt.Equal(*gate.ClaimedAt) || scope.Job.ClaimID != in.ClaimID || scope.Job.Status != model.JobRunStatusRunning || scope.Job.StartTime == nil || !scope.Job.StartTime.Equal(*gate.ClaimedAt) || scope.Run.Status != model.PipelineStatusRunning || scope.Run.StartTime == nil || !scope.Run.StartTime.Equal(*gate.ClaimedAt) {
				return ErrCopilotRerunConflict
			}
		} else {
			if schedule.State != CopilotRerunScheduleSealed || gate.AdmissionNonceDigest != "" {
				return ErrCopilotRerunConflict
			}
			if err := validateCopilotRerunStoredDispatch(ctx, tx, scope, &schedule); err != nil {
				return err
			}
			for _, write := range []struct {
				row    any
				where  string
				args   []any
				values map[string]any
			}{
				{&model.JobRun{}, "job_run_id = ? AND claim_id = ? AND status = ?", []any{in.JobRunID, "", model.JobRunStatusPending}, map[string]any{"claim_id": in.ClaimID, "status": model.JobRunStatusRunning, "start_time": now, "artifact_storage_id": in.ArtifactStorageID, "artifact_storage_fingerprint": in.ArtifactStorageFingerprint, "artifact_storage_type": storageType}},
				{&model.PipelineRun{}, "run_id = ? AND status = ?", []any{in.RunID, model.PipelineStatusPending}, map[string]any{"status": model.PipelineStatusRunning, "start_time": now, "running_jobs": 1}},
				{&model.CopilotRerunAdmissionGate{}, "gate_id = ? AND version = ? AND state = ? AND claim_id = ?", []any{in.GateID, gate.Version, "unclaimed", ""}, map[string]any{"state": "claimed", "claim_id": in.ClaimID, "claimed_at": now, "admission_nonce_digest": nonceDigest, "version": gate.Version + 1}},
				{&model.CopilotRerunSchedule{}, "schedule_id = ? AND version = ? AND state = ? AND claimed_id = ?", []any{in.ScheduleID, schedule.Version, CopilotRerunScheduleSealed, ""}, map[string]any{"state": CopilotRerunScheduleClaimed, "claimed_id": in.ClaimID, "claimed_at": now, "version": schedule.Version + 1}},
			} {
				res := tx.Model(write.row).Where(write.where, write.args...).Updates(write.values)
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected != 1 {
					return ErrCopilotRerunConflict
				}
			}
			gate.ClaimedAt = &now
		}
		if err := current(ctx); err != nil {
			return err
		}
		if err := reviewCurrent(ctx); err != nil {
			return err
		}
		if err := checkRetryApprovalAuthorizationExpiry(ctx, tx); err != nil {
			return err
		}
		if !time.Now().Before(saved.ExpiresAt) || !time.Now().Before(snapshot.ExpiresAt) || !time.Now().Before(schedule.ExpiresAt) {
			return ErrCopilotRerunExpired
		}
		if err := validateCopilotRerunSealAgent(agent, time.Now()); err != nil {
			return err
		}
		result = &CopilotRerunClaimResult{Receipt: &CopilotRerunClaimReceipt{in.AgentID, in.PipelineID, in.RunID, in.JobRunID, in.GateID, in.ScheduleID, in.SealID, in.SealDigest, in.PayloadDigest, in.ClaimID, in.ArtifactStorageID, in.ArtifactStorageFingerprint, storageType, gate.ClaimedAt.UnixMilli()}, PayloadJSON: append([]byte(nil), material.PayloadJSON...)}
		return nil
	})
	if err != nil {
		// This exact callback branch precedes all claim writes and disclosure.
		// Do not classify generic conflicts or transaction/commit failures here.
		if callbackFailure != nil && errors.Is(err, callbackFailure) && errors.Is(callbackFailure, errCopilotRerunIncarnationMismatch) {
			return nil, errors.Join(ErrCopilotRerunClaimRejected, errCopilotRerunIncarnationMismatch, ErrCopilotRerunConflict)
		}
		normalized := rerunError(err)
		if callbackFailure != nil && errors.Is(err, callbackFailure) && (errors.Is(normalized, ErrCopilotRerunForbidden) || errors.Is(normalized, ErrCopilotRerunExpired) || errors.Is(normalized, ErrCopilotRerunInvalid) || errors.Is(normalized, ErrCopilotRerunUnsupported)) {
			return nil, errors.Join(ErrCopilotRerunClaimRejected, normalized)
		}
		return nil, normalized
	}
	return result, nil
}
func validateCopilotRerunClaimMaterial(v *CopilotRerunClaimMaterial, saved *model.CopilotRerunIntent, snapshot *model.CopilotDefinitionSnapshot, evidence *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, job *model.JobRun, in CopilotRerunClaimInput, binding *CopilotRerunSealBinding, ciphertext []byte) (*taskqueue.JobRunTaskPayload, error) {
	if v == nil || len(v.PayloadJSON) == 0 || len(v.PayloadJSON) > CopilotRerunMaxDefinitionBytes {
		return nil, ErrCopilotRerunInvalid
	}
	var p taskqueue.JobRunTaskPayload
	dec := json.NewDecoder(bytes.NewReader(v.PayloadJSON))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil {
		return nil, ErrCopilotRerunInvalid
	}
	canonical, err := json.Marshal(&p)
	if err != nil || !bytes.Equal(canonical, v.PayloadJSON) {
		return nil, ErrCopilotRerunInvalid
	}
	sealInput := CopilotRerunSealInput{AgentID: in.AgentID, SealID: in.SealID}
	if err := validateCopilotRerunSealMaterial(&CopilotRerunSealedPayload{Reviewed: v.Reviewed, Payload: &p, Binding: binding, Ciphertext: ciphertext}, saved, snapshot, evidence, ids, job, sealInput); err != nil {
		return nil, err
	}
	if err := validateCopilotRerunOutputStorage(&p, in); err != nil {
		return nil, err
	}
	planJSON, err := json.Marshal(p.RetryPlan)
	if err != nil || p.Runtime.BindingDigest != job.RuntimeBindingDigest || p.RetryPlanDigest != job.RetryPlanDigest || !rerunJSONEqual(string(planJSON), job.RetryPlan) {
		return nil, ErrCopilotRerunConflict
	}
	return &p, nil
}

// Controller lease takeover cannot invalidate the immutable published seal.
// Every non-lease field is compared, including cancellation and claim state.
func rerunClaimScheduleUnchanged(a, b model.CopilotRerunSchedule) bool {
	a.LeaseOwner, b.LeaseOwner = "", ""
	a.LeaseEpoch, b.LeaseEpoch = 0, 0
	a.LeaseUntil, b.LeaseUntil = nil, nil
	a.Version, b.Version = 0, 0
	return reflect.DeepEqual(a, b)
}
