// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
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
			runs := make([]model.PipelineRun, 0, 10001)
			works := make([]model.InboxWork, 0, 5000)
			for i := 0; i < 10001; i++ {
				id := fmt.Sprintf("completed-%05d", i)
				origin, actor := 0, ""
				if i <= 5000 || i == 10000 {
					origin, actor = 1, "actor"
				}
				// Explicit IDs make both excluded boundary sentinels deterministic.
				runs = append(runs, model.PipelineRun{BaseModel: model.BaseModel{ID: uint64(i + 2)}, RunID: id, PipelineID: "pipe", InboxOrigin: origin, InboxActorID: actor, Status: model.PipelineStatusSuccess, Env: "{}"})
				if i < 5000 {
					works = append(works, model.InboxWork{SourceKind: "run", SourceID: id, State: 1, TerminalDone: true, NextCheckAt: now.Add(-24 * time.Hour)})
				}
			}
			require.NoError(t, db.WithContext(ctx).CreateInBatches(runs, 100).Error)
			require.NoError(t, db.WithContext(ctx).CreateInBatches(works, 100).Error)
			require.NoError(t, enrollInboxWork(db.WithContext(ctx), "run", "due-source"))
			// Database-relative past/future values avoid any clock or timestamp-
			// precision inference from the earlier zero-hit execution plan.
			require.NoError(t, db.WithContext(ctx).Model(&model.InboxWork{}).Where("source_kind = ? AND source_id = ?", "run", "due-source").UpdateColumn("next_check_at", gorm.Expr("CURRENT_TIMESTAMP - INTERVAL 1 DAY")).Error)
			require.NoError(t, db.WithContext(ctx).Create(&model.InboxWork{SourceKind: "approval", SourceID: "other-kind", NextCheckAt: now.Add(-48 * time.Hour)}).Error)
			require.NoError(t, enrollInboxWork(db.WithContext(ctx), "run", "future-source"))
			require.NoError(t, db.WithContext(ctx).Model(&model.InboxWork{}).Where("source_kind = ? AND source_id = ?", "run", "future-source").UpdateColumn("next_check_at", gorm.Expr("CURRENT_TIMESTAMP + INTERVAL 1 DAY")).Error)

			const repairSQL = "SELECT id, run_id FROM pipeline_run WHERE inbox_origin = 1 AND id > 4900 AND id <= 5001 ORDER BY id ASC LIMIT 100"
			const dueSQL = "SELECT * FROM inbox_work WHERE source_kind = 'run' AND state = 0 AND next_check_at <= CURRENT_TIMESTAMP ORDER BY next_check_at ASC, id ASC LIMIT 1"
			densePlan, denseExecution := captureInboxMySQLPlan(t, ctx, db, "repair-dense", repairSQL)
			duePlan, dueExecution := captureInboxMySQLPlan(t, ctx, db, "due-work", dueSQL)
			require.NoError(t, validateInboxMySQLPlan(densePlan, denseExecution, "repair", 100))
			require.NoError(t, validateInboxMySQLPlan(duePlan, dueExecution, "due", 1))
			var selected []model.PipelineRun
			require.NoError(t, db.WithContext(ctx).Raw(repairSQL).Scan(&selected).Error)
			require.Len(t, selected, 100)
			for i, row := range selected {
				require.EqualValues(t, 4901+i, row.ID)
			}
			var due []model.InboxWork
			require.NoError(t, db.WithContext(ctx).Raw(dueSQL).Scan(&due).Error)
			require.Len(t, due, 1)
			require.Equal(t, "due-source", due[0].SourceID)
			require.Equal(t, "run", due[0].SourceKind)
			require.Zero(t, due[0].State)

			// Keep matching sentinels at 4900 and 5002 outside the window, and
			// only its first/last IDs inside. Thousands of irrelevant rows remain.
			require.NoError(t, db.WithContext(ctx).Model(&model.PipelineRun{}).Where("id > 4901 AND id < 5001").Updates(map[string]any{"inbox_origin": 0, "inbox_actor_id": ""}).Error)
			sparsePlan, sparseExecution := captureInboxMySQLPlan(t, ctx, db, "repair-sparse-boundaries", repairSQL)
			require.NoError(t, validateInboxMySQLPlan(sparsePlan, sparseExecution, "repair", 2))
			selected = nil
			require.NoError(t, db.WithContext(ctx).Raw(repairSQL).Scan(&selected).Error)
			require.Len(t, selected, 2)
			require.EqualValues(t, 4901, selected[0].ID)
			require.EqualValues(t, 5001, selected[1].ID)

			// A production sweep's upper bound may be far away. This separate
			// 5101-slot window contains 5099 valid legacy/origin0 rows and only
			// two eligible rows. A PRIMARY range cannot prove this sparse case.
			require.NoError(t, db.WithContext(ctx).Model(&model.PipelineRun{}).Where("id > 4901 AND id < 10001").Updates(map[string]any{"inbox_origin": 0, "inbox_actor_id": ""}).Error)
			require.NoError(t, db.WithContext(ctx).Model(&model.PipelineRun{}).Where("id = ?", 10001).Updates(map[string]any{"inbox_origin": 1, "inbox_actor_id": "actor"}).Error)
			const wideSQL = "SELECT id, run_id FROM pipeline_run WHERE inbox_origin = 1 AND id > 4900 AND id <= 10001 ORDER BY id ASC LIMIT 100"
			widePlan, wideExecution := captureInboxMySQLPlan(t, ctx, db, "repair-wide-sparse", wideSQL)
			require.NoError(t, validateInboxMySQLPlan(widePlan, wideExecution, "repair-wide", 2))
			selected = nil
			require.NoError(t, db.WithContext(ctx).Raw(wideSQL).Scan(&selected).Error)
			require.Len(t, selected, 2)
			require.EqualValues(t, 4901, selected[0].ID)
			require.EqualValues(t, 10001, selected[1].ID)

			// Schema replay leaves original legacy rows unbound and new work untouched.
			require.NoError(t, MigrateSchema(ctx, db))
			require.NoError(t, db.Where("run_id = ?", "legacy").First(&legacy).Error)
			require.Empty(t, legacy.InboxActorID)
		})
	}
}

// These diagnostics and the parser below intentionally target the pinned MySQL
// 8.0.40 TREE format. Unknown operators or syntax fail closed, never imply a pass.
// Accepted shapes are Limit -> [Filter] -> exact range, plus the observed wide
// Limit -> Sort -> Filter -> Intersect -> one exact range. Numeric checks bound
// narrow repair to at most 301 iterator row-visits (100+100+101), origin-aware
// sparse ranges to at most six, due-work to two, and the intersection to ten.
// This accounts for reported iterator work, not physical I/O or page reads.
type inboxMySQLQueryPlan struct {
	Table        string
	Key          *string
	PossibleKeys *string
	Rows         int64
	Type         string
	Filtered     float64
	Extra        *string
}

func captureInboxMySQLPlan(t *testing.T, ctx context.Context, db *gorm.DB, name, query string) ([]inboxMySQLQueryPlan, string) {
	t.Helper()
	var plan []inboxMySQLQueryPlan
	require.NoError(t, db.WithContext(ctx).Raw("EXPLAIN "+query).Scan(&plan).Error)
	diagnostic, err := json.Marshal(plan)
	require.NoError(t, err)
	require.LessOrEqual(t, len(diagnostic), 8192)
	t.Logf("synthetic Inbox plan %s: %s", name, diagnostic)
	rows, err := db.WithContext(ctx).Raw("EXPLAIN ANALYZE " + query).Rows()
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next(), "execution plan must contain one row")
	var execution string
	require.NoError(t, rows.Scan(&execution))
	require.LessOrEqual(t, len(execution), 8192)
	t.Logf("synthetic Inbox execution %s: %s", name, execution)
	require.False(t, rows.Next(), "execution plan must contain only one row")
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	return plan, execution
}

const inboxPlanNumber = `[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?`

var inboxPlanLine = regexp.MustCompile(`^( *)-> (.+)  \(cost=` + inboxPlanNumber + ` rows=` + inboxPlanNumber + `\) \(actual time=(` + inboxPlanNumber + `)\.\.(` + inboxPlanNumber + `) rows=(` + inboxPlanNumber + `) loops=(` + inboxPlanNumber + `)\)$`)
var inboxDueRange = regexp.MustCompile(`^Index range scan on inbox_work using idx_inbox_work_due over \(source_kind = 'run' AND state = 0 AND next_check_at <= '[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,6})?'\), with index condition: \(\(inbox_work.state = 0\) and \(inbox_work.source_kind = 'run'\) and \(inbox_work.next_check_at <= <cache>\(now\(\)\)\)\)$`)

func validateInboxMySQLPlan(plan []inboxMySQLQueryPlan, execution, kind string, outputRows int) error {
	if len(plan) != 1 || plan[0].Key == nil || plan[0].Rows <= 0 || plan[0].Rows >= 1000 {
		return fmt.Errorf("expected one bounded estimated range plan")
	}
	intersection := plan[0].Type == "index_merge"
	if intersection {
		// This one observed 8.0.40 intersection uses PRIMARY only as a row-ID
		// filter, not a second retrieval leaf (MySQL Index Merge documentation):
		// https://dev.mysql.com/doc/refman/8.0/en/index-merge-optimization.html
		// Do not generalize this exception to unions or multi-leaf intersections.
		// Estimated Rows is statistical, not actual work: retain the bounded
		// positive estimate above, but prove exactly two rows at every iterator
		// below rather than requiring the estimate to equal the actual count.
		if kind != "repair-wide" || outputRows != 2 || plan[0].Table != "pipeline_run" ||
			*plan[0].Key != "idx_run_inbox_origin,PRIMARY" || plan[0].Filtered != 100 ||
			plan[0].Extra == nil || *plan[0].Extra != "Using intersect(idx_run_inbox_origin,PRIMARY); Using where; Using filesort" ||
			plan[0].PossibleKeys == nil || *plan[0].PossibleKeys != "PRIMARY,idx_run_inbox_origin,idx_run_inbox_repair" {
			return fmt.Errorf("unknown index-merge metadata")
		}
	} else if plan[0].Type != "range" {
		return fmt.Errorf("expected a supported bounded access method")
	}
	if len(execution) == 0 || len(execution) > 8192 {
		return fmt.Errorf("execution plan exceeds diagnostic bounds")
	}
	lines := strings.Split(strings.TrimSuffix(execution, "\n"), "\n")
	if (intersection && len(lines) != 5) || (!intersection && (len(lines) < 2 || len(lines) > 3)) {
		return fmt.Errorf("unknown execution plan shape")
	}
	limit, maxScan, upperID := 100, 101, 5001 // The narrow unique-ID window has at most 101 rows.
	if kind == "repair-wide" {
		maxScan, upperID = 2, 10001
	}
	if kind == "due" {
		limit, maxScan = 1, 1
		if outputRows != 1 || plan[0].Table != "inbox_work" || *plan[0].Key != "idx_inbox_work_due" || len(lines) != 2 {
			return fmt.Errorf("unexpected due-work range plan")
		}
	} else if (kind != "repair" && kind != "repair-wide") || plan[0].Table != "pipeline_run" || (outputRows != 100 && outputRows != 2) || (kind == "repair-wide" && outputRows != 2) {
		return fmt.Errorf("unexpected repair range plan")
	}
	descriptions := make([]string, len(lines))
	iteratorRowVisits := float64(0)
	for i, line := range lines {
		match := inboxPlanLine.FindStringSubmatch(line)
		if match == nil || len(match[1]) != i*4 {
			return fmt.Errorf("unknown execution iterator syntax at %d", i)
		}
		descriptions[i] = match[2]
		values := make([]float64, 4)
		for j := range values {
			value, err := strconv.ParseFloat(match[j+3], 64)
			if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
				return fmt.Errorf("invalid actual iterator metric at %d", i)
			}
			values[j] = value
		}
		if values[1] < values[0] || values[3] != 1 || values[2] != math.Trunc(values[2]) || values[2] < float64(outputRows) || values[2] > float64(maxScan) {
			return fmt.Errorf("excessive or anomalous actual iterator work at %d", i)
		}
		iteratorRowVisits += values[2] * values[3]
		if (i < len(lines)-1 || len(lines) == 2 || *plan[0].Key != "PRIMARY") && values[2] != float64(outputRows) {
			return fmt.Errorf("unexpected actual output at %d", i)
		}
	}
	if descriptions[0] != fmt.Sprintf("Limit: %d row(s)", limit) {
		return fmt.Errorf("missing exact query limit")
	}
	if intersection {
		// Exact single-chain shape observed under both collations. Each of the
		// five iterators reports two rows and one loop: ten iterator row-visits,
		// not a claim about physical I/O or storage-engine page reads.
		expected := []string{
			"Limit: 100 row(s)",
			"Sort: pipeline_run.id, limit input to 100 row(s) per chunk",
			"Filter: ((pipeline_run.inbox_origin = 1) and (pipeline_run.id > 4900) and (pipeline_run.id <= 10001))",
			"Intersect rows sorted by row ID",
			"Index range scan on pipeline_run using idx_run_inbox_origin over (inbox_origin = 1 AND 4900 < id <= 10001)",
		}
		for i, description := range descriptions {
			if description != expected[i] {
				return fmt.Errorf("unknown index-merge iterator at %d", i)
			}
		}
		if iteratorRowVisits != 10 {
			return fmt.Errorf("unexpected aggregate index-merge iterator work")
		}
		return nil
	}
	leaf := descriptions[len(descriptions)-1]
	if kind == "due" {
		if !inboxDueRange.MatchString(leaf) {
			return fmt.Errorf("unknown or unbounded due-work scan")
		}
		return nil
	}
	predicate := fmt.Sprintf("((pipeline_run.inbox_origin = 1) and (pipeline_run.id > 4900) and (pipeline_run.id <= %d))", upperID)
	if len(lines) == 3 && descriptions[1] != "Filter: "+predicate {
		return fmt.Errorf("unknown repair filter")
	}
	key := *plan[0].Key
	rangeText := fmt.Sprintf("(inbox_origin = 1 AND 4900 < id <= %d)", upperID)
	switch key {
	case "PRIMARY":
		if kind == "repair-wide" {
			return fmt.Errorf("wide sparse repair requires an origin-aware bounded range")
		}
		rangeText = "(4900 < id <= 5001)"
		if len(lines) != 3 {
			return fmt.Errorf("primary range must retain origin filter")
		}
	case "idx_run_inbox_repair", "idx_run_inbox_origin":
	default:
		return fmt.Errorf("unknown repair range index")
	}
	prefix := "Index range scan on pipeline_run using " + key + " over " + rangeText
	if leaf != prefix && leaf != prefix+", with index condition: "+predicate {
		return fmt.Errorf("unknown or unbounded repair scan")
	}
	return nil
}
