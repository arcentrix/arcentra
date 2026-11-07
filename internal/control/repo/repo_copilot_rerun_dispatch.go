// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CopilotRerunDispatchReference authorizes only a reference notification. It
// never claims, discloses plaintext, or converts a prior receipt into authority.
// The caller publishes outside this transaction; an unknown queue outcome must
// replay this exact immutable reference, never allocate replacement execution.
func (r *Repositories) CopilotRerunDispatchReference(ctx context.Context, lease CopilotRerunLeaseToken, authorize CopilotRerunAuthorize, validate CopilotRerunClaimValidator) (*taskqueue.RerunClaimReference, error) {
	if r == nil || r.database == nil || authorize == nil || validate == nil || !rerunUUID(lease.ScheduleID) || !historyID(lease.Holder) || lease.Epoch == 0 {
		return nil, ErrCopilotRerunInvalid
	}
	db := r.database.Database().Clauses(database.Write())
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		return nil, ErrCopilotRerunInvalid
	}
	var hints []model.CopilotRerunIntent
	if err := db.WithContext(ctx).Where("schedule_id = ?", lease.ScheduleID).Limit(2).Find(&hints).Error; err != nil {
		return nil, rerunError(err)
	}
	if len(hints) != 1 || hints[0].ScheduleID != lease.ScheduleID || hints[0].State != CopilotRerunConfirmed {
		return nil, ErrCopilotRerunConflict
	}
	hint := hints[0]
	var result *taskqueue.RerunClaimReference
	err := executionTransaction(ctx, db, hint.NewJobRunID, func(tx *gorm.DB) error {
		gate, err := lockCopilotRerunTargetGate(ctx, tx, &hint)
		if err != nil {
			return err
		}
		if gate.State != "unclaimed" || gate.ClaimID != "" || gate.AdmissionNonceDigest != "" || gate.SealID == "" {
			return ErrCopilotRerunConflict
		}
		// Reuse the unclaimed seal proof and active scope checks; expose the narrower
		// dispatch operation to the trusted authorizer, never the historical inspect path.
		scope, current, err := lockCopilotRerunSealScope(ctx, tx, &hint, "seal", func(c context.Context, v *Repositories, s *CopilotRerunScope, owner, action string) error {
			if action != "seal" {
				return ErrCopilotRerunForbidden
			}
			return authorize(c, v, s, owner, "dispatch")
		})
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
		d, err := evidence.Digest()
		if err != nil || d != hint.OriginalEvidenceDigest {
			return ErrCopilotRerunConflict
		}
		var agents []model.Agent
		if err := lock().Where("agent_id = ?", gate.AgentID).Limit(2).Find(&agents).Error; err != nil {
			return err
		}
		if len(agents) != 1 || agents[0].AgentID != gate.AgentID {
			return ErrCopilotRerunConflict
		}
		agent := &agents[0]
		if err := validateCopilotRerunSealAgent(agent, time.Now()); err != nil {
			return err
		}
		// Immutable sealed bytes may be read before the final Schedule lock. They are
		// compared again under that lock; no callback can publish a response here.
		var candidate model.CopilotRerunSchedule
		if err := tx.WithContext(ctx).Where("schedule_id = ?", lease.ScheduleID).First(&candidate).Error; err != nil {
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
		if err := lock().Where("schedule_id = ?", lease.ScheduleID).First(&schedule).Error; err != nil {
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
		binding := CopilotRerunSealBinding{Version: 1, SealID: gate.SealID, ScheduleID: lease.ScheduleID, RunID: hint.NewRunID, JobRunID: hint.NewJobRunID, AgentID: gate.AgentID, PayloadDigest: candidate.PayloadDigest, CipherVersion: schedule.CipherVersion, CiphertextDigest: schedule.CiphertextDigest, SecretVersionsDigest: schedule.SecretVersionsDigest, ExpiresAtUnixMilli: schedule.ExpiresAt.UnixMilli()}
		if ValidateCopilotRerunSealedBinding(&schedule, &binding) != nil || schedule.SealDigest != gate.SealDigest || schedule.CancelRequested || schedule.CompletedAt != nil {
			return ErrCopilotRerunConflict
		}
		if err := validateRerunDispatchMaterial(material, &saved, &snapshot, &evidence, &ids, scope.Job, &binding, schedule.PayloadCiphertext); err != nil {
			return err
		}

		if schedule.State != CopilotRerunScheduleSealed || schedule.ClaimedID != "" || schedule.ClaimedAt != nil || schedule.AgentID != gate.AgentID || !CopilotRerunControllerLeaseCurrent(&schedule, lease.Holder, lease.Epoch, time.Now()) || schedule.LeaseUntil == nil || !schedule.LeaseUntil.Equal(lease.Until) {
			return ErrCopilotRerunConflict
		}
		if err := validateCopilotRerunStoredDispatch(ctx, tx, scope, &schedule); err != nil {
			return err
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
		now := time.Now()
		if !now.Before(lease.Until) || !now.Before(saved.ExpiresAt) || !now.Before(snapshot.ExpiresAt) || !now.Before(schedule.ExpiresAt) {
			return ErrCopilotRerunExpired
		}
		if err := validateCopilotRerunSealAgent(agent, now); err != nil {
			return err
		}
		ref := taskqueue.RerunClaimReference{AgentID: gate.AgentID, PipelineID: saved.PipelineID, RunID: saved.NewRunID, JobRunID: saved.NewJobRunID, GateID: gate.GateID, ScheduleID: schedule.ScheduleID, SealID: gate.SealID, SealDigest: gate.SealDigest, PayloadDigest: schedule.PayloadDigest}
		if ref.Validate() != nil {
			return ErrCopilotRerunConflict
		}
		result = &ref
		return nil
	})
	if err != nil {
		return nil, rerunError(err)
	}
	return result, nil
}
func validateRerunDispatchMaterial(v *CopilotRerunClaimMaterial, saved *model.CopilotRerunIntent, snapshot *model.CopilotDefinitionSnapshot, evidence *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, job *model.JobRun, binding *CopilotRerunSealBinding, cipher []byte) error {
	if v == nil || len(v.PayloadJSON) == 0 || len(v.PayloadJSON) > CopilotRerunMaxDefinitionBytes {
		return ErrCopilotRerunInvalid
	}
	var p taskqueue.JobRunTaskPayload
	dec := json.NewDecoder(bytes.NewReader(v.PayloadJSON))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil {
		return ErrCopilotRerunInvalid
	}
	canonical, err := json.Marshal(&p)
	if err != nil || !bytes.Equal(canonical, v.PayloadJSON) {
		return ErrCopilotRerunInvalid
	}
	if err := validateCopilotRerunSealMaterial(&CopilotRerunSealedPayload{Reviewed: v.Reviewed, Payload: &p, Binding: binding, Ciphertext: cipher}, saved, snapshot, evidence, ids, job, CopilotRerunSealInput{AgentID: binding.AgentID, SealID: binding.SealID}); err != nil {
		return err
	}
	plan, err := json.Marshal(p.RetryPlan)
	if err != nil || p.Runtime.BindingDigest != job.RuntimeBindingDigest || p.RetryPlanDigest != job.RetryPlanDigest || !rerunJSONEqual(string(plan), job.RetryPlan) {
		return ErrCopilotRerunConflict
	}
	return nil
}
