// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func rerunDispatchMySQLSparseHistory(now time.Time) []model.CopilotRerunSchedule {
	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	rows := make([]model.CopilotRerunSchedule, 0, 32*128+24)
	for batch := 0; batch < 32; batch++ {
		for i := 0; i < 128; i++ {
			rows = append(rows, model.CopilotRerunSchedule{ScheduleID: uuid.NewString(), IntentID: uuid.NewString(), OwnerUserID: "synthetic", ProjectID: "synthetic", PipelineID: "synthetic", RunID: uuid.NewString(), JobRunID: uuid.NewString(), State: CopilotRerunSchedulePending, Version: 1, LeaseUntil: &future, CreatedAt: now, ExpiresAt: future})
		}
		if batch < 24 {
			rows = append(rows, model.CopilotRerunSchedule{ScheduleID: uuid.NewString(), IntentID: uuid.NewString(), OwnerUserID: "synthetic", ProjectID: "synthetic", PipelineID: "synthetic", RunID: uuid.NewString(), JobRunID: uuid.NewString(), State: CopilotRerunSchedulePending, Version: 1, CancelRequested: true, CreatedAt: past, ExpiresAt: past})
		}
	}
	return rows
}

// This uses the same disposable database gate as the existing rerun acceptance
// lane. EXPLAIN ANALYZE executes the actual captured candidate query; merely
// finding an index in metadata or seeing LIMIT at the top is insufficient.
func TestCopilotRerunDispatchMySQLRequiredGate(t *testing.T) {
	required := os.Getenv("ARCENTRA_REQUIRE_COPILOT_RERUN_MYSQL")
	require.True(t, required == "" || required == "1")
	dsn := os.Getenv("ARCENTRA_COPILOT_RERUN_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required disposable rerun MySQL missing")
		}
		t.Skip("real MySQL absent; no dispatch scan/concurrency acceptance claim")
	}
	cfg, err := rerunConfirmMySQLConfig(dsn)
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(8)
	defer pool.Close()
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.Empty(t, tables)
	for _, collation := range []string{"utf8mb4_0900_ai_ci", "utf8mb4_bin"} {
		t.Run(collation, func(t *testing.T) {
			models := append(rerunConfirmMySQLModels(), &model.Agent{})
			require.NoError(t, db.Session(&gorm.Session{NewDB: true}).Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation).AutoMigrate(models...))
			defer func() {
				for i := len(models) - 1; i >= 0; i-- {
					require.NoError(t, db.Migrator().DropTable(models[i]))
				}
			}()
			now := time.Now().UTC().Truncate(time.Millisecond)
			past := now.Add(-time.Hour)
			// Cold leases must remain ahead of every NULL-bucket cursor, not just
			// before the first page. Otherwise a tiny PRIMARY tail legitimately wins
			// the cost estimate and does not exercise sparse-history index access.
			// Interleave cancelled/expired due rows and leave 1,024 cold rows before
			// the real work row; no eligibility filter or index hint alters the query.
			rows := rerunDispatchMySQLSparseHistory(now)
			require.NoError(t, db.CreateInBatches(&rows, 128).Error)
			r, saved, lease, in := rerunSealMySQLSeed(t, db)
			require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("lease_until", nil).Error)
			require.NoError(t, db.Exec("ANALYZE TABLE copilot_rerun_schedule").Error)
			type capturedQuery struct {
				sql  string
				args []any
			}
			var captured []capturedQuery
			require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:capture-dispatch-scan", func(tx *gorm.DB) {
				if tx.Statement.Table == "copilot_rerun_schedule" && strings.HasPrefix(tx.Statement.SQL.String(), "SELECT id, schedule_id, lease_until") && len(tx.Statement.Vars) > 0 && tx.Statement.Vars[0] == CopilotRerunSchedulePending {
					captured = append(captured, capturedQuery{tx.Statement.SQL.String(), append([]any(nil), tx.Statement.Vars...)})
				}
			}))
			cursor := CopilotRerunDispatchCursor{}
			found := false
			for turn := 0; turn < 24; turn++ {
				page, err := r.CopilotRerunScanDispatch(context.Background(), cursor, 8)
				require.NoError(t, err)
				require.LessOrEqual(t, page.Inspected, 8)
				cursor = page.Next
				for _, work := range page.Work {
					found = found || work.ScheduleID == saved.ScheduleID
				}
			}
			require.True(t, found, "sparse cancelled history starved active work")
			require.NoError(t, db.Callback().Query().Remove("test:capture-dispatch-scan"))
			require.Len(t, captured, 8)
			for _, query := range captured {
				analyzed, err := db.Raw("EXPLAIN ANALYZE "+query.sql, query.args...).Rows()
				require.NoError(t, err)
				var plan strings.Builder
				for analyzed.Next() {
					var line string
					require.NoError(t, analyzed.Scan(&line))
					plan.WriteString(line)
					plan.WriteByte('\n')
				}
				require.NoError(t, analyzed.Err())
				require.NoError(t, analyzed.Close())
				text := plan.String()
				require.Contains(t, text, "idx_copilot_rerun_schedule_due")
				require.NotContains(t, strings.ToLower(text), "table scan")
				matches := regexp.MustCompile(`actual time=[^)]* rows=([0-9.e+]+) loops=([0-9.e+]+)`).FindAllStringSubmatch(text, -1)
				require.NotEmpty(t, matches)
				for _, match := range matches {
					rows, err := strconv.ParseFloat(match[1], 64)
					require.NoError(t, err)
					loops, err := strconv.ParseFloat(match[2], 64)
					require.NoError(t, err)
					require.LessOrEqual(t, rows*loops, float64(16), "candidate query examined excessive rows")
				}
				t.Logf("dispatch captured candidate query actual work: %s", text)
			}
			// Takeover retains exact seal and makes the old controller incapable of
			// acquiring a fresh publication reference.
			require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("lease_until", past).Error)
			current, err := r.CopilotRerunAcquireLease(context.Background(), saved.ScheduleID, "dispatch-controller", time.Minute)
			require.NoError(t, err)
			work, err := r.CopilotRerunReadDispatchWork(context.Background(), saved.ScheduleID)
			require.NoError(t, err)
			in.ExpectedScheduleVersion = work.ScheduleVersion
			_, err = r.CopilotRerunSeal(context.Background(), *current, in, permitRerun, testRerunSealBuilder)
			require.NoError(t, err)
			_, err = r.CopilotRerunDispatchReference(context.Background(), lease, permitRerun, testRerunClaimValidator)
			require.Error(t, err)
			ref, err := r.CopilotRerunDispatchReference(context.Background(), *current, permitRerun, testRerunClaimValidator)
			require.NoError(t, err)
			require.Equal(t, in.SealID, ref.SealID)
		})
	}
}
