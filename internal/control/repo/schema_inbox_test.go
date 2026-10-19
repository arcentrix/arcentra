// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type inboxLegacyRun struct {
	ID          uint64 `gorm:"primaryKey"`
	RunID       string `gorm:"type:varchar(64)"`
	PipelineID  string
	ProjectID   string
	TriggeredBy string
	Status      int
	Env         string `gorm:"type:json"`
}

func (inboxLegacyRun) TableName() string { return "pipeline_run" }

type inboxLegacyApproval struct {
	ID                 uint64 `gorm:"primaryKey"`
	ApprovalID         string `gorm:"type:varchar(64)"`
	PipelineRunID      string
	RequestedBy        string
	Status             int
	CandidateApprovers string `gorm:"type:json"`
}

func (inboxLegacyApproval) TableName() string { return "approval_request" }
func TestInboxSchemaOldShapeUpgradeNoActorOrHistoryBackfill(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "old.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&inboxLegacyRun{}, &inboxLegacyApproval{}))
	require.NoError(t, db.Create(&inboxLegacyRun{RunID: "old", PipelineID: "pipeline", ProjectID: "project", TriggeredBy: "actor", Status: model.PipelineStatusSuccess, Env: `{"TOKEN":"legacy-secret-not-read"}`}).Error)
	require.NoError(t, db.Create(&inboxLegacyApproval{ApprovalID: "old-approval", PipelineRunID: "old", RequestedBy: "actor", CandidateApprovers: `["candidate"]`, Status: model.ApprovalStatusApproved}).Error)
	require.NoError(t, MigrateSchema(context.Background(), db))
	var run model.PipelineRun
	require.NoError(t, db.Where("run_id = ?", "old").First(&run).Error)
	require.Empty(t, run.InboxActorID)
	require.Zero(t, run.InboxOrigin)
	require.Equal(t, `{"TOKEN":"legacy-secret-not-read"}`, run.Env)
	var approval model.ApprovalRequest
	require.NoError(t, db.Where("approval_id = ?", "old-approval").First(&approval).Error)
	require.Zero(t, approval.InboxOrigin)
	for _, table := range []any{&model.InboxWork{}, &model.InboxDelivery{}, &model.RoleGrant{}} {
		var n int64
		require.NoError(t, db.Model(table).Count(&n).Error)
		require.Zero(t, n)
	}
	require.True(t, db.Migrator().HasIndex(&model.PipelineRun{}, "idx_run_inbox_repair"))
	require.True(t, db.Migrator().HasIndex(&model.ApprovalRequest{}, "idx_approval_inbox_repair"))
	require.NoError(t, MigrateSchema(context.Background(), db))
}
func TestInboxSchemaPreflightBeforeDDLAndPartialOrigin(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprint(duplicate), func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "old.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			pool, _ := db.DB()
			t.Cleanup(func() { _ = pool.Close() })
			require.NoError(t, db.AutoMigrate(&inboxLegacyRun{}))
			require.NoError(t, db.Create(&inboxLegacyRun{RunID: "same"}).Error)
			if duplicate {
				require.NoError(t, db.Create(&inboxLegacyRun{RunID: "same"}).Error)
			} else {
				require.NoError(t, db.Exec("ALTER TABLE pipeline_run ADD COLUMN inbox_actor_id varchar(64) NOT NULL DEFAULT ''").Error)
			}
			require.Error(t, MigrateSchema(context.Background(), db))
			require.False(t, db.Migrator().HasTable(&model.InboxDelivery{}))
			require.False(t, db.Migrator().HasColumn(&model.PipelineRun{}, "inbox_origin"))
		})
	}
}
func TestInboxLargeHistoryRepairUsesIndexAndWrapsLateLowerID(t *testing.T) {
	db := inboxTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	ctx := context.Background()
	runs := make([]model.PipelineRun, 0, 1000)
	works := make([]model.InboxWork, 0, 1000)
	for i := 1; i <= 1000; i++ {
		if i == 50 {
			continue
		}
		id := fmt.Sprintf("history-%04d", i)
		runs = append(runs, model.PipelineRun{BaseModel: model.BaseModel{ID: uint64(i)}, RunID: id, PipelineID: "pipeline", InboxOrigin: 1, InboxActorID: "actor", Status: model.PipelineStatusSuccess})
		works = append(works, model.InboxWork{SourceKind: "run", SourceID: id, State: 1, TerminalDone: true, NextCheckAt: time.Now()})
	}
	require.NoError(t, db.CreateInBatches(runs, 100).Error)
	require.NoError(t, db.CreateInBatches(works, 100).Error)
	var plan []struct{ Detail string }
	require.NoError(t, db.Raw("EXPLAIN QUERY PLAN SELECT id, run_id FROM pipeline_run WHERE inbox_origin = 1 AND id > 100 AND id <= 1000 ORDER BY id ASC LIMIT 100").Scan(&plan).Error)
	found := false
	for _, line := range plan {
		if strings.Contains(line.Detail, "INDEX") && strings.Contains(line.Detail, "inbox") {
			found = true
		}
	}
	require.True(t, found, "repair must use an indexed origin/keyset range")
	require.NoError(t, r.repairInbox(ctx, "run"))
	var progress model.InboxSweep
	require.NoError(t, db.Where("kind = ?", "run").First(&progress).Error)
	require.EqualValues(t, 101, progress.AfterID)
	// Model a lower transaction ID committing after this round passed its slot.
	require.NoError(t, db.Create(&model.PipelineRun{BaseModel: model.BaseModel{ID: 50}, RunID: "late-low", PipelineID: "pipeline", InboxOrigin: 1, InboxActorID: "actor"}).Error)
	for range 11 {
		require.NoError(t, r.repairInbox(ctx, "run"))
	}
	var work model.InboxWork
	require.NoError(t, db.Where("source_kind = ? AND source_id = ?", "run", "late-low").First(&work).Error)
	require.Zero(t, work.State)
	var done int64
	require.NoError(t, db.Model(&model.InboxWork{}).Where("state = 1").Count(&done).Error)
	require.EqualValues(t, 999, done, "repair must not reopen completed work")
}
func TestInboxOfflineRecoveryOnlyCommittedTerminalEvidence(t *testing.T) {
	db := inboxTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	run := seedInbox(t, db, "interrupted", "actor", model.PipelineStatusRunning)
	require.NoError(t, db.Model(run).Update("end_time", nil).Error)
	inboxTick(t, r)
	var n int64
	require.NoError(t, db.Model(&model.InboxDelivery{}).Count(&n).Error)
	require.Zero(t, n)
	summary, err := RecoverInterruptedRuns(context.Background(), db, []string{run.RunID}, true)
	require.NoError(t, err)
	require.EqualValues(t, 1, summary.Runs)
	dueInbox(t, db)
	inboxTick(t, r)
	page, err := r.InboxList(context.Background(), "actor", "all", "", 20, inboxAllow)
	require.NoError(t, err)
	require.Len(t, page.List, 1)
	require.Equal(t, "failed", page.List[0].Status)
}

func TestInboxSchemaRejectsOriginWithoutActorBeforeDDL(t *testing.T) {
	for _, origin := range []int{0, 1} {
		t.Run(fmt.Sprint(origin), func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "partial.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			pool, _ := db.DB()
			t.Cleanup(func() { _ = pool.Close() })
			require.NoError(t, db.AutoMigrate(&inboxLegacyRun{}))
			require.NoError(t, db.Exec("ALTER TABLE pipeline_run ADD COLUMN inbox_origin integer NOT NULL DEFAULT 0").Error)
			require.NoError(t, db.Create(&inboxLegacyRun{RunID: "partial"}).Error)
			require.NoError(t, db.Table("pipeline_run").Where("run_id = ?", "partial").Update("inbox_origin", origin).Error)
			require.Error(t, MigrateSchema(context.Background(), db))
			require.False(t, db.Migrator().HasColumn(&model.PipelineRun{}, "inbox_actor_id"))
			require.False(t, db.Migrator().HasTable(&model.InboxDelivery{}))
		})
	}
}
