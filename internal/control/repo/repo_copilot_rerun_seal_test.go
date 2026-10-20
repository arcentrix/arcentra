// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func rerunSealFixture(t *testing.T) (*Repositories, *model.CopilotRerunIntent, CopilotRerunLeaseToken, CopilotRerunSealInput) {
	t.Helper()
	r, saved, confirm := rerunConfirmFixture(t)
	ctx := context.Background()
	saved, err := r.CopilotRerunConfirm(ctx, confirm, permitRerun, testRerunReview)
	require.NoError(t, err)
	db := r.database.Database()
	require.NoError(t, db.AutoMigrate(&model.Agent{}))
	now := time.Now().UTC().Truncate(time.Millisecond)
	caps, _ := json.Marshal(taskqueue.AgentRuntimeCapabilities{ExecutionMode: taskqueue.RuntimeContainerdOfflineV1, ContainerdOfflineV1: true, ContainerdWholeJobRetryV1: true, ContainerdRetryApprovalV1: true, CopilotWholeJobRerunPreparationV1: true})
	require.NoError(t, db.Create(&model.Agent{AgentID: "sealed-agent", OS: "linux", IsEnabled: 1, Status: 1, LastHeartbeat: &now, RuntimeCapabilities: caps}).Error)
	lease, err := r.CopilotRerunAcquireLease(ctx, saved.ScheduleID, "controller", time.Minute)
	require.NoError(t, err)
	return r, saved, *lease, CopilotRerunSealInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, AgentID: "sealed-agent", SealID: uuid.NewString(), ExpectedIntentDigest: saved.IntentDigest, ExpectedReviewedRunContextDigest: saved.ReviewedRunContextDigest, ExpectedIntentVersion: saved.Version, ExpectedGateVersion: 1, ExpectedScheduleVersion: 2}
}
func testRerunSealBuilder(ctx context.Context, view *Repositories, scope *CopilotRerunScope, snapshot *model.CopilotDefinitionSnapshot, evidence *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, intent *model.CopilotRerunIntent, agent *model.Agent, sealID string) (*CopilotRerunSealedPayload, error) {
	reviewed, err := testRerunReview(ctx, view, scope, intent.OwnerUserID, snapshot, evidence, ids)
	if err != nil {
		return nil, err
	}
	runtime := reviewed.Layout.Runtime
	p := &taskqueue.JobRunTaskPayload{PipelineID: intent.PipelineID, PipelineRunID: ids.RunID, JobRunID: ids.JobRunID, JobName: snapshot.JobName, AgentID: agent.AgentID, Runtime: &runtime, Retry: reviewed.Layout.Retry, RetryApprovalRequired: true, RetryApprovalTimeoutSeconds: reviewed.Layout.ApprovalTimeoutSeconds, Source: &taskqueue.SourcePayload{Type: scope.Job.SourceType, Repo: scope.Job.SourceRepository, Branch: scope.Job.SourceRef, BindingDigest: scope.Job.SourceBindingDigest}, SourceExpectation: &taskqueue.SourceCheckoutExpectation{Version: 1, ScheduleID: ids.ScheduleID, SealID: sealID, ExpectedSourceCommit: intent.ExpectedSourceCommit}}
	runtime.BindingDigest, err = taskqueue.RuntimeBindingDigest(p.PipelineID, p.PipelineRunID, p.JobRunID, p.AgentID, &runtime)
	if err != nil {
		return nil, err
	}
	for i, s := range ids.Steps {
		p.Steps = append(p.Steps, taskqueue.StepPayload{Name: s.Name, StepIndex: s.Index, StepRunID: s.Attempts[0], AttemptRunIDs: s.Attempts, Uses: reviewed.Layout.Steps[i].Uses, Action: reviewed.Layout.Steps[i].Action, Args: map[string]any{"script": "echo sealed"}})
	}
	p.RetryPlan, err = taskqueue.NewJobRetryPlan(p)
	if err != nil {
		return nil, err
	}
	p.RetryPlanDigest, err = p.RetryPlan.Digest(p.PipelineID, p.PipelineRunID, p.JobRunID, p.AgentID, runtime.BindingDigest)
	if err != nil {
		return nil, err
	}
	digest, err := CopilotRerunPayloadDigest(p)
	if err != nil {
		return nil, err
	}
	binding := &CopilotRerunSealBinding{Version: 1, SealID: sealID, ScheduleID: ids.ScheduleID, RunID: ids.RunID, JobRunID: ids.JobRunID, AgentID: agent.AgentID, PayloadDigest: digest, CipherVersion: 1, SecretVersionsDigest: CopilotRerunEmptySecretVersionsDigest(), ExpiresAtUnixMilli: intent.ExpiresAt.UnixMilli()}
	content, err := binding.ContentBinding()
	if err != nil {
		return nil, err
	}
	ciphertext := []byte(strings.Repeat("synthetic-encrypted-payload", 3))
	binding, err = FinalizeCopilotRerunSeal(binding, content, ciphertext)
	if err != nil {
		return nil, err
	}
	return &CopilotRerunSealedPayload{reviewed, p, binding, ciphertext}, nil
}
func TestCopilotRerunSealInertFixedAgentAndReplay(t *testing.T) {
	r, saved, lease, in := rerunSealFixture(t)
	ctx := context.Background()
	db := r.database.Database()
	receipt, err := r.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
	require.NoError(t, err)
	replay, err := r.CopilotRerunSeal(ctx, lease, in, permitRerun, func(context.Context, *Repositories, *CopilotRerunScope, *model.CopilotDefinitionSnapshot, *CopilotRerunOriginalEvidence, *CopilotRerunExecutionIdentities, *model.CopilotRerunIntent, *model.Agent, string) (*CopilotRerunSealedPayload, error) {
		t.Fatal("replay invoked builder")
		return nil, nil
	})
	require.NoError(t, err)
	require.Equal(t, receipt, replay)
	var job model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
	require.Equal(t, in.AgentID, job.AgentID)
	require.Empty(t, job.ClaimID)
	require.Empty(t, job.InputManifest)
	require.Empty(t, job.SourceResolvedCommit)
	require.Equal(t, "unknown", job.InputManifestState)
	require.Empty(t, job.RetryApprovalID)
	require.Equal(t, "{}", job.Env)
	var steps []model.StepRun
	require.NoError(t, db.Where("job_run_id = ?", job.JobRunID).Find(&steps).Error)
	for _, step := range steps {
		require.Equal(t, in.AgentID, step.AgentID)
		require.Equal(t, "{}", step.Args)
		require.Equal(t, "{}", step.Secrets)
		require.Equal(t, model.JobRunStatusPending, step.Status)
	}
	require.Error(t, executionTransaction(ctx, db, job.JobRunID, func(tx *gorm.DB) error {
		_, err := lockExecutionJob(ctx, tx, job.JobRunID, executionLockActive)
		return err
	}))
	in.SealID = uuid.NewString()
	_, err = r.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
	require.ErrorIs(t, err, ErrCopilotRerunConflict)
}

func TestCopilotRerunSealRejectsStaleLeasePolicyExpiryAndAuthority(t *testing.T) {
	for _, scenario := range []string{"lease-epoch", "lease-holder", "lease-deadline", "gate-version", "schedule-version", "revoked", "capability", "expired-during-builder", "policy", "config", "source-auth", "cancelled-schedule"} {
		t.Run(scenario, func(t *testing.T) {
			r, saved, lease, in := rerunSealFixture(t)
			db := r.database.Database()
			ctx := context.Background()
			auth := CopilotRerunAuthorize(permitRerun)
			build := CopilotRerunSealBuilder(testRerunSealBuilder)
			switch scenario {
			case "lease-epoch":
				lease.Epoch++
			case "lease-holder":
				lease.Holder = "other"
			case "lease-deadline":
				lease.Until = lease.Until.Add(time.Second)
			case "gate-version":
				in.ExpectedGateVersion++
			case "schedule-version":
				in.ExpectedScheduleVersion++
			case "revoked":
				auth = func(context.Context, *Repositories, *CopilotRerunScope, string, string) error {
					return errors.New("revoked")
				}
			case "config":
				require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", saved.PipelineID).Update("default_branch", "changed").Error)
			case "source-auth":
				build = func(ctx context.Context, v *Repositories, s *CopilotRerunScope, d *model.CopilotDefinitionSnapshot, e *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, i *model.CopilotRerunIntent, a *model.Agent, seal string) (*CopilotRerunSealedPayload, error) {
					out, err := testRerunSealBuilder(ctx, v, s, d, e, ids, i, a, seal)
					if err == nil {
						out.Payload.Source.Auth = map[string]string{"token": "forbidden"}
					}
					return out, err
				}
			case "capability":
				require.NoError(t, db.Model(&model.Agent{}).Where("agent_id = ?", in.AgentID).Update("runtime_capabilities", `{}`).Error)
			case "cancelled-schedule":
				require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("cancel_requested", true).Error)
			case "expired-during-builder":
				until := time.Now().UTC().Truncate(time.Millisecond).Add(150 * time.Millisecond)
				lease.Until = until
				require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("lease_until", until).Error)
				build = func(ctx context.Context, v *Repositories, s *CopilotRerunScope, d *model.CopilotDefinitionSnapshot, e *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, i *model.CopilotRerunIntent, a *model.Agent, seal string) (*CopilotRerunSealedPayload, error) {
					time.Sleep(time.Until(until) + 10*time.Millisecond)
					return testRerunSealBuilder(ctx, v, s, d, e, ids, i, a, seal)
				}
			case "policy":
				build = func(ctx context.Context, v *Repositories, s *CopilotRerunScope, d *model.CopilotDefinitionSnapshot, e *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, i *model.CopilotRerunIntent, a *model.Agent, seal string) (*CopilotRerunSealedPayload, error) {
					out, err := testRerunSealBuilder(ctx, v, s, d, e, ids, i, a, seal)
					if err == nil {
						out.Reviewed.AdmissionPolicyDigest = rerunDigest("changed", true)
					}
					return out, err
				}
			}
			_, err := r.CopilotRerunSeal(ctx, lease, in, auth, build)
			require.Error(t, err)
			var job model.JobRun
			require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
			require.Empty(t, job.AgentID)
			require.Empty(t, job.RetryPlan)
			var gate model.CopilotRerunAdmissionGate
			require.NoError(t, db.Where("gate_id = ?", saved.GateID).First(&gate).Error)
			require.Empty(t, gate.SealID)
			require.EqualValues(t, 1, gate.Version)
		})
	}
}
func TestCopilotRerunSealLostCommitAcknowledgementInspection(t *testing.T) {
	r, saved, lease, in := rerunSealFixture(t)
	ctx := context.Background()
	db := r.database.Database()
	pool, err := db.DB()
	require.NoError(t, err)
	lose := &atomic.Bool{}
	lose.Store(true)
	wrapped := db.Session(&gorm.Session{NewDB: true, Initialized: true})
	wrapped.Statement.ConnPool = historyLostCommitPool{ConnPool: pool, pool: pool, lose: lose}
	uncertain := &Repositories{database: statusTestDB{wrapped}}
	receipt, err := uncertain.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
	require.Nil(t, receipt)
	require.ErrorIs(t, err, ErrCopilotRerunUnavailable)
	observed, err := r.CopilotRerunInspectSeal(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
	require.NoError(t, err)
	require.Equal(t, in.SealID, observed.Binding.SealID)
	var schedule model.CopilotRerunSchedule
	require.NoError(t, db.Where("schedule_id = ?", saved.ScheduleID).First(&schedule).Error)
	require.Equal(t, lease.Epoch, schedule.LeaseEpoch)
	cancel := CopilotRerunTargetCancelInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, ExpectedIntentDigest: saved.IntentDigest, ExpectedIntentVersion: saved.Version, ExpectedGateVersion: 2, ExpectedScheduleVersion: 3}
	_, err = r.CopilotRerunCancelUnclaimed(ctx, cancel, permitRerun)
	require.NoError(t, err)
	observed, err = r.CopilotRerunInspectSeal(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
	require.NoError(t, err)
	require.Equal(t, CopilotRerunScheduleCancelled, observed.State)
	require.Equal(t, in.SealID, observed.Binding.SealID)
	replay, err := r.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
	require.NoError(t, err)
	require.Equal(t, observed, replay)

	require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", saved.NewJobRunID).Update("runtime_binding_digest", "corrupt").Error)
	_, err = r.CopilotRerunInspectSeal(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
	require.ErrorIs(t, err, ErrCopilotRerunConflict)
}

func TestCopilotRerunGateProofExactTransactionAndOrdinaryRefusal(t *testing.T) {
	r, saved, _, _ := rerunSealFixture(t)
	db := r.database.Database()
	ctx := context.Background()
	var captured any
	require.ErrorIs(t, executionTransaction(ctx, db, saved.NewJobRunID, func(tx *gorm.DB) error {
		_, err := lockExecutionJob(ctx, tx, saved.NewJobRunID, executionLockRerunSeal)
		return err
	}), ErrCopilotRerunUnsupported)
	require.NoError(t, executionTransaction(ctx, db, saved.NewJobRunID, func(tx *gorm.DB) error {
		_, err := lockCopilotRerunTargetGate(ctx, tx, saved)
		require.NoError(t, err)
		captured, _ = tx.Statement.Settings.Load(rerunGateLockProofKey)
		_, err = lockExecutionJob(ctx, tx, saved.NewJobRunID, executionLockActive)
		require.Error(t, err, "ordinary path must refuse even with gate proof")
		_, err = lockExecutionJob(ctx, tx, saved.NewJobRunID, executionLockRerunSeal)
		return err
	}))
	require.ErrorIs(t, executionTransaction(ctx, db, saved.NewJobRunID, func(tx *gorm.DB) error {
		tx.Statement.Settings.Store(rerunGateLockProofKey, captured)
		_, err := lockExecutionJob(ctx, tx, saved.NewJobRunID, executionLockRerunSeal)
		return err
	}), ErrCopilotRerunUnsupported)
	require.NoError(t, db.Model(&model.CopilotRerunAdmissionGate{}).Where("gate_id = ?", saved.GateID).Update("schedule_id", uuid.NewString()).Error)
	require.ErrorIs(t, executionTransaction(ctx, db, saved.NewJobRunID, func(tx *gorm.DB) error { _, err := lockCopilotRerunTargetGate(ctx, tx, saved); return err }), ErrCopilotRerunConflict)
}
func TestCopilotRerunCancelUnclaimedBeforeAndAfterSeal(t *testing.T) {
	for _, sealFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel-first", true: "seal-first"}[sealFirst], func(t *testing.T) {
			r, saved, lease, in := rerunSealFixture(t)
			ctx := context.Background()
			cancel := CopilotRerunTargetCancelInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, ExpectedIntentDigest: saved.IntentDigest, ExpectedIntentVersion: saved.Version, ExpectedGateVersion: 1, ExpectedScheduleVersion: 2}
			if sealFirst {
				_, err := r.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
				require.NoError(t, err)
				cancel.ExpectedGateVersion++
				cancel.ExpectedScheduleVersion++
			}
			receipt, err := r.CopilotRerunCancelUnclaimed(ctx, cancel, permitRerun)
			require.NoError(t, err)
			replay, err := r.CopilotRerunCancelUnclaimed(ctx, cancel, permitRerun)
			require.NoError(t, err)
			require.Equal(t, receipt, replay)
			observed, err := r.CopilotRerunInspectSeal(ctx, saved.OwnerUserID, saved.ProjectID, saved.IntentID, permitRerun)
			require.NoError(t, err)
			require.Equal(t, CopilotRerunScheduleCancelled, observed.State)
			sealedReplay, err := r.CopilotRerunSeal(ctx, lease, in, permitRerun, func(context.Context, *Repositories, *CopilotRerunScope, *model.CopilotDefinitionSnapshot, *CopilotRerunOriginalEvidence, *CopilotRerunExecutionIdentities, *model.CopilotRerunIntent, *model.Agent, string) (*CopilotRerunSealedPayload, error) {
				t.Fatal("historical replay must not build")
				return nil, nil
			})
			if sealFirst {
				require.NoError(t, err)
				require.Equal(t, observed, sealedReplay)
				require.Equal(t, in.SealID, observed.Binding.SealID)
			} else {
				require.Error(t, err)
				require.Empty(t, observed.Binding.SealID)
			}
			var run model.PipelineRun
			var job model.JobRun
			require.NoError(t, r.database.Database().Where("run_id = ?", saved.NewRunID).First(&run).Error)
			require.NoError(t, r.database.Database().Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
			require.Equal(t, model.PipelineStatusCancelled, run.Status)
			require.Equal(t, 1, run.CompletedJobs)
			require.Zero(t, run.RunningJobs)
			require.Nil(t, run.StartTime)
			require.Equal(t, receipt.CancelledAt, *run.EndTime)
			require.Equal(t, model.JobRunStatusCancelled, job.Status)
			require.Equal(t, job.TotalSteps, job.CompletedSteps)
			require.Empty(t, job.ClaimID)
			require.Nil(t, job.StartTime)
			require.Equal(t, receipt.CancelledAt, *job.EndTime)
		})
	}
}
func TestCopilotRerunSealIdentityPrecedesPayloadDigest(t *testing.T) {
	in := CopilotRerunSealBinding{Version: 1, ScheduleID: uuid.NewString(), RunID: uuid.NewString(), JobRunID: uuid.NewString(), AgentID: "agent", CipherVersion: 1, SecretVersionsDigest: CopilotRerunEmptySecretVersionsDigest(), ExpiresAtUnixMilli: time.Now().Add(time.Hour).UnixMilli()}
	identity, err := NewCopilotRerunSealIdentity(in)
	require.NoError(t, err)
	require.True(t, rerunUUID(identity.SealID))
	require.Empty(t, identity.PayloadDigest)
	_, err = identity.ContentBinding()
	require.Error(t, err)
	metadata, err := BindCopilotRerunSealPayload(identity, rerunDigest("payload", true))
	require.NoError(t, err)
	require.Equal(t, identity.SealID, metadata.SealID)
	_, err = BindCopilotRerunSealPayload(metadata, rerunDigest("changed", true))
	require.Error(t, err)
	in.SealID = uuid.NewString()
	_, err = NewCopilotRerunSealIdentity(in)
	require.Error(t, err)
}

func TestCopilotRerunSealRollbackAfterAssignments(t *testing.T) {
	r, saved, lease, in := rerunSealFixture(t)
	db := r.database.Database()
	const name = "seal-rollback"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CopilotRerunSchedule" {
			tx.AddError(errors.New("synthetic failure"))
		}
	}))
	_, err := r.CopilotRerunSeal(context.Background(), lease, in, permitRerun, testRerunSealBuilder)
	require.NoError(t, db.Callback().Update().Remove(name))
	require.ErrorIs(t, err, ErrCopilotRerunUnavailable)
	observed, err := r.CopilotRerunInspectSeal(context.Background(), in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
	require.NoError(t, err)
	require.Equal(t, CopilotRerunSchedulePending, observed.State)
	var steps []model.StepRun
	require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).Find(&steps).Error)
	for _, step := range steps {
		require.Empty(t, step.AgentID)
	}
}
func TestCopilotRerunCancelUnclaimedRequiresCurrentPermission(t *testing.T) {
	r, saved, _, _ := rerunSealFixture(t)
	in := CopilotRerunTargetCancelInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, ExpectedIntentDigest: saved.IntentDigest, ExpectedIntentVersion: saved.Version, ExpectedGateVersion: 1, ExpectedScheduleVersion: 2}
	_, err := r.CopilotRerunCancelUnclaimed(context.Background(), in, func(context.Context, *Repositories, *CopilotRerunScope, string, string) error {
		return errors.New("revoked")
	})
	require.ErrorIs(t, err, ErrCopilotRerunForbidden)
	var job model.JobRun
	require.NoError(t, r.database.Database().Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
	require.Equal(t, model.JobRunStatusPending, job.Status)
}

func TestCopilotRerunSealReceiptAndCancelSurviveScopeArchive(t *testing.T) {
	for _, field := range []string{"pipeline-disabled", "pipeline-deleted", "project-disabled", "project-archived"} {
		t.Run(field, func(t *testing.T) {
			r, saved, lease, in := rerunSealFixture(t)
			ctx := context.Background()
			_, err := r.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
			require.NoError(t, err)
			db := r.database.Database()
			switch field {
			case "pipeline-disabled":
				require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", saved.PipelineID).Update("is_enabled", 0).Error)
			case "pipeline-deleted":
				require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", saved.PipelineID).Update("is_deleted", 1).Error)
			case "project-disabled":
				require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", saved.ProjectID).Update("is_enabled", 0).Error)
			case "project-archived":
				require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", saved.ProjectID).Update("status", model.ProjectStatusArchived).Error)
			}
			observed, err := r.CopilotRerunInspectSeal(ctx, saved.OwnerUserID, saved.ProjectID, saved.IntentID, permitRerun)
			require.NoError(t, err)
			require.Equal(t, in.SealID, observed.Binding.SealID)
			deny := func(context.Context, *Repositories, *CopilotRerunScope, string, string) error {
				return ErrCopilotRerunForbidden
			}
			_, err = r.CopilotRerunInspectSeal(ctx, saved.OwnerUserID, saved.ProjectID, saved.IntentID, deny)
			require.ErrorIs(t, err, ErrCopilotRerunForbidden)
			cancel := CopilotRerunTargetCancelInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, ExpectedIntentDigest: saved.IntentDigest, ExpectedIntentVersion: saved.Version, ExpectedGateVersion: 2, ExpectedScheduleVersion: 3}
			_, err = r.CopilotRerunCancelUnclaimed(ctx, cancel, deny)
			require.ErrorIs(t, err, ErrCopilotRerunForbidden)
			_, err = r.CopilotRerunCancelUnclaimed(ctx, cancel, permitRerun)
			require.NoError(t, err)
			_, err = r.CopilotRerunInspectSeal(ctx, saved.OwnerUserID, saved.ProjectID, saved.IntentID, permitRerun)
			require.NoError(t, err)
		})
	}
}
func TestCopilotRerunSealIdentityRejectsFrozenAliases(t *testing.T) {
	_, saved, _, _ := rerunSealFixture(t)
	var ids CopilotRerunExecutionIdentities
	var evidence CopilotRerunOriginalEvidence
	require.NoError(t, json.Unmarshal([]byte(saved.StepIdentitiesJSON), &ids))
	require.NoError(t, json.Unmarshal([]byte(saved.OriginalEvidenceJSON), &evidence))
	for _, id := range []string{saved.IntentID, saved.SnapshotID, ids.GateID, ids.RunID, ids.JobRunID, ids.ScheduleID, ids.Steps[0].Attempts[0]} {
		require.Error(t, validateCopilotRerunSealIdentity(id, saved, &evidence, &ids, "agent"))
	}
	fresh := uuid.NewString()
	evidence.Identity.ClaimID = strings.ToUpper(fresh)
	require.Error(t, validateCopilotRerunSealIdentity(fresh, saved, &evidence, &ids, "agent"))
	require.NoError(t, validateCopilotRerunSealIdentity(uuid.NewString(), saved, &evidence, &ids, "agent"))
}

func TestCopilotRerunSealRejectsInactiveScopeAndUnknownAction(t *testing.T) {
	r, saved, lease, in := rerunSealFixture(t)
	db := r.database.Database()
	require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", saved.ProjectID).Update("status", model.ProjectStatusArchived).Error)
	_, err := r.CopilotRerunSeal(context.Background(), lease, in, permitRerun, testRerunSealBuilder)
	require.ErrorIs(t, err, ErrCopilotRerunForbidden)
	_, _, err = lockCopilotRerunSealScope(context.Background(), db, saved, "unknown", permitRerun)
	require.ErrorIs(t, err, ErrCopilotRerunInvalid)
}
