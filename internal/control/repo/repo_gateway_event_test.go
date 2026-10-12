// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/safe"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func gatewayTestDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.WithContext(context.Background()).AutoMigrate(&model.GatewayEvent{}, &model.GatewayEventCursor{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func testGatewayRecord(agent string, seq uint64) *model.GatewayEvent {
	return &model.GatewayEvent{AgentID: agent, Seq: seq, EventID: fmt.Sprintf("evt-%d", seq), EventType: "outbox", Payload: `{"type":"test"}`, Fingerprint: fmt.Sprintf("stable-%d", seq)}
}

func TestGatewayEventRepositoryPersistsAndDeduplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	db := gatewayTestDB(t, path)
	r := NewGatewayEventRepository(db)
	ctx := context.Background()
	for range 2 {
		seq, err := r.StoreBatch(ctx, "agent-a", []*model.GatewayEvent{testGatewayRecord("agent-a", 1), testGatewayRecord("agent-a", 2)})
		if err != nil || seq != 2 {
			t.Fatalf("persist/replay returned %d, %v", seq, err)
		}
	}
	// Outbox event IDs are scoped to the authenticated agent, not globally unique.
	if seq, err := r.StoreBatch(ctx, "agent-b", []*model.GatewayEvent{testGatewayRecord("agent-b", 1)}); err != nil || seq != 1 {
		t.Fatalf("second agent returned %d, %v", seq, err)
	}
	var count int64
	if err := db.WithContext(ctx).Model(&model.GatewayEvent{}).Count(&count).Error; err != nil || count != 3 {
		t.Fatalf("event count = %d, %v", count, err)
	}
	sqlDB, _ := db.DB()
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	r = NewGatewayEventRepository(gatewayTestDB(t, path))
	if seq, err := r.LastAcceptedSeq(ctx, "agent-a"); err != nil || seq != 2 {
		t.Fatalf("reopened cursor = %d, %v", seq, err)
	}
	if seq, err := r.StoreBatch(ctx, "agent-a", []*model.GatewayEvent{testGatewayRecord("agent-a", 2), testGatewayRecord("agent-a", 3)}); err != nil || seq != 3 {
		t.Fatalf("reopened replay+append = %d, %v", seq, err)
	}
}

func TestGatewayEventRepositoryRollsBackGapAndConflicts(t *testing.T) {
	db := gatewayTestDB(t, ":memory:")
	r := NewGatewayEventRepository(db)
	ctx := context.Background()
	if _, err := r.StoreBatch(ctx, "agent", []*model.GatewayEvent{testGatewayRecord("agent", 1), testGatewayRecord("agent", 3)}); !errors.Is(err, ErrGatewayEventSequence) {
		t.Fatalf("gap error = %v", err)
	}
	if seq, err := r.LastAcceptedSeq(ctx, "agent"); err != nil || seq != 0 {
		t.Fatalf("gap advanced cursor to %d, %v", seq, err)
	}
	var count int64
	db.WithContext(ctx).Model(&model.GatewayEvent{}).Count(&count)
	if count != 0 {
		t.Fatalf("gap left %d partial records", count)
	}
	if _, err := r.StoreBatch(ctx, "agent", []*model.GatewayEvent{testGatewayRecord("agent", 1)}); err != nil {
		t.Fatal(err)
	}
	conflict := testGatewayRecord("agent", 1)
	conflict.Fingerprint = "changed-content"
	if _, err := r.StoreBatch(ctx, "agent", []*model.GatewayEvent{conflict}); !errors.Is(err, ErrGatewayEventConflict) {
		t.Fatalf("changed content error = %v", err)
	}
	conflict = testGatewayRecord("agent", 2)
	conflict.EventID = "evt-1"
	if _, err := r.StoreBatch(ctx, "agent", []*model.GatewayEvent{conflict}); !errors.Is(err, ErrGatewayEventConflict) {
		t.Fatalf("reused event ID error = %v", err)
	}
	if seq, err := r.LastAcceptedSeq(ctx, "agent"); err != nil || seq != 1 {
		t.Fatalf("conflict advanced cursor to %d, %v", seq, err)
	}
}

func TestGatewayEventRepositoryRollsBackDatabaseFailure(t *testing.T) {
	db := gatewayTestDB(t, ":memory:")
	r := NewGatewayEventRepository(db)
	ctx := context.Background()
	if err := db.Callback().Update().Before("gorm:update").Register("test:reject_cursor", func(tx *gorm.DB) {
		if tx.Statement.Table == "gateway_event_cursors" {
			tx.AddError(errors.New("injected storage failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if seq, err := r.StoreBatch(ctx, "agent", []*model.GatewayEvent{testGatewayRecord("agent", 1)}); err == nil || seq != 0 {
		t.Fatalf("failed transaction returned %d, %v", seq, err)
	}
	var count int64
	db.WithContext(ctx).Model(&model.GatewayEvent{}).Count(&count)
	if count != 0 {
		t.Fatalf("failed cursor update left %d events", count)
	}
	if seq, err := r.LastAcceptedSeq(ctx, "agent"); err != nil || seq != 0 {
		t.Fatalf("failed cursor update returned %d, %v", seq, err)
	}
	if err := db.Callback().Update().Remove("test:reject_cursor"); err != nil {
		t.Fatal(err)
	}
	if seq, err := r.StoreBatch(ctx, "agent", []*model.GatewayEvent{testGatewayRecord("agent", 1)}); err != nil || seq != 1 {
		t.Fatalf("retry after storage recovery = %d, %v", seq, err)
	}
}

func TestGatewayEventRepositoryDoesNotMigrateAtStartup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	r := NewGatewayEventRepository(db)
	if _, err := r.LastAcceptedSeq(context.Background(), "agent"); err == nil {
		t.Fatal("missing schema must fail closed")
	}
	if db.Migrator().HasTable(&model.GatewayEvent{}) || db.Migrator().HasTable(&model.GatewayEventCursor{}) {
		t.Fatal("constructor/read unexpectedly migrated schema")
	}
	if _, err := NewGatewayEventRepository(nil).StoreBatch(context.Background(), "agent", []*model.GatewayEvent{testGatewayRecord("agent", 1)}); err == nil {
		t.Fatal("missing database must fail")
	}
}

func TestGatewayEventRepositoryConcurrentReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db") + "?_busy_timeout=5000"
	first := gatewayTestDB(t, path)
	second := gatewayTestDB(t, path)
	repos := []IGatewayEventRepository{NewGatewayEventRepository(first), NewGatewayEventRepository(second)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	failures := make(chan error, 8)
	start := make(chan struct{})
	for i := range 8 {
		workers.Add(1)
		safe.Go(func() {
			defer workers.Done()
			<-start
			seq, err := repos[i%len(repos)].StoreBatch(ctx, "agent", []*model.GatewayEvent{testGatewayRecord("agent", 1), testGatewayRecord("agent", 2)})
			if err != nil || seq != 2 {
				failures <- fmt.Errorf("concurrent replay returned %d, %v", seq, err)
			}
		})
	}
	close(start)
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	var count int64
	if err := first.WithContext(ctx).Model(&model.GatewayEvent{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("concurrent replay count = %d, %v", count, err)
	}
}
