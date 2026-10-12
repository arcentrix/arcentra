package repo

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"testing"
)

func TestOfflineRecoveryPreservesTerminalAndNeverReplays(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.AutoMigrate(&model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []model.PipelineRun{{RunID: "active", Status: 2}, {RunID: "other", Status: 2}, {RunID: "done", Status: 3}} {
		if err := db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, job := range []model.JobRun{{JobRunID: "running", PipelineRunID: "active", Status: 3, ClaimID: "old-claim", TerminalReportDigest: "stale-receipt"}, {JobRunID: "succeeded", PipelineRunID: "active", Status: 4, TerminalReportDigest: "committed-receipt"}} {
		if err := db.Create(&job).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.StepRun{StepRunID: "waiting", PipelineRunID: "active", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	preview, err := RecoverInterruptedRuns(ctx, db, []string{"active"}, false)
	if err != nil || preview != (InterruptedRecoverySummary{Runs: 1, Jobs: 1, Steps: 1}) {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	var run model.PipelineRun
	db.Where("run_id = ?", "active").Take(&run)
	if run.Status != 2 {
		t.Fatal("dry-run mutated state")
	}
	if _, err := RecoverInterruptedRuns(ctx, db, []string{"active"}, true); err != nil {
		t.Fatal(err)
	}
	var jobs []model.JobRun
	db.Order("job_run_id").Find(&jobs)
	if jobs[0].Status != 5 || jobs[0].ClaimID != "old-claim" || jobs[0].TerminalReportDigest != "" || jobs[1].Status != 4 || jobs[1].TerminalReportDigest != "committed-receipt" {
		t.Fatalf("terminal preservation failed: %+v", jobs)
	}
	var other model.PipelineRun
	db.Where("run_id = ?", "other").Take(&other)
	if other.Status != 2 {
		t.Fatal("unselected run changed")
	}
	if summary, err := RecoverInterruptedRuns(ctx, db, []string{"active"}, true); err != nil || summary.Runs != 0 {
		t.Fatalf("reconciliation not idempotent: %+v %v", summary, err)
	}
}

func TestOfflineRecoveryWarnsAndPreservesLinkedRelease(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.AutoMigrate(&model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.DeploymentRelease{}, &model.DeploymentEnvironment{}); err != nil {
		t.Fatal(err)
	}
	run := model.PipelineRun{RunID: "run", Status: 2}
	step := model.StepRun{StepRunID: "step", PipelineRunID: "run", Status: 3, DeploymentReleaseID: "release"}
	release := model.DeploymentRelease{ReleaseID: "release", PipelineRunID: "run", ProjectID: "project", RequestID: "req", Status: "applying", ExecutionStepRunID: &step.StepRunID}
	env := model.DeploymentEnvironment{EnvironmentID: "env", ActiveReleaseID: "release"}
	for _, value := range []any{&run, &step, &release, &env} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	summary, err := RecoverInterruptedRuns(context.Background(), db, []string{"run"}, true)
	if err != nil || summary.LinkedUnresolvedReleases != 1 {
		t.Fatalf("missing unresolved warning: %+v %v", summary, err)
	}
	var after model.DeploymentRelease
	db.Where("release_id = ?", "release").Take(&after)
	var target model.DeploymentEnvironment
	db.Where("environment_id = ?", "env").Take(&target)
	if after.Status != "applying" || target.ActiveReleaseID != "release" {
		t.Fatalf("recovery altered external effects: %+v %+v", after, target)
	}
	again, err := RecoverInterruptedRuns(context.Background(), db, []string{"run"}, false)
	if err != nil || again.LinkedUnresolvedReleases != 1 || again.Runs != 0 {
		t.Fatalf("terminal run hides pending release: %+v %v", again, err)
	}
}
