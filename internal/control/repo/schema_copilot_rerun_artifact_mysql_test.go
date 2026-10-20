// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// EXPLAIN ANALYZE reports actual rows per iterator and loops, independently of
// optimizer estimates. Covering changes where values are read, not lookup scope.
// https://dev.mysql.com/doc/refman/8.0/en/explain.html
// https://dev.mysql.com/doc/dev/mysql-server/8.0.45/explain__access__path_8cc.html
const rerunArtifactPlanNumber = `[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]{1,3})?`

var rerunArtifactActualLine = regexp.MustCompile(`^( *)-> (.+)  \(cost=` + rerunArtifactPlanNumber + ` rows=` + rerunArtifactPlanNumber + `\) \(actual time=(` + rerunArtifactPlanNumber + `)\.\.(` + rerunArtifactPlanNumber + `) rows=(` + rerunArtifactPlanNumber + `) loops=(` + rerunArtifactPlanNumber + `)\)$`)

func validateRerunArtifactActualPlan(plan, table, target string) error {
	if len(plan) == 0 || len(plan) > 8192 || target == "" || strings.ContainsAny(target, "'\\\r\n") {
		return fmt.Errorf("unexpected plan or identity bounds")
	}
	index := ""
	switch table {
	case "step_run_artifact":
		index = rerunStepArtifactJobIndex
	case "project_artifact":
		index = rerunProjectArtifactJobIndex
	default:
		return fmt.Errorf("unexpected artifact table")
	}
	lines := strings.Split(strings.TrimSuffix(plan, "\n"), "\n")
	if len(lines) < 2 || len(lines) > 4 {
		return fmt.Errorf("unexpected iterator shape")
	}
	filter := "Filter: (" + table + ".job_run_id = '" + target + "')"
	lookup := " on " + table + " using " + index + " (job_run_id='" + target + "')"
	rangeLookup := "Index range scan on " + table + " using " + index + " over (job_run_id = '" + target + "')"
	var total float64
	for i, line := range lines {
		m := rerunArtifactActualLine.FindStringSubmatch(line)
		if m == nil || len(m[1]) != i*4 {
			return fmt.Errorf("missing actual work or malformed iterator")
		}
		metric := make([]float64, 4)
		for j := range metric {
			value, err := strconv.ParseFloat(m[j+3], 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || len(m[j+3]) > 64 {
				return fmt.Errorf("invalid actual metric")
			}
			if j >= 2 {
				// Bound token/exponent lengths before exact parsing. Float rounding
				// must not turn fractional work into an allowed zero or one.
				exact, ok := new(big.Rat).SetString(m[j+3])
				if !ok || !exact.IsInt() || exact.Sign() < 0 || exact.Cmp(big.NewRat(1, 1)) > 0 || (j == 3 && exact.Sign() == 0) {
					return fmt.Errorf("unbounded actual rows or loops")
				}
			}
			metric[j] = value
		}
		if metric[1] < metric[0] {
			return fmt.Errorf("invalid actual timing")
		}
		total += metric[2] * metric[3]
		switch {
		case i == 0:
			if m[2] != "Limit: 1 row(s)" {
				return fmt.Errorf("missing exact query limit")
			}
		case i == len(lines)-1:
			if m[2] != "Index lookup"+lookup && m[2] != rangeLookup &&
				!(table == "project_artifact" && m[2] == "Covering index lookup"+lookup) {
				return fmt.Errorf("missing exact indexed lookup")
			}
		default:
			if m[2] != filter {
				return fmt.Errorf("unexpected intermediate iterator")
			}
		}
	}
	if total > 4 {
		return fmt.Errorf("unbounded total actual work")
	}
	return nil
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
