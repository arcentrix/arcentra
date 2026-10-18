// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestInboxMySQLRealOldShapeMigrationAndLargeHistoryPlans(t *testing.T) {
	required := os.Getenv("ARCENTRA_REQUIRE_INBOX_MYSQL")
	require.True(t, required == "" || required == "1")
	dsn := os.Getenv("ARCENTRA_INBOX_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required inbox MySQL fixture missing")
		}
		t.Skip("explicit disposable Inbox MySQL fixture absent; no MySQL acceptance claim")
	}
	cfg, err := manualInboxMySQLConfig(dsn)
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("explicit Inbox MySQL connection unavailable")
	}
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.Empty(t, tables, "migration acceptance requires fresh empty schema")
	for _, collation := range []string{"utf8mb4_0900_ai_ci", "utf8mb4_bin"} {
		t.Run(collation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			require.NoError(t, db.Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation).AutoMigrate(&inboxLegacyRun{}, &inboxLegacyApproval{}))
			// This fresh schema is dedicated to this test; clean only tables it creates.
			defer func() {
				for _, entity := range schemaModels() {
					require.NoError(t, db.Migrator().DropTable(entity))
				}
			}()
			require.NoError(t, db.Create(&inboxLegacyRun{RunID: "legacy", PipelineID: "pipe", ProjectID: "old-project", TriggeredBy: "actor", Status: model.PipelineStatusSuccess, Env: `{"TOKEN":"do-not-rewrite"}`}).Error)
			require.NoError(t, db.Create(&inboxLegacyApproval{ApprovalID: "legacy-approval", PipelineRunID: "legacy", RequestedBy: "actor", CandidateApprovers: `["voter"]`, Status: model.ApprovalStatusApproved}).Error)
			require.NoError(t, MigrateSchema(ctx, db))
			var legacy model.PipelineRun
			require.NoError(t, db.Where("run_id = ?", "legacy").First(&legacy).Error)
			require.Empty(t, legacy.InboxActorID)
			require.Zero(t, legacy.InboxOrigin)
			require.Contains(t, legacy.Env, "do-not-rewrite")
			for _, entity := range []any{&model.InboxDelivery{}, &model.InboxWork{}, &model.RoleGrant{}} {
				var count int64
				require.NoError(t, db.Model(entity).Count(&count).Error)
				require.Zero(t, count)
			}
			now := time.Now().UTC()
			runs := make([]model.PipelineRun, 0, 5000)
			works := make([]model.InboxWork, 0, 5000)
			for i := 0; i < 5000; i++ {
				id := fmt.Sprintf("completed-%05d", i)
				runs = append(runs, model.PipelineRun{RunID: id, PipelineID: "pipe", InboxOrigin: 1, InboxActorID: "actor", Status: model.PipelineStatusSuccess, Env: "{}"})
				works = append(works, model.InboxWork{SourceKind: "run", SourceID: id, State: 1, TerminalDone: true, NextCheckAt: now})
			}
			require.NoError(t, db.CreateInBatches(runs, 100).Error)
			require.NoError(t, db.CreateInBatches(works, 100).Error)
			require.NoError(t, enrollInboxWork(db, "run", "due-source"))
			for _, query := range []string{"SELECT id, run_id FROM pipeline_run WHERE inbox_origin = 1 AND id > 4900 AND id <= 5001 ORDER BY id ASC LIMIT 100", "SELECT * FROM inbox_work WHERE source_kind = 'run' AND state = 0 AND next_check_at <= CURRENT_TIMESTAMP ORDER BY next_check_at ASC, id ASC LIMIT 1"} {
				var explain []struct {
					Key  *string
					Rows int64
					Type string
				}
				require.NoError(t, db.Raw("EXPLAIN "+query).Scan(&explain).Error)
				require.NotEmpty(t, explain)
				for _, row := range explain {
					require.NotNil(t, row.Key)
					require.True(t, strings.Contains(*row.Key, "inbox"), "bounded work must use Inbox range indexes")
					require.NotEqual(t, "ALL", row.Type)
					require.Less(t, row.Rows, int64(1000), "completed history must stay outside the scanned range")
				}
			}
			// Schema replay leaves original legacy rows unbound and new work untouched.
			require.NoError(t, MigrateSchema(ctx, db))
			require.NoError(t, db.Where("run_id = ?", "legacy").First(&legacy).Error)
			require.Empty(t, legacy.InboxActorID)
		})
	}
}
