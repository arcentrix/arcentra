package repo

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func retryGuardFixture(t *testing.T, mode string) (*gorm.DB, *StepRunRepo, *JobRunRepo, *model.StepRun, *model.JobRun) {
	t.Helper()
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "guard.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err = db.WithContext(ctx).AutoMigrate(&model.StepRun{}, &model.JobRun{}); err != nil {
		t.Fatal(err)
	}
	policy := `{"mode":"` + mode + `","max_attempts":3,"idempotent":true,"allowed_exit_codes":[9]}`
	job := &model.JobRun{JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", Status: 3, ClaimID: "job-claim"}
	exit := 9
	previous := &model.StepRun{StepRunID: "previous", JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", Name: "shell", Status: 5, ClaimID: "old-claim", TerminalReportDigest: "committed", Attempt: 1, RetryMode: mode, RetryPolicy: policy, ExitCode: &exit, Uses: "shell"}
	step := &model.StepRun{StepRunID: "next", JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", Name: "shell", Status: 2, Attempt: 2, RetryMode: mode, RetryPolicy: policy, PreviousStepRunID: "previous", Uses: "shell"}
	for _, value := range []any{job, previous, step} {
		if err = db.WithContext(ctx).Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, &StepRunRepo{IDatabase: statusTestDB{db}}, &JobRunRepo{IDatabase: statusTestDB{db}}, step, job
}
func TestRetryClaimsRequireCommittedMatchingPredecessorAndLiveParent(t *testing.T) {
	cases := []struct {
		name, table string
		change      map[string]any
	}{
		{"parent-cancelled", "job_run", map[string]any{"status": 6}},
		{"parent-queued", "job_run", map[string]any{"status": 2}},
		{"previous-pending", "step_run", map[string]any{"status": 2}},
		{"previous-running", "step_run", map[string]any{"status": 3}},
		{"previous-success", "step_run", map[string]any{"status": 4}},
		{"previous-recovered", "step_run", map[string]any{"terminal_report_digest": ""}},
		{"previous-wrong-name", "step_run", map[string]any{"name": "other"}},
		{"previous-wrong-index", "step_run", map[string]any{"step_index": 1}},
		{"previous-wrong-attempt", "step_run", map[string]any{"attempt": 9}},
		{"previous-wrong-scope", "step_run", map[string]any{"pipeline_run_id": "other"}},
		{"previous-unlisted-exit", "step_run", map[string]any{"exit_code": 8}},
		{"previous-continued", "step_run", map[string]any{"continue_on_error": 1}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			db, steps, _, next, _ := retryGuardFixture(t, "failed_step")
			ctx := context.Background()
			query := db.WithContext(ctx).Table(test.table)
			if test.table == "step_run" {
				query = query.Where("step_run_id = ?", "previous")
			} else {
				query = query.Where("job_run_id = ?", "job")
			}
			if err := query.Updates(test.change).Error; err != nil {
				t.Fatal(err)
			}
			if err := steps.PatchReportedStatus(ctx, next, map[string]any{"status": 3, "claim_id": "new"}); err == nil {
				t.Fatal("invalid retry activated")
			}
			got, _ := steps.GetByStepRunID(ctx, "next")
			if got.Status != 2 || got.ClaimID != "" {
				t.Fatalf("rejected retry mutated: %+v", got)
			}
		})
	}
	db, steps, _, next, _ := retryGuardFixture(t, "failed_step")
	ctx := context.Background()
	if err := steps.PatchReportedStatus(ctx, next, map[string]any{"status": 3, "claim_id": "new"}); err != nil {
		t.Fatal(err)
	}
	next, _ = steps.GetByStepRunID(ctx, "next")
	if err := steps.ValidateRetryActivation(ctx, next); err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("status", 6).Error; err != nil {
		t.Fatal(err)
	}
	if err := steps.ValidateRetryActivation(ctx, next); err == nil {
		t.Fatal("lost-ACK running recheck accepted after parent cancellation")
	}
}
func TestWholeJobRetryRequiresCompletedRoundWithEligibleFailure(t *testing.T) {
	db, steps, _, next, _ := retryGuardFixture(t, "whole_job")
	ctx := context.Background()
	if err := steps.PatchReportedStatus(ctx, next, map[string]any{"status": 3, "claim_id": "new"}); err != nil {
		t.Fatal(err)
	}
	// A successful earlier step alone cannot authorize another whole-job round.
	db, steps, _, next, _ = retryGuardFixture(t, "whole_job")
	if err := db.WithContext(ctx).Model(&model.StepRun{}).Where("step_run_id = ?", "previous").Update("status", 4).Error; err != nil {
		t.Fatal(err)
	}
	if err := steps.PatchReportedStatus(ctx, next, map[string]any{"status": 3, "claim_id": "new"}); err == nil {
		t.Fatal("successful whole job was repeated")
	}
}
func TestJobTerminalWaitsForEveryReservedAttempt(t *testing.T) {
	db, _, jobs, _, job := retryGuardFixture(t, "failed_step")
	ctx := context.Background()
	for _, artifacts := range []bool{false, true} {
		var err error
		if artifacts {
			err = jobs.PatchReportedStatusAndArtifacts(ctx, job, map[string]any{"status": 4, "terminal_report_digest": "job-receipt"}, nil)
		} else {
			err = jobs.PatchReportedStatus(ctx, job, map[string]any{"status": 4, "terminal_report_digest": "job-receipt"})
		}
		if err == nil {
			t.Fatal("job terminal overtook pending attempt")
		}
	}
	if err := db.WithContext(ctx).Model(&model.StepRun{}).Where("step_run_id = ?", "next").Updates(map[string]any{"status": 8, "terminal_report_digest": "skipped"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := jobs.PatchReportedStatus(ctx, job, map[string]any{"status": 4, "terminal_report_digest": "job-receipt"}); err != nil {
		t.Fatal(err)
	}
}

func TestRetryPolicyComparisonIgnoresDatabaseJSONNormalization(t *testing.T) {
	db, steps, _, next, _ := retryGuardFixture(t, "failed_step")
	ctx := context.Background()
	normalized := `{ "allowed_exit_codes": [9], "idempotent": true, "max_attempts": 3, "mode": "failed_step" }`
	if err := db.WithContext(ctx).Model(&model.StepRun{}).Where("job_run_id = ?", "job").Update("retry_policy", normalized).Error; err != nil {
		t.Fatal(err)
	}
	if err := steps.PatchReportedStatus(ctx, next, map[string]any{"status": 3, "claim_id": "new"}); err != nil {
		t.Fatalf("JSON normalization invalidated unchanged policy: %v", err)
	}
	if sameRetryPolicy(normalized, `{"mode":"whole_job","max_attempts":3,"idempotent":true,"allowed_exit_codes":[9]}`) {
		t.Fatal("material scope change compared equal")
	}
}
