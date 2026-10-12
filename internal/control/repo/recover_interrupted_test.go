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
	for _, job := range []model.JobRun{{JobRunID: "running", PipelineRunID: "active", Status: 3}, {JobRunID: "succeeded", PipelineRunID: "active", Status: 4}} {
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
	if jobs[0].Status != 5 || jobs[1].Status != 4 {
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
