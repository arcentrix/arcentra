package repo

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func cancellationTestRepos(t *testing.T) (context.Context, *gorm.DB, *JobRunRepo, *StepRunRepo) {
	t.Helper()
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.WithContext(ctx).AutoMigrate(&model.JobRun{}, &model.StepRun{}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{
		&model.JobRun{JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", ClaimID: "original", Status: 3},
		&model.StepRun{StepRunID: "active", JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", ClaimID: "step-claim", Status: 3},
		&model.StepRun{StepRunID: "pending", JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", Status: 2, Attempt: 2, PreviousStepRunID: "active"},
		&model.StepRun{StepRunID: "finished", JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", ClaimID: "finished-claim", Status: 5, TerminalReportDigest: strings.Repeat("b", 64)},
	} {
		if err := db.WithContext(ctx).Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	return ctx, db, &JobRunRepo{IDatabase: statusTestDB{db}}, &StepRunRepo{IDatabase: statusTestDB{db}}
}

func TestControllerCancellationTransactionRollsBackAndPreservesTerminalAttempts(t *testing.T) {
	ctx, db, jobs, steps := cancellationTestRepos(t)
	callback := "test-cancellation-step-failure"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "step_run" {
			tx.AddError(fmt.Errorf("injected step cancellation failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	cancel := func() error { return jobs.CancelAgentJobIfCurrent(ctx, "job", 3, map[string]any{"status": 6}) }
	if err := cancel(); err == nil {
		t.Fatal("partial cancellation succeeded")
	}
	job, _ := jobs.GetByJobRunID(ctx, "job")
	if job.Status != 3 || job.CancellationClaimID != "" {
		t.Fatalf("cancellation did not roll back: %+v", job)
	}
	if err := db.Callback().Update().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if err := cancel(); err != nil {
		t.Fatal(err)
	}
	if err := cancel(); err == nil {
		t.Fatal("stale duplicate cancellation rewrote the terminal job")
	}
	for _, id := range []string{"active", "pending"} {
		step, _ := steps.GetByStepRunID(ctx, id)
		if step.Status != 6 || step.CancellationClaimID != "original" {
			t.Fatalf("step cancellation lost: %+v", step)
		}
	}
	finished, _ := steps.GetByStepRunID(ctx, "finished")
	if finished.Status != 5 || finished.CancellationClaimID != "" || finished.TerminalReportDigest != strings.Repeat("b", 64) {
		t.Fatalf("finished attempt changed: %+v", finished)
	}
}

func TestCancellationReceiptCASFencesChangedAttemptIdentity(t *testing.T) {
	for _, field := range []string{"agent_id", "claim_id", "pipeline_id", "pipeline_run_id", "job_run_id", "attempt", "previous_step_run_id", "cancellation_claim_id", "terminal_report_digest"} {
		t.Run(field, func(t *testing.T) {
			ctx, db, jobs, steps := cancellationTestRepos(t)
			if err := jobs.CancelAgentJobIfCurrent(ctx, "job", 3, map[string]any{"status": 6}); err != nil {
				t.Fatal(err)
			}
			snapshot, _ := steps.GetByStepRunID(ctx, "pending")
			var changed any = "changed"
			if field == "attempt" {
				changed = 3
			}
			if err := db.WithContext(ctx).Model(&model.StepRun{}).Where("step_run_id = ?", "pending").Update(field, changed).Error; err != nil {
				t.Fatal(err)
			}
			if err := steps.CommitCancellationReceipt(ctx, snapshot, agentidentity.StepClaimID("original", "pending"), strings.Repeat("a", 64)); err == nil {
				t.Fatal("stale cancellation receipt accepted")
			}
		})
	}
}

func TestCancellationReceiptPinsOnceWithoutChangingUnstartedClaim(t *testing.T) {
	ctx, _, jobs, steps := cancellationTestRepos(t)
	if err := jobs.CancelAgentJobIfCurrent(ctx, "job", 3, map[string]any{"status": 6}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := steps.GetByStepRunID(ctx, "pending")
	claim := agentidentity.StepClaimID("original", "pending")
	if err := steps.CommitCancellationReceipt(ctx, snapshot, "random", strings.Repeat("a", 64)); err == nil {
		t.Fatal("unstarted cancellation used an unrelated claim")
	}
	if err := steps.CommitCancellationReceipt(ctx, snapshot, claim, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := steps.CommitCancellationReceipt(ctx, snapshot, claim, strings.Repeat("b", 64)); err == nil {
		t.Fatal("receipt digest overwritten")
	}
	stored, _ := steps.GetByStepRunID(ctx, "pending")
	if stored.ClaimID != "" || stored.TerminalReportDigest != strings.Repeat("a", 64) || !stored.UpdatedAt.Equal(snapshot.UpdatedAt) {
		t.Fatalf("receipt created execution ownership or rewrote terminal timestamp: %+v", stored)
	}
}

func TestCancellationSchemaMigrationPreservesLegacyFailClosedState(t *testing.T) {
	ctx, db, jobs, _ := cancellationTestRepos(t)
	for _, entity := range []any{&model.JobRun{}, &model.StepRun{}} {
		if err := db.Migrator().DropColumn(entity, "CancellationClaimID"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.WithContext(ctx).AutoMigrate(&model.JobRun{}, &model.StepRun{}); err != nil {
		t.Fatal(err)
	}
	job, err := jobs.GetByJobRunID(ctx, "job")
	if err != nil || job.CancellationClaimID != "" || job.ClaimID != "original" || job.Status != 3 {
		t.Fatalf("additive cancellation migration manufactured provenance: %+v %v", job, err)
	}
}

func TestCancellationBeforeAgentBindingHasNoReceiptProvenance(t *testing.T) {
	ctx, db, jobs, steps := cancellationTestRepos(t)
	for _, value := range []any{
		&model.JobRun{JobRunID: "unbound-job", PipelineID: "pipeline", PipelineRunID: "unbound-run", Status: 1},
		&model.StepRun{StepRunID: "unbound-step", JobRunID: "unbound-job", PipelineID: "pipeline", PipelineRunID: "unbound-run", Status: 1},
	} {
		if err := db.WithContext(ctx).Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := jobs.CancelAgentJobIfCurrent(ctx, "unbound-job", 1, map[string]any{"status": 6}); err != nil {
		t.Fatal(err)
	}
	job, err := jobs.GetByJobRunID(ctx, "unbound-job")
	if err != nil || job.Status != 6 || job.AgentID != "" || job.ClaimID != "" || job.CancellationClaimID != "" || job.TerminalReportDigest != "" {
		t.Fatalf("unbound cancellation fabricated identity: %+v %v", job, err)
	}
	step, err := steps.GetByStepRunID(ctx, "unbound-step")
	if err != nil || step.Status != 6 || step.AgentID != "" || step.ClaimID != "" || step.CancellationClaimID != "" {
		t.Fatalf("unbound step cancellation fabricated identity: %+v %v", step, err)
	}
	other, err := jobs.GetByJobRunID(ctx, "job")
	if err != nil || other.Status != 3 {
		t.Fatal("unbound cancellation crossed job scope")
	}
	if err := jobs.CommitCancellationReceipt(ctx, job, "new-claim", strings.Repeat("a", 64)); err == nil {
		t.Fatal("unclaimed canceled job accepted a receipt")
	}
}
