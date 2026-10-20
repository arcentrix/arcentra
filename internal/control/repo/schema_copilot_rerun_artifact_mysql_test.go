// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var rerunArtifactActualMetric = regexp.MustCompile(`\(actual time=[0-9.eE+\-]+\.\.[0-9.eE+\-]+ rows=([0-9.eE+\-]+) loops=([0-9.eE+\-]+)\)`)

func validateRerunArtifactActualPlan(plan, table, target string) error {
	lines := strings.Split(strings.TrimSpace(plan), "\n")
	if len(lines) < 2 || len(lines) > 4 || len(plan) > 8192 {
		return fmt.Errorf("unexpected iterator shape")
	}
	var total float64
	for _, line := range lines {
		m := rerunArtifactActualMetric.FindStringSubmatch(line)
		if len(m) != 3 {
			return fmt.Errorf("missing actual work")
		}
		rows, e := strconv.ParseFloat(m[1], 64)
		if e != nil || rows < 0 || rows > 1 || math.Trunc(rows) != rows {
			return fmt.Errorf("unbounded actual rows")
		}
		loops, e := strconv.ParseFloat(m[2], 64)
		if e != nil || loops != 1 {
			return fmt.Errorf("unbounded actual loops")
		}
		total += rows * loops
	}
	leaf := lines[len(lines)-1]
	if !strings.Contains(leaf, "Index lookup on "+table+" using ") && !strings.Contains(leaf, "Index range scan on "+table+" using ") {
		return fmt.Errorf("missing indexed lookup")
	}
	if !strings.Contains(leaf, "job_run_id") || !strings.Contains(leaf, target) || total > 4 {
		return fmt.Errorf("missing exact identity bound")
	}
	return nil
}
func TestCopilotRerunArtifactActualWorkBounds(t *testing.T) {
	good := "-> Limit: 1 row(s) (cost=99 rows=1000000) (actual time=0.01..0.02 rows=0 loops=1)\n -> Index lookup on step_run_artifact using any_job_key (job_run_id='target') (cost=99 rows=1000000) (actual time=0.01..0.02 rows=0 loops=1)"
	require.NoError(t, validateRerunArtifactActualPlan(good, "step_run_artifact", "target"))
	for _, bad := range []string{strings.ReplaceAll(good, "rows=0 loops=1", "rows=2000 loops=1"), strings.ReplaceAll(good, "loops=1", "loops=2000"), strings.ReplaceAll(good, "Index lookup", "Table scan"), strings.ReplaceAll(good, "job_run_id", "pipeline_run_id"), strings.ReplaceAll(good, "target", "foreign")} {
		require.Error(t, validateRerunArtifactActualPlan(bad, "step_run_artifact", "target"))
	}
}

func rerunArtifactSchemaMySQL(t *testing.T, db *gorm.DB, scenario, collation string) {
	t.Helper()
	ctx := context.Background()
	rerunSchemaLegacy(t, db)
	longID := strings.Repeat("历史", 200) + "tail"
	require.NoError(t, db.Create(&model.StepRunArtifact{ArtifactID: "historical-step-artifact", JobRunID: longID}).Error)
	require.NoError(t, db.Create(&model.ProjectArtifact{ArtifactID: "historical-project-artifact", JobRunID: "historical-job"}).Error)
	if scenario == "artifact-wrong-prefix" {
		require.NoError(t, db.Exec("ALTER TABLE step_run_artifact ADD INDEX idx_step_run_artifact_job_run (job_run_id(190))").Error)
	}
	if scenario == "artifact-wrong-full-index" {
		require.NoError(t, db.Exec("ALTER TABLE project_artifact ADD INDEX idx_project_artifact_job_run (job_run_id(32))").Error)
	}
	if strings.Contains(scenario, "wrong") {
		require.ErrorIs(t, MigrateSchema(ctx, db), errRerunSchemaShape)
		require.False(t, db.Migrator().HasIndex(&model.StepRun{}, rerunStepJobIndex))
		require.False(t, db.Migrator().HasTable(&model.CopilotRerunIntent{}))
		return
	}
	if scenario == "artifact-index-only" || scenario == "artifact-both-index-only" {
		require.NoError(t, db.Exec("ALTER TABLE step_run_artifact ADD INDEX idx_step_run_artifact_job_run (job_run_id(191))").Error)
	}
	if scenario == "artifact-project-index-only" || scenario == "artifact-both-index-only" {
		require.NoError(t, db.Exec("ALTER TABLE project_artifact ADD INDEX idx_project_artifact_job_run (job_run_id)").Error)
	}
	_, err := preflightCopilotRerunSchema(ctx, db)
	require.NoError(t, err)
	require.False(t, db.Migrator().HasIndex(&model.StepRun{}, rerunStepJobIndex), "preflight must not apply any index")
	require.False(t, db.Migrator().HasTable(&model.CopilotRerunIntent{}))
	for n := 0; n < 2; n++ {
		require.NoError(t, MigrateSchema(ctx, db))
	}
	for _, entry := range []struct {
		table, name, kind string
		prefix            int64
	}{{"step_run_artifact", rerunStepArtifactJobIndex, "longtext", 191}, {"project_artifact", rerunProjectArtifactJobIndex, "varchar(64)", 0}} {
		columns, err := rerunMySQLColumns(db, entry.table)
		require.NoError(t, err)
		indexes, err := rerunMySQLIndexes(db, entry.table)
		require.NoError(t, err)
		add, err := validateRerunJobAccessShape(columns["job_run_id"], indexes, entry.name, entry.kind, entry.prefix)
		require.NoError(t, err)
		require.False(t, add)
		require.Equal(t, collation, columns["job_run_id"].CollationName.String)
	}
	var old model.StepRunArtifact
	require.NoError(t, db.Where("artifact_id = ?", "historical-step-artifact").First(&old).Error)
	require.Equal(t, longID, old.JobRunID)
	var canonical model.ProjectArtifact
	require.NoError(t, db.Where("artifact_id = ?", "historical-project-artifact").First(&canonical).Error)
	require.Equal(t, "historical-job", canonical.JobRunID)
	if scenario != "artifact-indexes" {
		return
	}
	for _, table := range []string{"step_run_artifact", "project_artifact"} {
		t.Run(table, func(t *testing.T) {
			// No JSON columns in either artifact model. Include immediate neighbors of
			// the absent UUID plus a distant row for independent lock-scope proof.
			const target = "11111111-1111-4111-8111-111111111111"
			if table == "step_run_artifact" {
				rows := make([]model.StepRunArtifact, 2000)
				for i := range rows {
					rows[i] = model.StepRunArtifact{ArtifactID: fmt.Sprintf("artifact-%04d", i), JobRunID: fmt.Sprintf("90000000-0000-4000-8000-%012d", i)}
				}
				require.NoError(t, db.CreateInBatches(rows, 200).Error)
			} else {
				rows := make([]model.ProjectArtifact, 2000)
				for i := range rows {
					rows[i] = model.ProjectArtifact{ArtifactID: fmt.Sprintf("artifact-%04d", i), JobRunID: fmt.Sprintf("90000000-0000-4000-8000-%012d", i)}
				}
				require.NoError(t, db.CreateInBatches(rows, 200).Error)
			}
			for i, id := range []string{"11111111-1111-4111-8111-111111111110", "11111111-1111-4111-8111-111111111112"} {
				require.NoError(t, db.Exec("INSERT INTO "+table+" (artifact_id,job_run_id) VALUES (?,?)", fmt.Sprintf("neighbor-%d", i), id).Error)
			}
			query := "SELECT id FROM " + table + " WHERE job_run_id = ? LIMIT 1 FOR UPDATE"
			var actual string
			require.NoError(t, db.Raw("EXPLAIN ANALYZE "+query, target).Row().Scan(&actual))
			require.NoError(t, validateRerunArtifactActualPlan(actual, table, target), actual)
			t.Logf("artifact exact absent lookup actual work: %s", actual)
			var unrelated uint64
			require.NoError(t, db.Raw("SELECT id FROM "+table+" WHERE artifact_id = ?", "artifact-1999").Scan(&unrelated).Error)
			require.NotZero(t, unrelated)
			holder, hid := rerunMySQLConnection(t, db)
			waiter, wid := rerunMySQLConnection(t, db)
			tx := holder.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
			require.NoError(t, tx.Error)
			defer tx.Rollback()
			var ids []uint64
			require.NoError(t, tx.Raw(query, target).Scan(&ids).Error)
			require.Empty(t, ids)
			done := make(chan error, 1)
			go func() {
				c, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				done <- waiter.WithContext(c).Exec("INSERT INTO "+table+" (artifact_id,job_run_id) VALUES (?,?)", "target-artifact", target).Error
			}()
			rerunMySQLWaitEdge(t, db, hid, wid, done)
			var locks []struct {
				IndexName string
				Count     int
			}
			require.NoError(t, db.Raw(`SELECT l.INDEX_NAME AS index_name, COUNT(*) AS count FROM performance_schema.data_locks l JOIN performance_schema.threads th ON th.THREAD_ID=l.THREAD_ID WHERE th.PROCESSLIST_ID=? AND l.OBJECT_SCHEMA=DATABASE() AND l.OBJECT_NAME=? AND l.LOCK_TYPE='RECORD' GROUP BY l.INDEX_NAME`, hid, table).Scan(&locks).Error)
			expectedIndex := rerunStepArtifactJobIndex
			if table == "project_artifact" {
				expectedIndex = rerunProjectArtifactJobIndex
			}
			total, found := 0, false
			for _, lock := range locks {
				require.Contains(t, []string{expectedIndex, "PRIMARY"}, lock.IndexName)
				total += lock.Count
				found = found || lock.IndexName == expectedIndex
			}
			require.True(t, found, "holder must lock the intended job identity index")
			require.Positive(t, total)
			require.LessOrEqual(t, total, 8, "unrelated records must not be range-locked")
			// A scan lock would block this far-away existing row. Bounded context makes
			// that a test failure rather than mistaking elapsed sleep for lock proof.
			c, cancel := context.WithTimeout(ctx, time.Second)
			err := db.WithContext(c).Exec("UPDATE "+table+" SET name = ? WHERE id = ?", "unrelated updated", unrelated).Error
			cancel()
			require.NoError(t, err)
			require.NoError(t, tx.Commit().Error)
			require.NoError(t, awaitRetryMySQL(t, done))
			require.NoError(t, db.Raw("SELECT id FROM "+table+" WHERE job_run_id = ?", target).Scan(&ids).Error)
			require.Len(t, ids, 1)
		})
	}
}
