// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCopilotRerunBootstrapRequiresFreshIndependentApproval(t *testing.T) {
	for _, scenario := range []string{"success", "final-grant-expiry", "final-approval-expiry", "final-cancel", "vote-cancel"} {
		t.Run(scenario, func(t *testing.T) {
			r, saved, identity, expected := rerunInputFixture(t)
			ctx := context.Background()
			db := r.database.Database()
			jobs := r.JobRun.(*JobRunRepo)
			require.NoError(t, db.AutoMigrate(&model.JobRunGeneration{}, &model.TeamMember{}, &model.Team{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Role{}, &model.RoleGrant{}))
			c, err := retryapproval.ParseRunContext(saved.ReviewedRunContext)
			require.NoError(t, err)
			for _, id := range c.Policy.Candidates {
				var n int64
				require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", id).Count(&n).Error)
				if n == 0 {
					require.NoError(t, db.Create(&model.User{UserID: id, IsEnabled: 1}).Error)
				}
			}
			var projectedExpiry time.Time
			require.NoError(t, SetRetryApprovalAuthorizer(r, func(ctx context.Context, view RetryApprovalAuthorizationView, _ *retryapproval.Evidence, id, action string) error {
				view.(*retryApprovalView).expires = projectedExpiry
				reader := view.LiveRepositories().User.(IExecutionActorReader)
				u, err := reader.GetExecutionActor(ctx, id)
				if err != nil || u == nil || u.UserID != id || u.IsEnabled != 1 {
					return ErrApprovalForbidden
				}
				return nil
			}))
			t.Cleanup(func() { _ = SetRetryApprovalAuthorizer(r, nil) })
			job, err := jobs.GetByJobRunID(ctx, identity.JobRunID)
			require.NoError(t, err)
			admit := taskqueue.JobGenerationTransition{Version: 1, Identity: identity, RetryPlanDigest: job.RetryPlanDigest, Operation: "admit"}
			admit.RequestDigest, err = admit.Digest()
			require.NoError(t, err)
			generation, err := jobs.ReconcileJobGeneration(ctx, admit)
			require.NoError(t, err)
			require.True(t, generation.PreparationOnly)
			var n int64
			require.NoError(t, db.Model(&model.ApprovalRequest{}).Count(&n).Error)
			require.Zero(t, n)
			digest, err := expected.Digest(identity)
			require.NoError(t, err)
			unknown, _, err := jobs.GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, nil)
			require.NoError(t, err)
			require.Empty(t, unknown.RetryApprovalID)
			manifest, err := taskqueue.NewJobInputManifest(expected.ExpectedSourceCommit, nil, nil)
			require.NoError(t, err)
			job, _, err = jobs.GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, manifest)
			require.NoError(t, err)
			request := retryapproval.Request{Version: 1, Operation: "admit", Identity: identity, GenerationID: generation.GenerationID, GenerationBindingDigest: generation.BindingDigest, RetryPlanDigest: job.RetryPlanDigest, InputManifestDigest: job.InputManifestDigest}
			denied, err := jobs.ReconcileJobRetryApproval(ctx, request)
			require.Error(t, err)
			require.Nil(t, denied)
			observe := request
			observe.Operation = "observe"
			pending, err := jobs.ReconcileJobRetryApproval(ctx, observe)
			require.NoError(t, err)
			require.Equal(t, "pending", pending.State)
			require.False(t, pending.ExecutionAllowed)
			approvals := r.Approval.(*ApprovalRepo)
			if scenario == "vote-cancel" {
				const callback = "rerun-vote-final-cancel"
				reached := false
				require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "CopilotRerunSchedule" {
						return
					}
					if _, ok := tx.Statement.Clauses["FOR"]; !ok {
						return
					}
					reached = true
					var n int64
					require.NoError(t, tx.Session(&gorm.Session{NewDB: true}).Model(&model.ApprovalDecision{}).Count(&n).Error)
					require.EqualValues(t, 1, n)
					tx.Statement.Dest.(*model.CopilotRerunSchedule).CancelRequested = true
				}))
				defer db.Callback().Query().Remove(callback)
				_, err = approvals.Decide(ctx, job.RetryApprovalID, c.Policy.Candidates[0], model.ApprovalDecisionApprove, "")
				require.Error(t, err)
				require.True(t, reached)
				var n int64
				require.NoError(t, db.Model(&model.ApprovalDecision{}).Count(&n).Error)
				require.Zero(t, n)
				var approval model.ApprovalRequest
				require.NoError(t, db.Where("approval_id = ?", job.RetryApprovalID).First(&approval).Error)
				require.Equal(t, model.ApprovalStatusPending, approval.Status)
				require.Zero(t, approval.ApprovedCount)
				return
			}

			_, err = approvals.Decide(ctx, job.RetryApprovalID, c.Policy.Candidates[0], model.ApprovalDecisionApprove, "")
			require.NoError(t, err)
			if scenario != "success" {
				const callback = "rerun-startup-final-fence"
				reached := false
				require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "CopilotRerunSchedule" {
						return
					}
					if _, ok := tx.Statement.Clauses["FOR"]; !ok {
						return
					}
					reached = true
					var staged model.JobRunGeneration
					require.NoError(t, tx.Session(&gorm.Session{NewDB: true}).Where("generation_id = ?", generation.GenerationID).First(&staged).Error)
					require.NotEmpty(t, staged.RetryApprovalAdmission)
					switch scenario {
					case "final-grant-expiry":
						time.Sleep(time.Until(projectedExpiry) + 5*time.Millisecond)
					case "final-approval-expiry":
						time.Sleep(80 * time.Millisecond)
					case "final-cancel":
						tx.Statement.Dest.(*model.CopilotRerunSchedule).CancelRequested = true
					}
				}))
				defer db.Callback().Query().Remove(callback)
				if scenario == "final-grant-expiry" {
					projectedExpiry = time.Now().Add(100 * time.Millisecond)
				}
				if scenario == "final-approval-expiry" {
					deadline := time.Now().UTC().Truncate(time.Millisecond).Add(60 * time.Millisecond)
					var approval model.ApprovalRequest
					require.NoError(t, db.Where("approval_id = ?", job.RetryApprovalID).First(&approval).Error)
					evidence, e := retryapproval.ParseEvidence(approval.EvidenceJSON)
					require.NoError(t, e)
					evidence.ExpiresAtUnixMilli = deadline.UnixMilli()
					body, e := evidence.Canonical()
					require.NoError(t, e)
					hash, e := evidence.Digest()
					require.NoError(t, e)
					require.NoError(t, db.Model(&model.ApprovalRequest{}).Where("approval_id = ?", job.RetryApprovalID).Updates(map[string]any{"expires_at": deadline, "evidence_json": body, "evidence_hash": hash}).Error)
					require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", job.JobRunID).Update("retry_approval_evidence_hash", hash).Error)
				}
				denied, err = jobs.ReconcileJobRetryApproval(ctx, request)
				require.Error(t, err)
				require.Nil(t, denied)
				require.True(t, reached)
				var after model.JobRunGeneration
				require.NoError(t, db.Where("generation_id = ?", generation.GenerationID).First(&after).Error)
				require.Empty(t, after.RetryApprovalAdmission)
				require.Equal(t, "preparing", after.State)
				return
			}
			started, err := jobs.ReconcileJobRetryApproval(ctx, request)
			require.NoError(t, err)
			require.True(t, started.ExecutionAllowed)
			require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", saved.OwnerUserID).Update("is_enabled", 0).Error)
			denied, err = jobs.ReconcileJobRetryApproval(ctx, request)
			require.Error(t, err)
			require.Nil(t, denied)
			// An exact old vote acknowledgement survives revocation without authorizing another command.
			_, err = approvals.Decide(ctx, job.RetryApprovalID, c.Policy.Candidates[0], model.ApprovalDecisionApprove, "")
			require.NoError(t, err)
			inspect := taskqueue.JobGenerationTransition{Version: 1, Identity: identity, RetryPlanDigest: job.RetryPlanDigest, Operation: "inspect"}
			inspect.RequestDigest, err = inspect.Digest()
			require.NoError(t, err)
			receipt, err := jobs.ReconcileJobGeneration(ctx, inspect)
			require.NoError(t, err)
			require.False(t, receipt.ExecutionAllowed)
			require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", saved.OwnerUserID).Update("is_enabled", 1).Error)
			reportCtx := agentidentity.WithGenerationReport(ctx, generation.GenerationID, generation.BindingDigest)
			var steps []model.StepRun
			require.NoError(t, db.Where("job_run_id = ?", job.JobRunID).Order("attempt ASC, step_index ASC").Find(&steps).Error)
			for i := range steps {
				step := steps[i]
				claim := agentidentity.StepClaimID(identity.ClaimID, step.StepRunID)
				if step.Attempt == generation.Number {
					require.NoError(t, r.StepRun.(*StepRunRepo).PatchReportedStatus(reportCtx, &step, map[string]any{"status": model.JobRunStatusRunning, "claim_id": claim, "generation_id": generation.GenerationID, "generation_binding_digest": generation.BindingDigest, "start_time": time.Now()}))
					require.NoError(t, db.Where("step_run_id = ?", step.StepRunID).First(&step).Error)
				}
				next := model.JobRunStatusSuccess
				if step.Attempt != generation.Number {
					next = model.JobRunStatusSkipped
				}
				require.NoError(t, r.StepRun.(*StepRunRepo).PatchReportedStatus(reportCtx, &step, map[string]any{"status": next, "claim_id": claim, "generation_id": generation.GenerationID, "generation_binding_digest": generation.BindingDigest, "terminal_report_digest": fmt.Sprintf("%064d", i+1), "exit_code": int32(0), "end_time": time.Now()}))
			}
			job, err = jobs.GetByJobRunID(ctx, job.JobRunID)
			require.NoError(t, err)
			require.NoError(t, jobs.PatchReportedStatus(reportCtx, job, map[string]any{"status": model.JobRunStatusSuccess, "claim_id": identity.ClaimID, "terminal_report_digest": strings.Repeat("a", 64), "end_time": time.Now(), "source_resolved_commit": expected.ExpectedSourceCommit, "source_provenance_state": "reported"}))
			job, err = jobs.GetByJobRunID(ctx, job.JobRunID)
			require.NoError(t, err)
			require.NoError(t, jobs.InspectCopilotRerunJobReceipt(ctx, job))
			terminal, err := jobs.FinalizeCopilotRerun(ctx, identity)
			require.NoError(t, err)
			require.Equal(t, model.PipelineStatusSuccess, terminal.RunStatus)
			replay, err := jobs.FinalizeCopilotRerun(ctx, identity)
			require.NoError(t, err)
			require.Equal(t, terminal, replay)
			var run model.PipelineRun
			require.NoError(t, db.Where("run_id = ?", saved.NewRunID).First(&run).Error)
			require.Equal(t, model.PipelineStatusSuccess, run.Status)
			require.Equal(t, 1, run.CompletedJobs)
			require.Zero(t, run.RunningJobs)
			var original model.JobRun
			require.NoError(t, db.Where("job_run_id = ?", saved.OriginalJobRunID).First(&original).Error)
			require.Equal(t, model.JobRunStatusFailed, original.Status)

		})
	}
}

func TestCopilotRerunExpiredWindowsAllowEvidenceAndPendingClosure(t *testing.T) {
	for _, closure := range []string{"expire", "cancel"} {
		t.Run(closure, func(t *testing.T) {
			r, saved, lease, in := rerunSealFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			db := r.database.Database()
			sealed, err := r.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
			require.NoError(t, err)
			sealDigest, err := sealed.Binding.Digest()
			require.NoError(t, err)
			claim := CopilotRerunClaimInput{AgentID: in.AgentID, PipelineID: saved.PipelineID, RunID: saved.NewRunID, JobRunID: saved.NewJobRunID, GateID: saved.GateID, ScheduleID: saved.ScheduleID, SealID: in.SealID, SealDigest: sealDigest, PayloadDigest: sealed.Binding.PayloadDigest, ClaimID: uuid.NewString(), AdmissionNonce: []byte(strings.Repeat("synthetic-nonce!", 2))}
			_, err = r.CopilotRerunClaim(ctx, claim, permitRerun, testRerunClaimValidator)
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&model.JobRunGeneration{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}))
			jobs := r.JobRun.(*JobRunRepo)
			job, err := jobs.GetByJobRunID(ctx, saved.NewJobRunID)
			require.NoError(t, err)
			identity := generationIdentity(job)
			admit := taskqueue.JobGenerationTransition{Version: 1, Operation: "admit", Identity: identity, RetryPlanDigest: job.RetryPlanDigest}
			admit.RequestDigest, err = admit.Digest()
			require.NoError(t, err)
			generation, err := jobs.ReconcileJobGeneration(ctx, admit)
			require.NoError(t, err)
			expected := taskqueue.SourceCheckoutExpectation{Version: 1, ScheduleID: saved.ScheduleID, SealID: in.SealID, ExpectedSourceCommit: job.ExpectedSourceCommit}
			digest, err := expected.Digest(identity)
			require.NoError(t, err)
			manifest, err := taskqueue.NewJobInputManifest(job.ExpectedSourceCommit, nil, nil)
			require.NoError(t, err)
			job, _, err = jobs.GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, manifest)
			require.NoError(t, err)
			// Construct a consistently expired metadata fixture only after every
			// live setup operation has completed. The synthetic test ciphertext is
			// deliberately constant; this is not a real encryption-lifetime test.
			// Held-lock deadline tests separately exercise actual time crossing.
			var expiry time.Time
			require.NoError(t, db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				expiry = time.Now().UTC().Truncate(time.Millisecond).Add(20 * time.Millisecond)
				var intent model.CopilotRerunIntent
				var schedule model.CopilotRerunSchedule
				var gate model.CopilotRerunAdmissionGate
				var approval model.ApprovalRequest
				require.NoError(t, tx.Where("intent_id = ?", saved.IntentID).First(&intent).Error)
				require.NoError(t, tx.Where("schedule_id = ?", saved.ScheduleID).First(&schedule).Error)
				require.NoError(t, tx.Where("gate_id = ?", saved.GateID).First(&gate).Error)
				require.NoError(t, tx.Where("approval_id = ?", job.RetryApprovalID).First(&approval).Error)
				require.True(t, time.Now().Before(intent.ExpiresAt), "live setup deadline must not be repaired after expiry")
				require.True(t, time.Now().Before(schedule.ExpiresAt))
				require.NotNil(t, approval.ExpiresAt)
				require.True(t, time.Now().Before(*approval.ExpiresAt))
				originalIntent, originalSchedule, originalGate := intent, schedule, gate
				evidence, err := retryapproval.ParseEvidence(approval.EvidenceJSON)
				require.NoError(t, err)
				require.Greater(t, expiry.UnixMilli(), evidence.CreatedAtUnixMilli)
				evidence.ExpiresAtUnixMilli = expiry.UnixMilli()
				body, err := evidence.Canonical()
				require.NoError(t, err)
				hash, err := evidence.Digest()
				require.NoError(t, err)
				intent.ExpiresAt = expiry
				intent.IntentDigest = rerunIntentDigest(&intent)
				binding := sealed.Binding
				binding.ExpiresAtUnixMilli = expiry.UnixMilli()
				boundDigest, err := binding.Digest()
				require.NoError(t, err)
				schedule.ExpiresAt, schedule.SealDigest = expiry, boundDigest
				gate.SealDigest = boundDigest
				boundClaim := claim
				boundClaim.SealDigest = boundDigest
				gate.AdmissionNonceDigest, err = rerunClaimNonceDigest(boundClaim)
				require.NoError(t, err)
				require.NoError(t, ValidateCopilotRerunSealedBinding(&schedule, &binding))
				require.True(t, rerunClaimNonceMatches(gate.AdmissionNonceDigest, boundClaim))
				// Only expiry-dependent metadata changes; the actual claim, fixed
				// identities, original bytes and all execution evidence stay intact.
				unchangedIntent, unchangedSchedule, unchangedGate := intent, schedule, gate
				unchangedIntent.ExpiresAt, unchangedIntent.IntentDigest = originalIntent.ExpiresAt, originalIntent.IntentDigest
				unchangedSchedule.ExpiresAt, unchangedSchedule.SealDigest = originalSchedule.ExpiresAt, originalSchedule.SealDigest
				unchangedGate.SealDigest, unchangedGate.AdmissionNonceDigest = originalGate.SealDigest, originalGate.AdmissionNonceDigest
				require.Equal(t, originalIntent, unchangedIntent)
				require.Equal(t, originalSchedule, unchangedSchedule)
				require.Equal(t, originalGate, unchangedGate)
				require.NoError(t, tx.Model(&model.CopilotRerunIntent{}).Where("intent_id = ?", intent.IntentID).Updates(map[string]any{"expires_at": expiry, "intent_digest": intent.IntentDigest}).Error)
				require.NoError(t, tx.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", schedule.ScheduleID).Updates(map[string]any{"expires_at": expiry, "seal_digest": schedule.SealDigest}).Error)
				require.NoError(t, tx.Model(&model.CopilotRerunAdmissionGate{}).Where("gate_id = ?", gate.GateID).Updates(map[string]any{"seal_digest": gate.SealDigest, "admission_nonce_digest": gate.AdmissionNonceDigest}).Error)
				require.NoError(t, tx.Model(&model.ApprovalRequest{}).Where("approval_id = ?", approval.ApprovalID).Updates(map[string]any{"expires_at": expiry, "evidence_json": body, "evidence_hash": hash}).Error)
				require.NoError(t, tx.Model(&model.JobRun{}).Where("job_run_id = ?", job.JobRunID).Update("retry_approval_evidence_hash", hash).Error)
				var storedSchedule model.CopilotRerunSchedule
				var storedGate model.CopilotRerunAdmissionGate
				var storedJob model.JobRun
				require.NoError(t, tx.Where("schedule_id = ?", schedule.ScheduleID).First(&storedSchedule).Error)
				require.NoError(t, tx.Where("gate_id = ?", gate.GateID).First(&storedGate).Error)
				require.NoError(t, tx.Where("job_run_id = ?", job.JobRunID).First(&storedJob).Error)
				require.NoError(t, ValidateCopilotRerunSealedBinding(&storedSchedule, &binding))
				require.Equal(t, gate, storedGate)
				require.True(t, rerunClaimNonceMatches(storedGate.AdmissionNonceDigest, boundClaim))
				require.Equal(t, identity, generationIdentity(&storedJob))
				require.Equal(t, hash, storedJob.RetryApprovalEvidenceHash)
				return nil
			}))
			timer := time.NewTimer(time.Until(expiry) + 5*time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				t.Fatalf("fixture expiry wait exceeded context: %v", ctx.Err())
			}
			require.False(t, time.Now().Before(expiry))
			request := retryapproval.Request{Version: 1, Operation: "observe", Identity: identity, GenerationID: generation.GenerationID, GenerationBindingDigest: generation.BindingDigest, RetryPlanDigest: job.RetryPlanDigest, InputManifestDigest: job.InputManifestDigest}
			observed, err := jobs.ReconcileJobRetryApproval(ctx, request)
			require.NoError(t, err)
			require.Equal(t, "expired", observed.State)
			require.False(t, observed.ExecutionAllowed)
			request.Operation = "admit"
			_, err = jobs.ReconcileJobRetryApproval(ctx, request)
			require.Error(t, err)
			approvals := r.Approval.(*ApprovalRepo)
			if closure == "expire" {
				closed, err := approvals.Expire(ctx, job.RetryApprovalID)
				require.NoError(t, err)
				require.Equal(t, model.ApprovalStatusExpired, closed.Status)
			} else {
				closed, err := approvals.Cancel(ctx, job.RetryApprovalID, "cancel expired preparation")
				require.NoError(t, err)
				require.Equal(t, model.ApprovalStatusCanceled, closed.Status)
			}
			var votes int64
			require.NoError(t, db.Model(&model.ApprovalDecision{}).Count(&votes).Error)
			require.Zero(t, votes)
		})
	}
}
