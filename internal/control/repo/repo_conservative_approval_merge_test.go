package repo

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

func TestConservativeMergeFailedStepRejectsApprovalJobMetadata(t *testing.T) {
	for name, patch := range map[string]map[string]any{
		"required marker":      {"retry_approval_required": true},
		"approval backlink":    {"retry_approval_id": "unexpected-approval"},
		"approval evidence":    {"retry_approval_evidence_hash": "v1:" + strings.Repeat("a", 64)},
		"all approval markers": {"retry_approval_required": true, "retry_approval_id": "unexpected-approval", "retry_approval_evidence_hash": "v1:" + strings.Repeat("a", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newStepAttemptFixture(t, "")
			request := f.request(t, 0)
			first := f.admitStep(t, 0)
			f.finishStep(t, first, model.JobRunStatusFailed)
			advance := f.nextRequest(t, first, 1)
			// Simulate malformed persisted metadata rather than relying only on DSL validation.
			if err := f.db.WithContext(f.ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(patch).Error; err != nil {
				t.Fatal(err)
			}
			job, err := f.jobs.GetByJobRunID(f.ctx, "job")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := retryPlanForJob(job); err == nil {
				t.Fatal("approval-bearing failed_step survived persisted-plan validation")
			}
			if _, err := f.jobs.ReconcileStepAttempt(f.ctx, request); err == nil {
				t.Fatal("old step admission bypassed unsupported approval metadata")
			}
			if _, err := f.jobs.ReconcileStepAttempt(f.ctx, advance); err == nil {
				t.Fatal("step advance bypassed unsupported approval metadata")
			}
			if err := f.reportStep(t, "first-2", first, model.JobRunStatusRunning, false); err == nil {
				t.Fatal("RUNNING bypassed unsupported approval metadata")
			}
			ctx := agentidentity.WithGenerationReport(f.ctx, f.generation.GenerationID, f.generation.BindingDigest)
			if err := validateGenerationJobReport(ctx, f.db, job, model.JobRunStatusSuccess); err == nil {
				t.Fatal("job SUCCESS bypassed unsupported approval metadata")
			}
			if err := validateGenerationArtifact(ctx, f.db, job, "upload-1"); err == nil {
				t.Fatal("artifact publication bypassed unsupported approval metadata")
			}
		})
	}
}

func TestConservativeMergeFailedStepCannotStartWithApprovalMetadata(t *testing.T) {
	for _, field := range []string{"retry_approval_required", "retry_approval_id", "retry_approval_evidence_hash"} {
		for _, admitted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/admitted=%t", field, admitted), func(t *testing.T) {
				f := newStepAttemptFixture(t, "")
				request := f.request(t, 0)
				var admission *taskqueue.StepAttemptAdmission
				if admitted {
					admission = f.admitStep(t, 0)
				}
				var value any = "unexpected-approval"
				if field == "retry_approval_required" {
					value = true
				} else if field == "retry_approval_evidence_hash" {
					value = "v1:" + strings.Repeat("a", 64)
				}
				if err := f.db.WithContext(f.ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update(field, value).Error; err != nil {
					t.Fatal(err)
				}
				if _, err := f.jobs.ReconcileStepAttempt(f.ctx, request); err == nil {
					t.Fatal("initial or replayed admission bypassed approval metadata")
				}
				if admitted {
					if err := f.reportStep(t, "first-1", admission, model.JobRunStatusRunning, false); err == nil {
						t.Fatal("exact admitted initial RUNNING bypassed approval metadata")
					}
				}
			})
		}
	}
}

func TestConservativeMergeWholeJobHistorySurvivesLostAuthority(t *testing.T) {
	for _, reason := range []string{"revocation", "expiry", "cancellation"} {
		t.Run(reason, func(t *testing.T) {
			f := gatedPinFixture(t)
			g := f.admission(t)
			f.pin(t)
			installRetryApprovalTestAuthority(t, f, nil)
			approveRetryApproval(t, f)
			if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f, g, "admit")); err != nil {
				t.Fatal(err)
			}
			f.failedRound(t, g)
			request := f.advance(t, g)
			next, err := f.jobs.ReconcileJobGeneration(f.ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			before, err := f.steps.GetByStepRunID(f.ctx, "first-1")
			if err != nil || before.TerminalReportDigest == "" {
				t.Fatal("missing original failure receipt", err)
			}
			switch reason {
			case "revocation":
				if err := f.db.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", "voter").Update("is_enabled", 0).Error; err != nil {
					t.Fatal(err)
				}
			case "expiry":
				setRetryApprovalTestExpiry(t, f, time.Now().Add(30*time.Millisecond))
				time.Sleep(40 * time.Millisecond)
			case "cancellation":
				job, err := f.jobs.GetByJobRunID(f.ctx, "job")
				if err != nil {
					t.Fatal(err)
				}
				if err := f.jobs.CancelAgentJobIfCurrent(f.ctx, "job", job.Status, map[string]any{"status": model.JobRunStatusCancelled}); err != nil {
					t.Fatal(err)
				}
			}
			replay, err := f.jobs.ReconcileJobGeneration(f.ctx, request)
			if err != nil || replay.GenerationID != next.GenerationID || replay.BindingDigest != next.BindingDigest || replay.ExecutionAllowed {
				t.Fatalf("historical advance ACK changed or regained authority: %+v %v", replay, err)
			}
			if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f, next, "admit")); err == nil {
				t.Fatal("fresh startup survived lost authority")
			}
			if err := f.report(t, next, "first-2", model.JobRunStatusRunning, false); err == nil {
				t.Fatal("fresh RUNNING survived lost authority")
			}
			after, err := f.steps.GetByStepRunID(f.ctx, "first-1")
			if err != nil || before.TerminalReportDigest != after.TerminalReportDigest || before.Status != after.Status || before.ClaimID != after.ClaimID || before.GenerationID != after.GenerationID || before.GenerationBindingDigest != after.GenerationBindingDigest {
				t.Fatal("lost authority rewrote historical failure receipt", err)
			}
			ctx := agentidentity.WithGenerationReport(f.ctx, g.GenerationID, g.BindingDigest)
			if err := f.steps.ValidateRuntimeStepClaim(ctx, after, after.ClaimID, after.Status); err != nil {
				t.Fatal("repository validation denied exact historical terminal ownership", err)
			}
		})
	}
}
