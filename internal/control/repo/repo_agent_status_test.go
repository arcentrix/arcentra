package repo

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"testing"
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
