//go:build sandbox_acceptance

package taskqueue

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
)

func TestRetryApprovalAcceptanceQueriesExactRuntimeJobIdentity(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "query.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := db.AutoMigrate(&model.StepRun{}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []model.StepRun{
		{StepRunID: "first", JobID: "definition-job", JobRunID: "runtime-job", Attempt: 1},
		{StepRunID: "second", JobID: "definition-job", JobRunID: "runtime-job", Attempt: 2},
		{StepRunID: "foreign", JobID: "runtime-job", JobRunID: "foreign-runtime", Attempt: 1},
	} {
		if err := db.Create(&step).Error; err != nil {
			t.Fatal(err)
		}
	}
	steps, err := containerApprovalPersistedSteps(context.Background(), db, "runtime-job")
	if err != nil || len(steps) != 2 {
		t.Fatalf("exact runtime steps %d: %v", len(steps), err)
	}
	for _, step := range steps {
		if step.JobRunID != "runtime-job" || step.JobID != "definition-job" {
			t.Fatal("definition identity confused with runtime identity")
		}
	}
	missing, err := containerApprovalPersistedSteps(context.Background(), db, "absent-runtime")
	if err != nil || len(missing) != 0 {
		t.Fatal("missing runtime query broadened scope")
	}
}
