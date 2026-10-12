package repo

import (
	"context"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type statusTestDB struct{ db *gorm.DB }

func (s statusTestDB) Database() *gorm.DB { return s.db }

func TestAtomicStatusRejectsLateAndDuplicateClaims(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.AutoMigrate(&model.JobRun{}, &model.StepRun{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	job := &model.JobRun{JobRunID: "job", AgentID: "a", Status: 2}
	if err := db.Create(job).Error; err != nil {
		t.Fatal(err)
	}
	r := &JobRunRepo{IDatabase: statusTestDB{db}}
	if err := r.PatchStatusIfCurrent(ctx, "job", 2, map[string]any{"status": 3}); err != nil {
		t.Fatal(err)
	}
	if err := r.PatchStatusIfCurrent(ctx, "job", 2, map[string]any{"status": 3}); err == nil {
		t.Fatal("duplicate claim succeeded")
	}
	if err := r.PatchStatusIfCurrent(ctx, "job", 3, map[string]any{"status": 6}); err != nil {
		t.Fatal(err)
	}
	if err := r.PatchStatusIfCurrent(ctx, "job", 3, map[string]any{"status": 4}); err == nil {
		t.Fatal("late success overwrote cancellation")
	}
	got, err := r.GetByJobRunID(ctx, "job")
	if err != nil || got.Status != 6 {
		t.Fatalf("terminal state changed: %+v %v", got, err)
	}
	step := &model.StepRun{StepRunID: "step", Status: 2}
	if err := db.Create(step).Error; err != nil {
		t.Fatal(err)
	}
	sr := &StepRunRepo{IDatabase: statusTestDB{db}}
	if err := sr.PatchStatusIfCurrent(ctx, "step", 2, map[string]any{"status": 4}); err != nil {
		t.Fatal(err)
	}
	if err := sr.PatchStatusIfCurrent(ctx, "step", 2, map[string]any{"status": 3}); err == nil {
		t.Fatal("late step status accepted")
	}
}

func TestReportedStatusFencesChangedOwnershipAndAttempt(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"job", "step"} {
		fields := []string{"agent_id", "claim_id", "pipeline_id", "pipeline_run_id"}
		if kind == "step" {
			fields = append(fields, "job_run_id")
		}
		for _, field := range fields {
			t.Run(kind+"/"+field, func(t *testing.T) {
				db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
				if err != nil {
					t.Fatal(err)
				}
				sqlDB, _ := db.DB()
				defer sqlDB.Close()
				if err := db.WithContext(ctx).AutoMigrate(&model.JobRun{}, &model.StepRun{}); err != nil {
					t.Fatal(err)
				}
				updates := map[string]any{"status": 5, "terminal_report_digest": "receipt", "claim_id": "original"}
				if kind == "job" {
					snapshot := &model.JobRun{JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", ClaimID: "original", Status: 3}
					if err := db.WithContext(ctx).Create(snapshot).Error; err != nil {
						t.Fatal(err)
					}
					if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update(field, "changed").Error; err != nil {
						t.Fatal(err)
					}
					if err := (&JobRunRepo{IDatabase: statusTestDB{db}}).PatchReportedStatus(ctx, snapshot, updates); err == nil {
						t.Fatal("stale job ownership accepted")
					}
				} else {
					snapshot := &model.StepRun{StepRunID: "step", JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", ClaimID: "original", Status: 3}
					if err := db.WithContext(ctx).Create(snapshot).Error; err != nil {
						t.Fatal(err)
					}
					if err := db.WithContext(ctx).Model(&model.StepRun{}).Where("step_run_id = ?", "step").Update(field, "changed").Error; err != nil {
						t.Fatal(err)
					}
					if err := (&StepRunRepo{IDatabase: statusTestDB{db}}).PatchReportedStatus(ctx, snapshot, updates); err == nil {
						t.Fatal("stale step ownership accepted")
					}
				}
				var state struct {
					Status               int
					TerminalReportDigest string
				}
				if err := db.WithContext(ctx).Table(kind+"_run").Select("status", "terminal_report_digest").Where(kind+"_run_id = ?", kind).Scan(&state).Error; err != nil {
					t.Fatal(err)
				}
				if state.Status != 3 || state.TerminalReportDigest != "" {
					t.Fatalf("rejected stale report mutated state: %+v", state)
				}
			})
		}
	}
}
