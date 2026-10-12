package repo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"gorm.io/gorm"
)

func TestTerminalConflictVerificationIsReadOnlyAndRequiresOriginalClaim(t *testing.T) {
	ctx, db, jobs, steps := cancellationTestRepos(t)
	if err := jobs.CancelAgentJobIfCurrent(ctx, "job", 3, map[string]any{"status": 6}); err != nil {
		t.Fatal(err)
	}
	job, err := jobs.GetByJobRunID(ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	active, err := steps.GetByStepRunID(ctx, "active")
	if err != nil {
		t.Fatal(err)
	}
	unused, err := steps.GetByStepRunID(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := jobs.ValidateTerminalConflict(ctx, job, "original"); err != nil {
			t.Fatal(err)
		}
		if err := steps.ValidateTerminalConflict(ctx, active, "step-claim"); err != nil {
			t.Fatal(err)
		}
		if err := steps.ValidateTerminalConflict(ctx, unused, agentidentity.StepClaimID("original", "pending")); err != nil {
			t.Fatal(err)
		}
	}
	for _, claim := range []string{"", "wrong"} {
		if err := jobs.ValidateTerminalConflict(ctx, job, claim); err == nil {
			t.Fatal("wrong job claim accepted")
		}
		if err := steps.ValidateTerminalConflict(ctx, active, claim); err == nil {
			t.Fatal("wrong step claim accepted")
		}
		if err := steps.ValidateTerminalConflict(ctx, unused, claim); err == nil {
			t.Fatal("wrong derived claim accepted")
		}
	}
	stored, err := steps.GetByStepRunID(ctx, "pending")
	if err != nil || stored.ClaimID != "" || stored.TerminalReportDigest != "" || !stored.UpdatedAt.Equal(unused.UpdatedAt) {
		t.Fatalf("conflict verification changed cancelled attempt: %+v %v", stored, err)
	}
	current, err := jobs.GetByJobRunID(ctx, "job")
	if err != nil || current.TerminalReportDigest != "" || !current.UpdatedAt.Equal(job.UpdatedAt) {
		t.Fatal("conflict verification changed job")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := jobs.ValidateTerminalConflict(cancelled, job, "original"); err == nil {
		t.Fatal("ignored cancelled context")
	}
	if err := db.Callback().Query().Before("gorm:query").Register("terminal-verification-db-failure", func(tx *gorm.DB) { tx.AddError(errors.New("database unavailable")) }); err != nil {
		t.Fatal(err)
	}
	if err := steps.ValidateTerminalConflict(ctx, active, "step-claim"); err == nil {
		t.Fatal("DB failure became proof")
	}
}

func TestTerminalConflictVerificationFencesChangedStepSnapshot(t *testing.T) {
	for _, field := range []string{"status", "agent_id", "claim_id", "pipeline_id", "pipeline_run_id", "job_run_id", "attempt", "step_index", "previous_step_run_id", "cancellation_claim_id", "terminal_report_digest"} {
		t.Run(field, func(t *testing.T) {
			ctx, db, jobs, steps := cancellationTestRepos(t)
			if err := jobs.CancelAgentJobIfCurrent(ctx, "job", 3, map[string]any{"status": 6}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := steps.GetByStepRunID(ctx, "pending")
			if err != nil {
				t.Fatal(err)
			}
			var change any = "changed"
			switch field {
			case "status":
				change = 7
			case "attempt":
				change = 3
			case "step_index":
				change = 1
			case "terminal_report_digest":
				change = strings.Repeat("c", 64)
			}
			if err := db.WithContext(ctx).Model(&model.StepRun{}).Where("step_run_id = ?", "pending").Update(field, change).Error; err != nil {
				t.Fatal(err)
			}
			if err := steps.ValidateTerminalConflict(ctx, snapshot, agentidentity.StepClaimID("original", "pending")); err == nil {
				t.Fatal("stale terminal snapshot accepted")
			}
		})
	}
}

func TestTerminalConflictVerificationFencesChangedJobAndDerivedParent(t *testing.T) {
	for _, field := range []string{"status", "agent_id", "claim_id", "pipeline_id", "pipeline_run_id", "cancellation_claim_id", "terminal_report_digest"} {
		t.Run(field, func(t *testing.T) {
			ctx, db, jobs, steps := cancellationTestRepos(t)
			if err := jobs.CancelAgentJobIfCurrent(ctx, "job", 3, map[string]any{"status": 6}); err != nil {
				t.Fatal(err)
			}
			job, err := jobs.GetByJobRunID(ctx, "job")
			if err != nil {
				t.Fatal(err)
			}
			step, err := steps.GetByStepRunID(ctx, "pending")
			if err != nil {
				t.Fatal(err)
			}
			var change any = "changed"
			if field == "status" {
				change = 7
			}
			if field == "terminal_report_digest" {
				change = strings.Repeat("a", 64)
			}
			if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update(field, change).Error; err != nil {
				t.Fatal(err)
			}
			if err := jobs.ValidateTerminalConflict(ctx, job, "original"); err == nil {
				t.Fatal("stale job conflict accepted")
			}
			// A subsequently committed parent cancellation receipt does not invalidate its original cancellation provenance.
			if field != "terminal_report_digest" {
				if err := steps.ValidateTerminalConflict(ctx, step, agentidentity.StepClaimID("original", "pending")); err == nil {
					t.Fatal("derived attempt accepted changed parent")
				}
			}
		})
	}
}

func TestTerminalConflictVerificationRejectsUnprovenLegacyAndActiveClaims(t *testing.T) {
	ctx, db, jobs, steps := cancellationTestRepos(t)
	job, err := jobs.GetByJobRunID(ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	step, err := steps.GetByStepRunID(ctx, "active")
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.ValidateTerminalConflict(ctx, job, "original"); err == nil {
		t.Fatal("active job produced terminal proof")
	}
	if err := steps.ValidateTerminalConflict(ctx, step, "step-claim"); err == nil {
		t.Fatal("active step produced terminal proof")
	}
	if err := db.WithContext(ctx).Model(&model.StepRun{}).Where("step_run_id = ?", "pending").Update("status", 6).Error; err != nil {
		t.Fatal(err)
	}
	legacy, err := steps.GetByStepRunID(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if err := steps.ValidateTerminalConflict(ctx, legacy, agentidentity.StepClaimID("original", "pending")); err == nil {
		t.Fatal("legacy cancellation without parent proof accepted")
	}
	job.Status = 5
	job.ClaimID = ""
	if err := jobs.ValidateTerminalConflict(ctx, job, "arbitrary"); err == nil {
		t.Fatal("empty original job claim accepted")
	}
}

func TestTerminalConflictVerificationChecksDecodedIdentityByteExactly(t *testing.T) {
	// 模拟大小写不敏感排序规则返回同名记录，不能仅信任 WHERE 相等判断
	for _, kind := range []string{"job", "claimed_step", "derived_step_parent", "derived_step"} {
		t.Run(kind, func(t *testing.T) {
			ctx, db, jobs, steps := cancellationTestRepos(t)
			if err := jobs.CancelAgentJobIfCurrent(ctx, "job", 3, map[string]any{"status": 6}); err != nil {
				t.Fatal(err)
			}
			job, err := jobs.GetByJobRunID(ctx, "job")
			if err != nil {
				t.Fatal(err)
			}
			stepID, claim := "active", "step-claim"
			if strings.HasPrefix(kind, "derived_") {
				stepID = "pending"
				claim = agentidentity.StepClaimID("original", "pending")
			}
			step, err := steps.GetByStepRunID(ctx, stepID)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Query().After("gorm:query").Register("simulate-case-insensitive-match", func(tx *gorm.DB) {
				switch current := tx.Statement.Dest.(type) {
				case *model.JobRun:
					if kind == "job" || kind == "derived_step_parent" {
						current.AgentID = "AGENT"
					}
				case *model.StepRun:
					if kind == "claimed_step" || kind == "derived_step" {
						current.PreviousStepRunID = strings.ToUpper(current.PreviousStepRunID)
						current.PipelineRunID = "RUN"
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			if kind == "job" {
				if err := jobs.ValidateTerminalConflict(ctx, job, "original"); err == nil {
					t.Fatal("case-insensitive job match became exact proof")
				}
			} else if err := steps.ValidateTerminalConflict(ctx, step, claim); err == nil {
				t.Fatal("case-insensitive step or parent match became exact proof")
			}
		})
	}
}
