// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Capture the upload layout prospectively; no existing immutable snapshot or
// claimed target is edited to manufacture output eligibility.
func rerunOutputFixture(t *testing.T) (*Repositories, *model.CopilotRerunIntent, taskqueue.JobInputIdentity, context.Context, *model.ProjectArtifact) {
	t.Helper()
	ctx := context.Background()
	repository, run, originalSnapshot := rerunCaptureFixture(t)
	_, input, now := rerunValueFixture(t)
	input.RepositoryConfigDigest = originalSnapshot.RepositoryConfigDigest
	input.Layout.Steps = append(input.Layout.Steps, CopilotRerunStepLayout{Name: "publish", Index: 1, Uses: "builtin:artifacts", Action: "upload"})
	snapshot, err := newTestCopilotDefinitionSnapshot(run, input, now)
	require.NoError(t, err)
	db := repository.Database()
	require.NoError(t, db.AutoMigrate(&model.CopilotRerunIntent{}, &model.StepRun{}, &model.CopilotRerunAdmissionGate{}, &model.CopilotRerunSchedule{}, &model.Agent{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}, &model.JobRunGeneration{}, &model.TeamMember{}, &model.Team{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Role{}, &model.RoleGrant{}, &model.ProjectArtifact{}))
	require.NoError(t, repository.CreateVerifiedManualTriggerRunWithDefinition(ctx, "actor", run, snapshot, permitManualInbox))
	_, terminal, original, _ := rerunTerminalEvidenceFixture(t)
	runtime := repoRuntimeRequest(t)
	payload := &taskqueue.JobRunTaskPayload{PipelineID: original.PipelineID, PipelineRunID: original.PipelineRunID, JobRunID: original.JobRunID, AgentID: original.AgentID, JobName: original.JobName, Runtime: runtime, Retry: input.Layout.Retry, RetryApprovalRequired: true, RetryApprovalTimeoutSeconds: 300, Steps: []taskqueue.StepPayload{
		{Name: "compile", StepIndex: 0, StepRunID: "old-step-1", AttemptRunIDs: []string{"old-step-1", "old-step-2"}, Uses: "shell", Args: map[string]any{"run": "exit 9"}},
		{Name: "publish", StepIndex: 1, StepRunID: "old-upload-1", AttemptRunIDs: []string{"old-upload-1", "old-upload-2"}, Uses: "builtin:artifacts", Action: "upload", Args: map[string]any{"path": "dist/result.txt"}},
	}}
	plan, err := taskqueue.NewJobRetryPlan(payload)
	require.NoError(t, err)
	original.RetryPlan, err = plan.Canonical()
	require.NoError(t, err)
	original.RetryPlanDigest, err = plan.Digest(original.PipelineID, original.PipelineRunID, original.JobRunID, original.AgentID, original.RuntimeBindingDigest)
	require.NoError(t, err)
	original.ArtifactReferences, original.ArtifactURIs = "{}", "{}"
	require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", run.RunID).Updates(map[string]any{"status": terminal.Status, "end_time": terminal.EndTime, "project_id": "project"}).Error)
	require.NoError(t, db.Create(original).Error)
	r := NewRepositories(statusTestDB{db}, nil)
	prepared, err := testRerunPrepare(r, ctx, CopilotRerunPrepareInput{OwnerUserID: "actor", ProjectID: "project", OriginalJobRunID: original.JobRunID, RequestID: "output-rerun", SnapshotDigest: snapshot.BindingDigest, ExpiresAt: time.Now().Add(10 * time.Minute).UTC().Truncate(time.Millisecond)}, permitRerun)
	require.NoError(t, err)
	saved, err := r.CopilotRerunConfirm(ctx, CopilotRerunConfirmInput{OwnerUserID: prepared.OwnerUserID, ProjectID: prepared.ProjectID, IntentID: prepared.IntentID, ExpectedVersion: prepared.Version, ExpectedIntentDigest: prepared.IntentDigest, ExpectedReviewedRunContextDigest: prepared.ReviewedRunContextDigest}, permitRerun, testRerunReview)
	require.NoError(t, err)
	caps, _ := json.Marshal(taskqueue.AgentRuntimeCapabilities{ExecutionMode: taskqueue.RuntimeContainerdOfflineV1, ContainerdOfflineV1: true, ContainerdWholeJobRetryV1: true, ContainerdRetryApprovalV1: true, CopilotWholeJobRerunPreparationV1: true})
	now = time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, db.Create(&model.Agent{AgentID: "sealed-agent", OS: "linux", IsEnabled: 1, Status: 1, LastHeartbeat: &now, RuntimeCapabilities: caps}).Error)
	lease, err := r.CopilotRerunAcquireLease(ctx, saved.ScheduleID, "controller", time.Minute)
	require.NoError(t, err)
	sealID := uuid.NewString()
	sealed, err := r.CopilotRerunSeal(ctx, *lease, CopilotRerunSealInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, AgentID: "sealed-agent", SealID: sealID, ExpectedIntentDigest: saved.IntentDigest, ExpectedReviewedRunContextDigest: saved.ReviewedRunContextDigest, ExpectedIntentVersion: saved.Version, ExpectedGateVersion: 1, ExpectedScheduleVersion: 2}, permitRerun, testRerunSealBuilder)
	require.NoError(t, err)
	sealDigest, err := sealed.Binding.Digest()
	require.NoError(t, err)
	claim := CopilotRerunClaimInput{AgentID: "sealed-agent", PipelineID: saved.PipelineID, RunID: saved.NewRunID, JobRunID: saved.NewJobRunID, GateID: saved.GateID, ScheduleID: saved.ScheduleID, SealID: sealID, SealDigest: sealDigest, PayloadDigest: sealed.Binding.PayloadDigest, ClaimID: uuid.NewString(), AdmissionNonce: []byte(strings.Repeat("synthetic-nonce!", 2))}
	rerunClaimStorageSeed(t, db, &claim)
	_, err = r.CopilotRerunClaim(ctx, claim, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	jobs := r.JobRun.(*JobRunRepo)
	job, err := jobs.GetByJobRunID(ctx, saved.NewJobRunID)
	require.NoError(t, err)
	identity := generationIdentity(job)
	c, err := retryapproval.ParseRunContext(saved.ReviewedRunContext)
	require.NoError(t, err)
	for _, id := range c.Policy.Candidates {
		require.NoError(t, db.Create(&model.User{UserID: id, IsEnabled: 1}).Error)
	}
	require.NoError(t, SetRetryApprovalAuthorizer(r, func(ctx context.Context, view RetryApprovalAuthorizationView, _ *retryapproval.Evidence, id, action string) error {
		u, e := view.LiveRepositories().User.(IExecutionActorReader).GetExecutionActor(ctx, id)
		if e != nil || u == nil || u.IsEnabled != 1 {
			return ErrApprovalForbidden
		}
		return nil
	}))
	t.Cleanup(func() { _ = SetRetryApprovalAuthorizer(r, nil) })
	admit := taskqueue.JobGenerationTransition{Version: 1, Identity: identity, RetryPlanDigest: job.RetryPlanDigest, Operation: "admit"}
	admit.RequestDigest, err = admit.Digest()
	require.NoError(t, err)
	gen, err := jobs.ReconcileJobGeneration(ctx, admit)
	require.NoError(t, err)
	expected := taskqueue.SourceCheckoutExpectation{Version: 1, ScheduleID: saved.ScheduleID, SealID: sealID, ExpectedSourceCommit: job.ExpectedSourceCommit}
	expectedDigest, err := expected.Digest(identity)
	require.NoError(t, err)
	manifest, err := taskqueue.NewJobInputManifest(job.ExpectedSourceCommit, nil, nil)
	require.NoError(t, err)
	job, _, err = jobs.GetOrRecordJobInputsWithSourceExpectation(ctx, identity, expectedDigest, manifest)
	require.NoError(t, err)
	_, err = r.Approval.(*ApprovalRepo).Decide(ctx, job.RetryApprovalID, c.Policy.Candidates[0], model.ApprovalDecisionApprove, "")
	require.NoError(t, err)
	_, err = jobs.ReconcileJobRetryApproval(ctx, retryapproval.Request{Version: 1, Operation: "admit", Identity: identity, GenerationID: gen.GenerationID, GenerationBindingDigest: gen.BindingDigest, RetryPlanDigest: job.RetryPlanDigest, InputManifestDigest: job.InputManifestDigest})
	require.NoError(t, err)
	reportCtx := agentidentity.WithGenerationReport(ctx, gen.GenerationID, gen.BindingDigest)
	var steps []model.StepRun
	require.NoError(t, db.Where("job_run_id = ?", job.JobRunID).Order("attempt, step_index").Find(&steps).Error)
	var upload string
	for i, step := range steps {
		stepClaim := agentidentity.StepClaimID(identity.ClaimID, step.StepRunID)
		next := model.JobRunStatusSkipped
		if step.Attempt == gen.Number {
			next = model.JobRunStatusSuccess
			require.NoError(t, r.StepRun.(*StepRunRepo).PatchReportedStatus(reportCtx, &step, map[string]any{"status": model.JobRunStatusRunning, "claim_id": stepClaim, "generation_id": gen.GenerationID, "generation_binding_digest": gen.BindingDigest, "start_time": time.Now()}))
			require.NoError(t, db.Where("step_run_id = ?", step.StepRunID).First(&step).Error)
			if step.Action == "upload" {
				upload = step.StepRunID
			}
		}
		require.NoError(t, r.StepRun.(*StepRunRepo).PatchReportedStatus(reportCtx, &step, map[string]any{"status": next, "claim_id": stepClaim, "generation_id": gen.GenerationID, "generation_binding_digest": gen.BindingDigest, "terminal_report_digest": fmt.Sprintf("%064d", i+1), "exit_code": int32(0), "end_time": time.Now()}))
	}
	require.NotEmpty(t, upload)
	artifact := &model.ProjectArtifact{ProjectID: saved.ProjectID, PipelineID: saved.PipelineID, PipelineRunID: saved.NewRunID, JobRunID: saved.NewJobRunID, StepRunID: upload, Name: "result.txt", StorageType: "s3", StorageID: claim.ArtifactStorageID, StorageFingerprint: claim.ArtifactStorageFingerprint, Size: 12, SHA256: strings.Repeat("b", 64), MediaType: "text/plain"}
	artifact.StoragePath, err = artifactutil.RemotePath(artifact.PipelineRunID, artifact.JobRunID, artifact.StepRunID, artifact.SHA256, artifact.Name)
	require.NoError(t, err)
	return r, saved, identity, reportCtx, artifact
}

func TestCopilotRerunOutputsPublishAtomicallyAndReplayReadOnly(t *testing.T) {
	r, saved, identity, ctx, artifact := rerunOutputFixture(t)
	jobs := r.JobRun.(*JobRunRepo)
	db := r.database.Database()
	job, err := jobs.GetByJobRunID(ctx, identity.JobRunID)
	require.NoError(t, err)
	updates := map[string]any{"status": model.JobRunStatusSuccess, "claim_id": identity.ClaimID, "terminal_report_digest": strings.Repeat("a", 64), "end_time": time.Now(), "source_resolved_commit": job.ExpectedSourceCommit, "source_provenance_state": "reported"}
	require.Error(t, NewArtifactRepo(r.database).Upsert(ctx, artifact), "standalone cannot publish")
	wrong := *artifact
	wrong.StorageFingerprint = strings.Repeat("c", 64)
	require.Error(t, jobs.PatchReportedStatusAndArtifacts(ctx, job, updates, []*model.ProjectArtifact{&wrong}))
	require.NoError(t, jobs.PatchReportedStatusAndArtifacts(ctx, job, updates, []*model.ProjectArtifact{artifact}))
	require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", saved.OwnerUserID).Update("is_enabled", 0).Error)
	require.NoError(t, jobs.ValidateCompletedArtifacts(ctx, job.JobRunID, []*model.ProjectArtifact{artifact}))
	require.NoError(t, jobs.ValidateCompletedArtifacts(ctx, job.JobRunID, []*model.ProjectArtifact{artifact}))
	require.Error(t, jobs.ValidateCompletedArtifacts(ctx, job.JobRunID, []*model.ProjectArtifact{artifact, artifact}))
	require.Error(t, jobs.ValidateCompletedArtifacts(ctx, job.JobRunID, []*model.ProjectArtifact{&wrong}))
	receipt, err := jobs.FinalizeCopilotRerun(ctx, identity)
	require.NoError(t, err)
	require.Equal(t, model.PipelineStatusSuccess, receipt.RunStatus)
	var count int64
	require.NoError(t, db.Model(&model.ProjectArtifact{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestCopilotRerunOutputWriteRollsBackWithTerminalFailure(t *testing.T) {
	r, _, identity, ctx, artifact := rerunOutputFixture(t)
	db := r.database.Database()
	jobs := r.JobRun.(*JobRunRepo)
	job, err := jobs.GetByJobRunID(ctx, identity.JobRunID)
	require.NoError(t, err)
	const callback = "rerun-output-terminal-failure"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "JobRun" {
			tx.AddError(ErrCopilotRerunConflict)
		}
	}))
	defer db.Callback().Update().Remove(callback)
	require.Error(t, jobs.PatchReportedStatusAndArtifacts(ctx, job, map[string]any{"status": model.JobRunStatusSuccess, "claim_id": identity.ClaimID, "terminal_report_digest": strings.Repeat("a", 64), "end_time": time.Now(), "source_resolved_commit": job.ExpectedSourceCommit, "source_provenance_state": "reported"}, []*model.ProjectArtifact{artifact}))
	var count int64
	require.NoError(t, db.Model(&model.ProjectArtifact{}).Count(&count).Error)
	require.Zero(t, count)
	current, err := jobs.GetByJobRunID(ctx, identity.JobRunID)
	require.NoError(t, err)
	require.Equal(t, model.JobRunStatusRunning, current.Status)
	require.Empty(t, current.TerminalReportDigest)
}

func TestCopilotRerunArtifactProofCannotCrossOperationOrTransaction(t *testing.T) {
	r, _, identity, ctx, artifact := rerunOutputFixture(t)
	db := r.database.Database()
	require.NoError(t, executionTransaction(ctx, db, identity.JobRunID, func(tx *gorm.DB) error {
		job, _, _, err := lockCopilotRerunOperation(ctx, tx, identity, executionLockRerunJobSuccess)
		require.NoError(t, err)
		require.NoError(t, requireCopilotRerunOperationProof(tx, job, executionLockRerunJobSuccess))
		require.Error(t, requireCopilotRerunOperationProof(tx, job, executionLockRerunJobReport))
		foreign := tx.Session(&gorm.Session{NewDB: true}).WithContext(ctx)
		foreign.Statement.ConnPool = db.Statement.ConnPool
		require.Error(t, requireCopilotRerunOperationProof(foreign, job, executionLockRerunJobSuccess))
		altered := *job
		altered.ClaimID = uuid.NewString()
		require.Error(t, requireCopilotRerunOperationProof(tx, &altered, executionLockRerunJobSuccess))
		require.Error(t, UpsertArtifact(ctx, tx, artifact), "proof without atomic publication marker is insufficient")
		wrong := *artifact
		wrong.StorageID = "another-store"
		require.Error(t, UpsertArtifact(withGenerationArtifactPublication(ctx), tx, &wrong))
		var shell model.StepRun
		require.NoError(t, tx.Where("job_run_id = ? AND uses = ? AND attempt = ?", identity.JobRunID, "shell", 1).First(&shell).Error)
		wrong = *artifact
		wrong.StepRunID = shell.StepRunID
		require.Error(t, UpsertArtifact(withGenerationArtifactPublication(ctx), tx, &wrong), "only sealed output steps may publish")
		var old model.StepRun
		require.NoError(t, tx.Where("job_run_id = ? AND action = ? AND attempt = ?", identity.JobRunID, "upload", 2).First(&old).Error)
		wrong = *artifact
		wrong.StepRunID = old.StepRunID
		require.Error(t, UpsertArtifact(withGenerationArtifactPublication(ctx), tx, &wrong), "future or old generation cannot publish")
		return nil
	}))
	var n int64
	require.NoError(t, db.Model(&model.ProjectArtifact{}).Count(&n).Error)
	require.Zero(t, n)
}

func TestCopilotRerunFinalizerRejectsChangedDurableChildReceipt(t *testing.T) {
	for _, field := range []string{"terminal_report_digest", "generation_binding_digest", "claim_id"} {
		t.Run(field, func(t *testing.T) {
			r, _, identity, ctx, artifact := rerunOutputFixture(t)
			db := r.database.Database()
			jobs := r.JobRun.(*JobRunRepo)
			job, err := jobs.GetByJobRunID(ctx, identity.JobRunID)
			require.NoError(t, err)
			require.NoError(t, jobs.PatchReportedStatusAndArtifacts(ctx, job, map[string]any{"status": model.JobRunStatusSuccess, "claim_id": identity.ClaimID, "terminal_report_digest": strings.Repeat("a", 64), "end_time": time.Now(), "source_resolved_commit": job.ExpectedSourceCommit, "source_provenance_state": "reported"}, []*model.ProjectArtifact{artifact}))
			require.NoError(t, db.Model(&model.StepRun{}).Where("step_run_id = ?", artifact.StepRunID).Update(field, "").Error)
			_, err = jobs.FinalizeCopilotRerun(ctx, identity)
			require.Error(t, err)
			var run model.PipelineRun
			require.NoError(t, db.Where("run_id = ?", identity.PipelineRunID).First(&run).Error)
			require.Equal(t, model.PipelineStatusRunning, run.Status)
			require.Nil(t, run.EndTime)
		})
	}
}
