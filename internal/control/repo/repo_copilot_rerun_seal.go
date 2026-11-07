// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errCopilotRerunSealReplay = errors.New("inspect committed rerun seal")

type CopilotRerunSealReceipt struct {
	Binding                      CopilotRerunSealBinding
	State                        string
	GateVersion, ScheduleVersion uint64
}

// Seal assigns an immutable encrypted delivery to one Agent. It does not publish
// anything, claim execution, pin observed inputs or grant user-command admission.
func (r *Repositories) CopilotRerunSeal(ctx context.Context, lease CopilotRerunLeaseToken, in CopilotRerunSealInput, authorize CopilotRerunAuthorize, build CopilotRerunSealBuilder) (*CopilotRerunSealReceipt, error) {
	if r == nil || r.database == nil || authorize == nil || build == nil || !rerunUUID(in.IntentID) || !rerunUUID(in.SealID) || !historyID(in.OwnerUserID) || !historyID(in.ProjectID) || !historyID(in.AgentID) || in.ExpectedIntentVersion == 0 || in.ExpectedGateVersion == 0 || in.ExpectedScheduleVersion == 0 || in.ExpectedGateVersion >= math.MaxInt64 || in.ExpectedScheduleVersion >= math.MaxInt64 {
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
	if hint.IntentID != in.IntentID || hint.OwnerUserID != in.OwnerUserID || hint.ProjectID != in.ProjectID || hint.IntentDigest != in.ExpectedIntentDigest || hint.ReviewedRunContextDigest != in.ExpectedReviewedRunContextDigest || hint.Version != in.ExpectedIntentVersion || hint.State != CopilotRerunConfirmed || lease.ScheduleID != hint.ScheduleID {
		return nil, ErrCopilotRerunConflict
	}
	var receipt *CopilotRerunSealReceipt
	err := executionTransaction(ctx, db, hint.NewJobRunID, func(tx *gorm.DB) error {
		gate, err := lockCopilotRerunTargetGateMode(ctx, tx, &hint, true)
		if err != nil {
			return err
		}
		if gate.SealID != "" {
			return errCopilotRerunSealReplay
		}
		if gate.State != "unclaimed" {
			return ErrCopilotRerunConflict
		}
		scope, current, err := lockCopilotRerunSealScope(ctx, tx, &hint, "seal", authorize)
		if err != nil {
			return err
		}
		lock := func() *gorm.DB { return tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}) }
		var snapshot model.CopilotDefinitionSnapshot
		// Immutable source evidence only; never enter source Run/Job locks here.
		if err := tx.WithContext(ctx).Where("snapshot_id = ?", hint.SnapshotID).First(&snapshot).Error; err != nil {
			return err
		}
		if ValidateCopilotDefinitionSnapshot(&snapshot) != nil || snapshot.SnapshotID != hint.SnapshotID || snapshot.BindingDigest != hint.SnapshotDigest || snapshot.ProjectID != hint.ProjectID || snapshot.PipelineID != hint.PipelineID {
			return ErrCopilotRerunConflict
		}
		configDigest, configErr := CopilotDefinitionRepositoryConfigDigest(scope.Pipeline, scope.Project)
		if configErr != nil || configDigest != snapshot.RepositoryConfigDigest {
			return ErrCopilotRerunConflict
		}
		var evidence CopilotRerunOriginalEvidence
		if json.Unmarshal([]byte(hint.OriginalEvidenceJSON), &evidence) != nil {
			return ErrCopilotRerunConflict
		}
		evidenceDigest, err := evidence.Digest()
		if err != nil || evidenceDigest != hint.OriginalEvidenceDigest {
			return ErrCopilotRerunConflict
		}
		layout, err := ParseCopilotRerunLayout(snapshot.LayoutJSON)
		if err != nil {
			return err
		}
		var ids CopilotRerunExecutionIdentities
		if json.Unmarshal([]byte(hint.StepIdentitiesJSON), &ids) != nil || ids.Validate(layout) != nil || ids.RunID != hint.NewRunID || ids.JobRunID != hint.NewJobRunID || ids.GateID != hint.GateID || ids.ScheduleID != hint.ScheduleID {
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
		var steps []model.StepRun
		expectedSteps := 0
		for _, s := range ids.Steps {
			expectedSteps += len(s.Attempts)
		}
		if err := lock().Where("job_run_id = ?", ids.JobRunID).Order("id").Limit(expectedSteps + 1).Find(&steps).Error; err != nil {
			return err
		}
		if len(steps) != expectedSteps {
			return ErrCopilotRerunConflict
		}
		// Scope locks make these immutable hints safe for policy construction. The
		// exact Intent is locked/revalidated last before any write.
		view, reviewCurrent := NewScopedAuthorizationView(tx)
		fixed, _ := json.Marshal([]any{scope, snapshot, evidence, ids, hint, agent})
		material, err := build(ctx, view, scope, &snapshot, &evidence, &ids, &hint, agent, in.SealID)
		if err != nil {
			return err
		}
		after, _ := json.Marshal([]any{scope, snapshot, evidence, ids, hint, agent})
		if string(fixed) != string(after) {
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
		if !reflect.DeepEqual(saved, hint) || saved.IntentDigest != rerunIntentDigest(&saved) || schedule.ScheduleID != ids.ScheduleID || schedule.IntentID != saved.IntentID || schedule.OwnerUserID != saved.OwnerUserID || schedule.ProjectID != saved.ProjectID || schedule.PipelineID != saved.PipelineID || schedule.RunID != ids.RunID || schedule.JobRunID != ids.JobRunID || !schedule.ExpiresAt.Equal(saved.ExpiresAt) {
			return ErrCopilotRerunConflict
		}
		if err := inspectCopilotRerunTuple(ctx, tx, &saved, &ids, &snapshot, &evidence); err != nil {
			return err
		}
		if err := validateCopilotRerunSealMaterial(material, &saved, &snapshot, &evidence, &ids, scope.Job, in); err != nil {
			return err
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		if !now.Before(saved.ExpiresAt) || !now.Before(snapshot.ExpiresAt) {
			return ErrCopilotRerunExpired
		}
		if gate.Version != in.ExpectedGateVersion || gate.SealID != "" || gate.SealDigest != "" || gate.AgentID != "" || schedule.State != CopilotRerunSchedulePending || schedule.Version != in.ExpectedScheduleVersion || schedule.SealID != nil || schedule.SealDigest != "" || schedule.AgentID != "" || len(schedule.PayloadCiphertext) != 0 || !CopilotRerunControllerLeaseCurrent(&schedule, lease.Holder, lease.Epoch, now) || schedule.LeaseUntil == nil || !schedule.LeaseUntil.Equal(lease.Until) {
			return ErrCopilotRerunConflict
		}
		if err := validateCopilotRerunSealAgent(agent, time.Now()); err != nil {
			return err
		}
		if err := writeCopilotRerunSeal(ctx, tx, scope, &steps, &schedule, gate, material, now); err != nil {
			return err
		}
		if err := current(ctx); err != nil {
			return err
		}
		if err := reviewCurrent(ctx); err != nil {
			return err
		}
		if !time.Now().Before(lease.Until) || !time.Now().Before(saved.ExpiresAt) || !time.Now().Before(snapshot.ExpiresAt) {
			return ErrCopilotRerunExpired
		}
		receipt = &CopilotRerunSealReceipt{*material.Binding, CopilotRerunScheduleSealed, gate.Version + 1, schedule.Version + 1}
		return nil
	})
	if errors.Is(err, errCopilotRerunSealReplay) {
		observed, inspectErr := r.CopilotRerunInspectSeal(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, authorize)
		if inspectErr != nil {
			return nil, inspectErr
		}
		expectedGateVersion := in.ExpectedGateVersion + 1
		if observed.State == CopilotRerunScheduleCancelled || observed.State == CopilotRerunScheduleClaimed || observed.State == CopilotRerunScheduleCompleted {
			expectedGateVersion++
		}
		if (observed.State != CopilotRerunScheduleSealed && observed.State != CopilotRerunScheduleCancelled && observed.State != CopilotRerunScheduleClaimed && observed.State != CopilotRerunScheduleCompleted) || observed.Binding.SealID != in.SealID || observed.Binding.AgentID != in.AgentID || observed.GateVersion != expectedGateVersion {
			return nil, ErrCopilotRerunConflict
		}
		return observed, nil
	}
	if err != nil {
		return nil, rerunError(err)
	}
	return receipt, nil
}

func lockCopilotRerunSealScope(ctx context.Context, tx *gorm.DB, saved *model.CopilotRerunIntent, action string, authorize CopilotRerunAuthorize) (*CopilotRerunScope, func(context.Context) error, error) {
	return lockCopilotRerunTargetScope(ctx, tx, saved, action, authorize, executionLockRerunSeal)
}
func lockCopilotRerunTargetScope(ctx context.Context, tx *gorm.DB, saved *model.CopilotRerunIntent, action string, authorize CopilotRerunAuthorize, mode executionLockMode) (*CopilotRerunScope, func(context.Context) error, error) {
	if saved == nil {
		return nil, nil, ErrCopilotRerunInvalid
	}
	return lockCopilotRerunTargetScopeAs(ctx, tx, saved, saved.OwnerUserID, action, authorize, mode)
}

func lockCopilotRerunTargetScopeAs(ctx context.Context, tx *gorm.DB, saved *model.CopilotRerunIntent, actorID, action string, authorize CopilotRerunAuthorize, mode executionLockMode) (*CopilotRerunScope, func(context.Context) error, error) {
	if saved == nil || authorize == nil || !historyID(actorID) {
		return nil, nil, ErrCopilotRerunForbidden
	}
	if (mode != executionLockRerunSeal || (action != "seal" && action != "inspect" && action != "cancel")) && (mode != executionLockRerunClaim || (action != "claim" && action != "inspect")) {
		return nil, nil, ErrCopilotRerunInvalid
	}
	job, err := lockExecutionJob(ctx, tx, saved.NewJobRunID, mode)
	if err != nil {
		return nil, nil, err
	}
	if job.JobRunID != saved.NewJobRunID || job.PipelineRunID != saved.NewRunID || job.PipelineID != saved.PipelineID {
		return nil, nil, ErrCopilotRerunForbidden
	}
	scope := &CopilotRerunScope{Job: job, Run: &model.PipelineRun{}, Pipeline: &model.Pipeline{}, Project: &model.Project{}, JobCount: 1}
	lock := func() *gorm.DB { return tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}) }
	for _, item := range []struct {
		field, id string
		row       any
	}{{"job_run_id", saved.NewJobRunID, scope.Job}, {"run_id", saved.NewRunID, scope.Run}, {"pipeline_id", saved.PipelineID, scope.Pipeline}} {
		if err := lock().Where(item.field+" = ?", item.id).First(item.row).Error; err != nil {
			return nil, nil, err
		}
	}
	// Scope pointers cannot cause a late lock on a foreign Project.
	if scope.Run.ProjectID != saved.ProjectID || scope.Pipeline.ProjectID != saved.ProjectID {
		return nil, nil, ErrCopilotRerunForbidden
	}
	if err := lock().Where("project_id = ?", scope.Pipeline.ProjectID).First(scope.Project).Error; err != nil {
		return nil, nil, err
	}
	if scope.Job.JobRunID != saved.NewJobRunID || scope.Job.PipelineRunID != saved.NewRunID || scope.Run.RunID != saved.NewRunID || scope.Run.ProjectID != saved.ProjectID || scope.Run.PipelineID != saved.PipelineID || scope.Pipeline.PipelineID != saved.PipelineID || scope.Pipeline.ProjectID != saved.ProjectID || scope.Project.ProjectID != saved.ProjectID {
		return nil, nil, ErrCopilotRerunForbidden
	}
	if (action == "seal" || action == "claim") && (scope.Pipeline.IsEnabled != 1 || scope.Pipeline.IsDeleted != 0 || scope.Project.IsEnabled != 1 || scope.Project.Status != model.ProjectStatusActive) {
		return nil, nil, ErrCopilotRerunForbidden
	}
	view, current := NewScopedAuthorizationView(tx)
	actor, err := view.User.(IInboxActorReader).GetInboxActor(ctx, actorID)
	if err != nil || actor == nil || actor.UserID != actorID || actor.IsEnabled != 1 {
		return nil, nil, ErrCopilotRerunForbidden
	}
	if err := authorize(ctx, view, scope, actorID, action); err != nil {
		return nil, nil, ErrCopilotRerunForbidden
	}
	return scope, current, nil
}

func validateCopilotRerunSealAgent(agent *model.Agent, now time.Time) error {
	var capabilities taskqueue.AgentRuntimeCapabilities
	if agent == nil || agent.IsEnabled != 1 || (agent.Status != 1 && agent.Status != 4) || agent.OS != "linux" || agent.LastHeartbeat == nil || now.Sub(*agent.LastHeartbeat) > 2*time.Minute || agent.LastHeartbeat.After(now.Add(time.Minute)) || json.Unmarshal(agent.RuntimeCapabilities, &capabilities) != nil || !capabilities.CopilotWholeJobRerunPreparationV1 || !capabilities.ContainerdOfflineV1 || !capabilities.ContainerdWholeJobRetryV1 || !capabilities.ContainerdRetryApprovalV1 || capabilities.ExecutionMode != taskqueue.RuntimeContainerdOfflineV1 {
		return ErrCopilotRerunForbidden
	}
	return nil
}

func validateCopilotRerunSealMaterial(v *CopilotRerunSealedPayload, saved *model.CopilotRerunIntent, snapshot *model.CopilotDefinitionSnapshot, evidence *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, job *model.JobRun, in CopilotRerunSealInput) error {
	if v == nil || v.Payload == nil || v.Binding == nil || validateCopilotRerunReviewedTarget(v.Reviewed, snapshot, evidence, ids, saved.OwnerUserID) != nil {
		return ErrCopilotRerunInvalid
	}
	if v.Reviewed.RunContext != saved.ReviewedRunContext || v.Reviewed.RunContextDigest != saved.ReviewedRunContextDigest || v.Reviewed.AdmissionPolicyDigest != saved.ReviewedAdmissionPolicyDigest {
		return ErrCopilotRerunConflict
	}
	if err := validateCopilotRerunSealIdentity(in.SealID, saved, evidence, ids, in.AgentID); err != nil {
		return err
	}
	p, b := v.Payload, v.Binding
	digest, err := CopilotRerunPayloadDigest(p)
	if err != nil || b.PayloadDigest != digest || b.SealID != in.SealID || b.AgentID != in.AgentID || b.ScheduleID != ids.ScheduleID || b.RunID != ids.RunID || b.JobRunID != ids.JobRunID || b.ExpiresAtUnixMilli != saved.ExpiresAt.UnixMilli() || b.SecretVersionsDigest != CopilotRerunEmptySecretVersionsDigest() || b.CiphertextDigest != rerunCipherDigest(v.Ciphertext) || len(v.Ciphertext) < CopilotRerunMinCiphertextBytes || len(v.Ciphertext) > CopilotRerunMaxDefinitionBytes {
		return ErrCopilotRerunInvalid
	}
	if _, err := b.Digest(); err != nil {
		return err
	}
	if p.PipelineID != saved.PipelineID || p.PipelineRunID != ids.RunID || p.JobRunID != ids.JobRunID || p.JobName != snapshot.JobName || p.AgentID != in.AgentID || !p.RetryApprovalRequired || p.RetryApprovalTimeoutSeconds != v.Reviewed.Layout.ApprovalTimeoutSeconds || len(p.ArtifactURIs) != 0 || len(p.ArtifactReferences) != 0 || p.Source == nil || p.Source.Commit != "" || p.Source.Type != job.SourceType || p.Source.Repo != job.SourceRepository || p.Source.Branch != job.SourceRef || p.Source.BindingDigest != job.SourceBindingDigest || p.Runtime == nil || p.Runtime.Type != job.RuntimeType || p.Runtime.Image != job.RuntimeImage || p.Runtime.Resources.CPU != job.RuntimeCPU || p.Runtime.Resources.Memory != job.RuntimeMemory || p.SourceExpectation == nil {
		return ErrCopilotRerunInvalid
	}
	if len(p.Source.Auth) != 0 {
		return ErrCopilotRerunUnsupported
	}
	expectation := p.SourceExpectation
	if expectation.Version != 1 || expectation.ScheduleID != ids.ScheduleID || expectation.SealID != in.SealID || expectation.ExpectedSourceCommit != saved.ExpectedSourceCommit {
		return ErrCopilotRerunInvalid
	}
	runtimeDigest, err := taskqueue.RuntimeBindingDigest(p.PipelineID, p.PipelineRunID, p.JobRunID, p.AgentID, p.Runtime)
	if err != nil || runtimeDigest != p.Runtime.BindingDigest || taskqueue.ValidateJobRetryPlan(p) != nil || len(p.Steps) != len(ids.Steps) {
		return ErrCopilotRerunInvalid
	}
	policy, _ := json.Marshal(v.Reviewed.Layout.Retry)
	actualPolicy, _ := json.Marshal(p.Retry)
	if string(policy) != string(actualPolicy) {
		return ErrCopilotRerunInvalid
	}
	for i, step := range p.Steps {
		fixed, layout := ids.Steps[i], v.Reviewed.Layout.Steps[i]
		if step.StepRunID != fixed.Attempts[0] || !reflect.DeepEqual(step.AttemptRunIDs, fixed.Attempts) || step.Name != fixed.Name || step.StepIndex != fixed.Index || step.Uses != layout.Uses || step.Action != layout.Action || step.ContinueOnError {
			return ErrCopilotRerunInvalid
		}
	}
	return nil
}

func writeCopilotRerunSeal(ctx context.Context, tx *gorm.DB, scope *CopilotRerunScope, steps *[]model.StepRun, schedule *model.CopilotRerunSchedule, gate *model.CopilotRerunAdmissionGate, material *CopilotRerunSealedPayload, now time.Time) error {
	job, p := scope.Job, material.Payload
	if !rerunGateProofMatches(tx, gate.GateID, job.PipelineRunID, job.JobRunID, job.CopilotRerunIntentID) || requireExecutionScope(tx, job) != nil || scope.Run.Env != "{}" || scope.Run.Status != model.PipelineStatusPending || scope.Run.StartTime != nil || scope.Run.EndTime != nil || job.Env != "{}" || job.ArtifactReferences != "{}" || job.ArtifactURIs != "{}" || job.Status != model.JobRunStatusPending || job.AgentID != "" || job.ClaimID != "" || job.CancellationClaimID != "" || job.TerminalReportDigest != "" || job.StartTime != nil || job.EndTime != nil || job.RuntimeBindingDigest != "" || job.SourceResolvedCommit != "" || job.SourceProvenanceState != "unknown" || job.InputManifestState != "unknown" || job.InputExpectation != "" || job.InputManifest != "" || job.InputManifestDigest != "" || job.RetryApprovalID != "" || job.RetryApprovalEvidenceHash != "" || job.CurrentGenerationID != "" || job.CurrentGeneration != 0 || job.RetryPlan != "" || job.RetryPlanDigest != "" {
		return ErrCopilotRerunConflict
	}
	plan, err := p.RetryPlan.Canonical()
	if err != nil {
		return ErrCopilotRerunInvalid
	}
	manifest, err := taskqueue.NewJobInputManifest("", nil, nil)
	if err != nil {
		return ErrCopilotRerunInvalid
	}
	expectation, err := manifest.Canonical()
	if err != nil {
		return ErrCopilotRerunInvalid
	}
	result := tx.WithContext(ctx).Model(&model.JobRun{}).Where("id = ? AND job_run_id = ? AND copilot_rerun_gate_id = ? AND copilot_rerun_intent_id = ? AND status = ? AND agent_id = ? AND claim_id = ?", job.ID, job.JobRunID, gate.GateID, job.CopilotRerunIntentID, model.JobRunStatusPending, "", "").Updates(map[string]any{"agent_id": p.AgentID, "runtime_binding_digest": p.Runtime.BindingDigest, "retry_plan": plan, "retry_plan_digest": p.RetryPlanDigest, "input_expectation": expectation})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCopilotRerunConflict
	}
	for _, step := range *steps {
		if step.AgentID != "" || step.ClaimID != "" || step.Status != model.JobRunStatusPending || step.PipelineRunID != job.PipelineRunID || step.JobRunID != job.JobRunID || step.PipelineID != job.PipelineID || step.StartTime != nil || step.EndTime != nil || step.TerminalReportDigest != "" || step.Env != "{}" || step.Secrets != "{}" || step.Args != "{}" {
			return ErrCopilotRerunConflict
		}
		result = tx.WithContext(ctx).Model(&model.StepRun{}).Where("id = ? AND step_run_id = ? AND job_run_id = ? AND pipeline_run_id = ? AND status = ? AND agent_id = ? AND claim_id = ?", step.ID, step.StepRunID, job.JobRunID, job.PipelineRunID, model.JobRunStatusPending, "", "").Update("agent_id", p.AgentID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrCopilotRerunConflict
		}
	}
	digest, _ := material.Binding.Digest()
	result = tx.WithContext(ctx).Model(&model.CopilotRerunAdmissionGate{}).Where("id = ? AND gate_id = ? AND version = ? AND state = ? AND seal_id = ? AND claim_id = ?", gate.ID, gate.GateID, gate.Version, "unclaimed", "", "").Updates(map[string]any{"agent_id": p.AgentID, "seal_id": material.Binding.SealID, "seal_digest": digest, "version": gate.Version + 1})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCopilotRerunConflict
	}
	result = tx.WithContext(ctx).Model(&model.CopilotRerunSchedule{}).Where("id = ? AND schedule_id = ? AND version = ? AND state = ? AND lease_owner = ? AND lease_epoch = ? AND cancel_requested = ? AND claimed_id = ? AND seal_id IS NULL", schedule.ID, schedule.ScheduleID, schedule.Version, CopilotRerunSchedulePending, schedule.LeaseOwner, schedule.LeaseEpoch, false, "").Updates(map[string]any{"state": CopilotRerunScheduleSealed, "version": schedule.Version + 1, "agent_id": p.AgentID, "seal_id": material.Binding.SealID, "seal_digest": digest, "payload_digest": material.Binding.PayloadDigest, "cipher_version": material.Binding.CipherVersion, "payload_ciphertext": material.Ciphertext, "ciphertext_digest": material.Binding.CiphertextDigest, "secret_versions_digest": material.Binding.SecretVersionsDigest, "sealed_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCopilotRerunConflict
	}
	return nil
}

// Seal IDs cannot alias any frozen accepted identity. Legacy source IDs retain
// their spelling; equality here only rejects collisions, never adopts aliases.
func validateCopilotRerunSealIdentity(seal string, saved *model.CopilotRerunIntent, evidence *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, agent string) error {
	if !rerunUUID(seal) {
		return ErrCopilotRerunInvalid
	}
	originals := []string{saved.IntentID, saved.SnapshotID, saved.OwnerUserID, saved.ProjectID, saved.PipelineID, ids.GateID, ids.ScheduleID, ids.RunID, ids.JobRunID, agent, evidence.Identity.PipelineID, evidence.Identity.PipelineRunID, evidence.Identity.JobRunID, evidence.Identity.AgentID, evidence.Identity.ClaimID}
	for _, step := range ids.Steps {
		originals = append(originals, step.Attempts...)
	}
	for _, id := range originals {
		if strings.EqualFold(seal, id) {
			return ErrCopilotRerunInvalid
		}
	}
	return nil
}
