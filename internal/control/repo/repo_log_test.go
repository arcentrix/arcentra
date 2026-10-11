// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestLogRepositoryPersistenceAndRetry(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	r := NewLogRepository(db)
	ctx := context.Background()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	entries := []*model.StepRunLog{
		{LogID: "b", StepRunID: "step-b", LineNumber: 1, Timestamp: 2, Content: "second"},
		{LogID: "a", StepRunID: "step-a", LineNumber: 1, Timestamp: 1, Content: "first"},
		{LogID: "c", StepRunID: "step-a", LineNumber: 2, Timestamp: 3, Content: "third"},
	}
	for range 2 {
		if err := r.CreateBatch(ctx, entries); err != nil {
			t.Fatal(err)
		}
	}
	identities, ok := r.(ILogIdentityRepository)
	if !ok {
		t.Fatal("persistent repository must expose identity lookup")
	}
	for logID, want := range map[string]bool{"a": true, "b": true, "c": true, "missing": false} {
		got, err := identities.HasLogID(ctx, logID)
		if err != nil || got != want {
			t.Fatalf("identity lookup %q = %t, want %t, err = %v", logID, got, want, err)
		}
	}
	if _, err := identities.HasLogID(ctx, ""); err == nil {
		t.Fatal("empty identity lookup must fail")
	}
	logs, err := r.ListByStepRunIDs(ctx, []string{"step-a", "step-b"}, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 3 || logs[0].LogID != "a" || logs[1].LogID != "b" || logs[2].LogID != "c" {
		t.Fatalf("unexpected logs after retry: %#v", logs)
	}
	line, err := r.LastLineNumber(ctx, "step-a")
	if err != nil || line != 2 {
		t.Fatalf("last line = %d, err = %v", line, err)
	}
	logs, err = r.ListByStepRunIDs(ctx, []string{"step-a"}, 2, 10)
	if err != nil || len(logs) != 1 || logs[0].LogID != "c" {
		t.Fatalf("step/fromLine filter = %#v, err = %v", logs, err)
	}
	logs, err = r.ListByStepRunIDs(ctx, nil, 0, 10)
	if err != nil || len(logs) != 0 {
		t.Fatalf("empty authorized scope returned logs: %#v, %v", logs, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.ListByStepRunIDs(cancelled, []string{"step-a"}, 0, 10); err == nil {
		t.Fatal("cancelled context must fail")
	}
	if exists, err := identities.HasLogID(cancelled, "a"); err == nil || exists {
		t.Fatal("cancelled identity lookup must fail without claiming success")
	}
}

func TestLogRepositoryValidation(t *testing.T) {
	r := NewLogRepository(nil)
	if err := r.EnsureSchema(context.Background()); err == nil {
		t.Fatal("missing database must fail")
	}
	if _, err := r.LastLineNumber(context.Background(), "step"); err == nil {
		t.Fatal("missing database must fail")
	}
	if exists, err := r.(ILogIdentityRepository).HasLogID(context.Background(), "log"); err == nil || exists {
		t.Fatal("missing database identity lookup must fail without claiming success")
	}
}
