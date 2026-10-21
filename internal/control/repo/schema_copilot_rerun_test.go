// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func isRerunSchemaModel(entity any) bool {
	switch entity.(type) {
	case *model.CopilotDefinitionSnapshot, *model.CopilotRerunIntent, *model.CopilotRerunAdmissionGate, *model.CopilotRerunSchedule:
		return true
	}
	return false
}
func rerunSchemaLegacy(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, entity := range schemaModels() {
		if !isRerunSchemaModel(entity) {
			require.NoError(t, db.AutoMigrate(entity))
		}
	}
	for _, entry := range []struct {
		row   any
		field string
	}{{&model.PipelineRun{}, "copilot_rerun_intent_id"}, {&model.PipelineRun{}, "copilot_rerun_gate_id"}, {&model.JobRun{}, "copilot_rerun_intent_id"}, {&model.JobRun{}, "copilot_rerun_gate_id"}, {&model.JobRun{}, "expected_source_commit"}} {
		require.NoError(t, db.Migrator().DropColumn(entry.row, entry.field))
	}
	require.NoError(t, db.Migrator().DropIndex(&model.StepRun{}, rerunStepJobIndex))
	require.NoError(t, db.Migrator().DropIndex(&model.StepRunArtifact{}, rerunStepArtifactJobIndex))
	require.NoError(t, db.Migrator().DropIndex(&model.ProjectArtifact{}, rerunProjectArtifactJobIndex))
}
func TestCopilotRerunSchemaFreshAndUpgradePreserveHistory(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "upgrade"}[legacy], func(t *testing.T) {
			db := executionSchemaDB(t)
			longID := strings.Repeat("历史", 200) + "tail"
			if legacy {
				rerunSchemaLegacy(t, db)
				require.NoError(t, db.Exec("INSERT INTO pipeline_run (run_id,env) VALUES (?,?)", "old-run", "{}").Error)
				require.NoError(t, db.Exec("INSERT INTO job_run (job_run_id,pipeline_run_id,env,artifact_references,artifact_uris) VALUES (?,?,?,?,?)", "old-job", "old-run", "{}", "{}", "{}").Error)
				require.NoError(t, db.Exec("INSERT INTO step_run (step_run_id,job_run_id) VALUES (?,?)", "old-step", longID).Error)
			}
			for i := 0; i < 2; i++ {
				require.NoError(t, MigrateSchema(context.Background(), db))
			}
			for _, entity := range copilotRerunSchemaModels() {
				require.True(t, db.Migrator().HasTable(entity))
				var n int64
				require.NoError(t, db.Model(entity).Count(&n).Error)
				require.Zero(t, n)
			}
			var grants int64
			require.NoError(t, db.Model(&model.RoleGrant{}).Count(&grants).Error)
			require.Zero(t, grants)
			if legacy {
				var run model.PipelineRun
				var job model.JobRun
				var step model.StepRun
				require.NoError(t, db.Where("run_id = ?", "old-run").First(&run).Error)
				require.NoError(t, db.Where("job_run_id = ?", "old-job").First(&job).Error)
				require.NoError(t, db.Where("step_run_id = ?", "old-step").First(&step).Error)
				require.True(t, emptyRunRerun(&run))
				require.True(t, emptyJobRerun(&job))
				require.Equal(t, longID, step.JobRunID)
			}
		})
	}
}

// An additive access-path-only interruption creates no execution authority.
func TestCopilotRerunSchemaIndexOnlyRecovery(t *testing.T) {
	db := executionSchemaDB(t)
	rerunSchemaLegacy(t, db)
	require.NoError(t, db.Migrator().CreateIndex(&model.StepRun{}, rerunStepJobIndex))
	require.False(t, db.Migrator().HasTable(&model.CopilotRerunIntent{}))
	require.False(t, db.Migrator().HasColumn(&model.PipelineRun{}, "CopilotRerunIntentID"))
	for i := 0; i < 2; i++ {
		require.NoError(t, MigrateSchema(context.Background(), db))
	}
	for _, entity := range copilotRerunSchemaModels() {
		require.True(t, db.Migrator().HasTable(entity))
		var count int64
		require.NoError(t, db.Model(entity).Count(&count).Error)
		require.Zero(t, count)
	}
}
func TestCopilotRerunSchemaPartialFailsBeforeAnyDDL(t *testing.T) {
	for _, scenario := range []string{"one-ledger", "missing-ledger-column", "missing-index", "nonunique-index", "wrong-index-columns", "partial-run-markers", "partial-job-markers", "complete-markers-without-ledgers", "orphan-marker", "step-index-columns"} {
		t.Run(scenario, func(t *testing.T) {
			db := executionSchemaDB(t)
			switch scenario {
			case "one-ledger":
				require.NoError(t, db.AutoMigrate(&model.CopilotDefinitionSnapshot{}))
			case "partial-run-markers":
				require.NoError(t, db.Exec("CREATE TABLE pipeline_run (id INTEGER PRIMARY KEY, run_id TEXT, copilot_rerun_intent_id TEXT NOT NULL DEFAULT '')").Error)
			case "partial-job-markers":
				require.NoError(t, db.Exec("CREATE TABLE job_run (id INTEGER PRIMARY KEY, job_run_id TEXT, copilot_rerun_gate_id TEXT NOT NULL DEFAULT '')").Error)
			case "complete-markers-without-ledgers":
				require.NoError(t, db.AutoMigrate(&model.PipelineRun{}, &model.JobRun{}))
			case "orphan-marker":
				require.NoError(t, db.AutoMigrate(&model.PipelineRun{}))
				require.NoError(t, db.Create(&model.PipelineRun{RunID: "orphan", CopilotRerunIntentID: "unexpected"}).Error)
			case "step-index-columns":
				require.NoError(t, db.AutoMigrate(&model.StepRun{}))
				require.NoError(t, db.Migrator().DropIndex(&model.StepRun{}, rerunStepJobIndex))
				require.NoError(t, db.Exec("CREATE INDEX idx_step_run_job_run ON step_run(step_run_id)").Error)
			default:
				require.NoError(t, db.AutoMigrate(&model.PipelineRun{}, &model.JobRun{}))
				require.NoError(t, db.AutoMigrate(copilotRerunSchemaModels()...))
				switch scenario {
				case "missing-ledger-column":
					require.NoError(t, db.Migrator().DropColumn(&model.CopilotDefinitionSnapshot{}, "ciphertext_digest"))
				default:
					require.NoError(t, db.Migrator().DropIndex(&model.CopilotRerunIntent{}, "uk_copilot_rerun_intent"))
					if scenario == "nonunique-index" {
						require.NoError(t, db.Exec("CREATE INDEX uk_copilot_rerun_intent ON copilot_rerun_intent(intent_id)").Error)
					}
					if scenario == "wrong-index-columns" {
						require.NoError(t, db.Exec("CREATE UNIQUE INDEX uk_copilot_rerun_intent ON copilot_rerun_intent(request_id)").Error)
					}
				}
			}
			require.ErrorIs(t, MigrateSchema(context.Background(), db), errRerunSchemaShape)
			require.False(t, db.Migrator().HasTable(&model.User{}), "all preflight must precede every migration write")
		})
	}
}
func validRerunStepColumn() rerunSchemaColumn {
	return rerunSchemaColumn{ColumnName: "job_run_id", ColumnType: "longtext", IsNullable: "YES", CollationName: sql.NullString{String: "utf8mb4_bin", Valid: true}}
}
func validRerunStepIndex() rerunSchemaIndex {
	return rerunSchemaIndex{IndexName: rerunStepJobIndex, NonUnique: 1, SeqInIndex: 1, ColumnName: sql.NullString{String: "job_run_id", Valid: true}, SubPart: sql.NullInt64{Int64: 191, Valid: true}, Collation: sql.NullString{String: "A", Valid: true}, IndexType: "BTREE", IsVisible: "YES"}
}
func TestCopilotRerunSchemaMySQLExactStepShape(t *testing.T) {
	column, index := validRerunStepColumn(), validRerunStepIndex()
	add, err := validateRerunStepJobShape(column, nil)
	require.NoError(t, err)
	require.True(t, add)
	add, err = validateRerunStepJobShape(column, []rerunSchemaIndex{index})
	require.NoError(t, err)
	require.False(t, add)
	for _, scenario := range []string{"varchar", "not-null", "default", "expression", "missing-prefix", "short-prefix", "long-prefix", "unique", "wrong-name", "wrong-column", "descending", "invisible", "composite", "extra-job-index"} {
		t.Run(scenario, func(t *testing.T) {
			c, i := column, index
			indexes := []rerunSchemaIndex{i}
			switch scenario {
			case "varchar":
				c.ColumnType = "varchar(191)"
			case "not-null":
				c.IsNullable = "NO"
			case "default":
				c.ColumnDefault = sql.NullString{Valid: true}
			case "expression":
				c.GenerationExpression = "job_run_id"
			case "missing-prefix":
				indexes[0].SubPart.Valid = false
			case "short-prefix":
				indexes[0].SubPart.Int64 = 190
			case "long-prefix":
				indexes[0].SubPart.Int64 = 192
			case "unique":
				indexes[0].NonUnique = 0
			case "wrong-name":
				indexes[0].IndexName = "unknown"
			case "wrong-column":
				indexes[0].ColumnName.String = "step_run_id"
			case "descending":
				indexes[0].Collation.String = "D"
			case "invisible":
				indexes[0].IsVisible = "NO"
			case "composite":
				i.SeqInIndex = 2
				i.ColumnName.String = "id"
				indexes = append(indexes, i)
			case "extra-job-index":
				i.IndexName = "extra"
				indexes = append(indexes, i)
			}
			_, err := validateRerunStepJobShape(c, indexes)
			require.ErrorIs(t, err, errRerunSchemaShape)
		})
	}
}
func TestCopilotRerunSchemaMySQLLedgerMetadataMatchesModel(t *testing.T) {
	sqliteDB := executionSchemaDB(t)
	mysqlDB := sqliteDB.Session(&gorm.Session{NewDB: true})
	precision := 3
	mysqlDB.Dialector = mysql.New(mysql.Config{SkipInitializeWithVersion: true, DefaultDatetimePrecision: &precision})
	for _, entity := range copilotRerunSchemaModels() {
		stmt := &gorm.Statement{DB: mysqlDB}
		require.NoError(t, stmt.Parse(entity))
		for _, field := range stmt.Schema.Fields {
			if field.DBName == "" {
				continue
			}
			// Literal information_schema spelling, independently of GORM DDL output.
			columnType := field.TagSettings["TYPE"]
			if columnType == "" {
				switch field.DataType {
				case schema.Uint:
					if field.Size == 64 {
						columnType = "bigint unsigned"
					} else {
						require.Equal(t, 32, field.Size)
						columnType = "int unsigned"
					}
				case schema.Bool:
					columnType = "tinyint(1)"
				case schema.Time:
					columnType = "datetime(3)"
				default:
					t.Fatalf("unrepresented metadata type %s for %s", field.DataType, field.DBName)
				}
			}
			c := rerunSchemaColumn{ColumnName: field.DBName, ColumnType: columnType, IsNullable: "YES"}
			if field.NotNull || field.PrimaryKey {
				c.IsNullable = "NO"
			}
			if field.AutoIncrement {
				c.Extra = "auto_increment"
			} else if field.HasDefaultValue {
				value := field.DefaultValue
				if value == "false" {
					value = "0"
				}
				c.ColumnDefault = sql.NullString{Valid: true, String: value}
			}
			require.True(t, rerunLedgerColumnValid(mysqlDB, field, c), field.DBName)
			bad := c
			bad.ColumnType = "varchar(1)"
			require.False(t, rerunLedgerColumnValid(mysqlDB, field, bad), field.DBName)
			bad = c
			if bad.IsNullable == "YES" {
				bad.IsNullable = "NO"
			} else {
				bad.IsNullable = "YES"
			}
			require.False(t, rerunLedgerColumnValid(mysqlDB, field, bad), field.DBName)
			bad = c
			bad.IsNullable = "unknown"
			require.False(t, rerunLedgerColumnValid(mysqlDB, field, bad), field.DBName)
			bad = c
			bad.ColumnDefault = sql.NullString{Valid: true, String: "unexpected"}
			require.False(t, rerunLedgerColumnValid(mysqlDB, field, bad), field.DBName)
			bad = c
			bad.Extra = "DEFAULT_GENERATED"
			require.False(t, rerunLedgerColumnValid(mysqlDB, field, bad), field.DBName)
			if field.DataType == schema.Bool {
				for _, incompatible := range []string{"boolean", "tinyint", "tinyint(1) unsigned", "int"} {
					bad = c
					bad.ColumnType = incompatible
					require.False(t, rerunLedgerColumnValid(mysqlDB, field, bad), incompatible)
				}
			}
			if field.DataType == schema.Time {
				for _, incompatible := range []string{"datetime", "datetime(6)", "timestamp(3)", "datetime(3) NULL"} {
					bad = c
					bad.ColumnType = incompatible
					require.False(t, rerunLedgerColumnValid(mysqlDB, field, bad), incompatible)
				}
			}
		}
	}
}

func TestCopilotRerunSchemaPreservesExistingCiphertextAndReceipts(t *testing.T) {
	r, saved, in := rerunConfirmFixture(t)
	ctx := context.Background()
	db := r.database.Database()
	receipt, err := r.CopilotRerunConfirm(ctx, in, permitRerun, testRerunReview)
	require.NoError(t, err)
	var before model.CopilotDefinitionSnapshot
	require.NoError(t, db.Where("snapshot_id = ?", saved.SnapshotID).First(&before).Error)
	require.NoError(t, MigrateSchema(ctx, db))
	require.NoError(t, MigrateSchema(ctx, db))
	var after model.CopilotDefinitionSnapshot
	require.NoError(t, db.Where("snapshot_id = ?", saved.SnapshotID).First(&after).Error)
	require.Equal(t, before, after)
	observed, err := r.CopilotRerunInspect(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
	require.NoError(t, err)
	require.Equal(t, receipt, observed)
}

func TestCopilotRerunNonceSchemaUpgradeOnlyUnclaimedInertShape(t *testing.T) {
	for _, mutation := range []string{"none", "gate-claimed-at", "job-running", "schedule-claimed"} {
		t.Run(mutation, func(t *testing.T) {
			r, saved, _, _ := rerunSealFixture(t)
			db := r.database.Database()
			require.NoError(t, db.Exec("ALTER TABLE copilot_rerun_admission_gate DROP COLUMN admission_nonce_digest").Error)
			switch mutation {
			case "gate-claimed-at":
				require.NoError(t, db.Model(&model.CopilotRerunAdmissionGate{}).Where("gate_id = ?", saved.GateID).Update("claimed_at", time.Now()).Error)
			case "job-running":
				require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", saved.NewJobRunID).Update("status", model.JobRunStatusRunning).Error)
			case "schedule-claimed":
				require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("state", "claimed").Error)
			}
			plan, err := preflightCopilotRerunSchema(context.Background(), db)
			require.False(t, db.Migrator().HasColumn(&model.CopilotRerunAdmissionGate{}, "admission_nonce_digest"), "preflight cannot apply DDL")
			if mutation != "none" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.True(t, plan.addAdmissionNonce)
			require.NoError(t, plan.apply(context.Background(), db))
			var gate model.CopilotRerunAdmissionGate
			require.NoError(t, db.Where("gate_id = ?", saved.GateID).First(&gate).Error)
			require.Empty(t, gate.AdmissionNonceDigest)
			_, err = preflightCopilotRerunSchema(context.Background(), db)
			require.NoError(t, err)
		})
	}
}
