// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
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

func rerunSchemaMySQLSession(db *gorm.DB, collation string) *gorm.DB {
	// Keep the DDL setting, but clone a clean statement for each subsequent model
	// operation. A reusable Set chain otherwise retains the first Create's model.
	return db.Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation).Session(&gorm.Session{})
}

// Uses the production explicit migration, never substitutes model-only creation
// for either fresh migration or upgrade/replay assertions.
func TestCopilotRerunSchemaMySQLRequiredGate(t *testing.T) {
	required := os.Getenv("ARCENTRA_REQUIRE_COPILOT_RERUN_MYSQL")
	require.True(t, required == "" || required == "1")
	dsn := os.Getenv("ARCENTRA_COPILOT_RERUN_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required disposable rerun MySQL missing")
		}
		t.Skip("real MySQL absent; no migration acceptance claim")
	}
	config, err := rerunConfirmMySQLConfig(dsn)
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(config.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.Empty(t, tables, "fresh dedicated empty schema required")
	for _, collation := range []string{"utf8mb4_0900_ai_ci", "utf8mb4_bin"} {
		t.Run(collation, func(t *testing.T) {
			for _, scenario := range []string{"artifact-indexes", "artifact-wrong-prefix", "artifact-wrong-full-index", "artifact-index-only", "artifact-project-index-only", "artifact-both-index-only", "nonce-upgrade", "nonce-gate-claimed", "nonce-job-running", "nonce-schedule-claimed", "fresh", "audit-preflight-order", "upgrade", "index-only", "complete-markers", "view-ledger", "partial-marker", "wrong-prefix", "late-preflight"} {
				t.Run(scenario, func(t *testing.T) {
					defer func() {
						tables, err := db.Migrator().GetTables()
						require.NoError(t, err)
						for _, table := range tables {
							require.NoError(t, db.Migrator().DropTable(table))
						}
					}()
					scoped := rerunSchemaMySQLSession(db, collation)
					ctx := context.Background()
					if strings.HasPrefix(scenario, "artifact-") {
						rerunArtifactSchemaMySQL(t, scoped, scenario, collation)
						return
					}
					if strings.HasPrefix(scenario, "nonce-") {
						require.NoError(t, MigrateSchema(ctx, scoped))
						_, saved, _, _ := rerunSealMySQLSeed(t, db)
						require.NoError(t, db.Exec("ALTER TABLE copilot_rerun_admission_gate DROP COLUMN admission_nonce_digest").Error)
						require.NoError(t, db.Migrator().DropIndex(&model.StepRun{}, rerunStepJobIndex))
						switch scenario {
						case "nonce-gate-claimed":
							require.NoError(t, db.Model(&model.CopilotRerunAdmissionGate{}).Where("gate_id = ?", saved.GateID).Update("claimed_at", time.Now().UTC().Truncate(time.Millisecond)).Error)
						case "nonce-job-running":
							require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", saved.NewJobRunID).Update("status", model.JobRunStatusRunning).Error)
						case "nonce-schedule-claimed":
							require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("state", CopilotRerunScheduleClaimed).Error)
						}
						plan, err := preflightCopilotRerunSchema(ctx, scoped)
						require.False(t, db.Migrator().HasColumn(&model.CopilotRerunAdmissionGate{}, "admission_nonce_digest"))
						require.False(t, db.Migrator().HasIndex(&model.StepRun{}, rerunStepJobIndex))
						if scenario != "nonce-upgrade" {
							require.ErrorIs(t, err, errRerunSchemaShape)
							require.ErrorIs(t, MigrateSchema(ctx, scoped), errRerunSchemaShape)
							require.False(t, db.Migrator().HasIndex(&model.StepRun{}, rerunStepJobIndex))
							return
						}
						require.NoError(t, err)
						require.True(t, plan.addAdmissionNonce)
						require.NoError(t, MigrateSchema(ctx, scoped))
						require.NoError(t, MigrateSchema(ctx, scoped))
						columns, err := rerunMySQLColumns(db, "copilot_rerun_admission_gate")
						require.NoError(t, err)
						require.Equal(t, "varchar(67)", columns["admission_nonce_digest"].ColumnType)
						require.Equal(t, collation, columns["admission_nonce_digest"].CollationName.String)
						var gate model.CopilotRerunAdmissionGate
						require.NoError(t, db.Where("gate_id = ?", saved.GateID).First(&gate).Error)
						require.Empty(t, gate.AdmissionNonceDigest)
						require.Equal(t, "unclaimed", gate.State)
						return
					}

					if scenario == "audit-preflight-order" {
						require.NoError(t, scoped.AutoMigrate(&auditActionLegacyLog{}))
						require.NoError(t, db.Create(&auditActionLegacyLog{LogID: "retained", Action: "secret:retained", Result: model.AuditResultDenied}).Error)
						plan, err := preflightAuditActionSchema(ctx, db)
						require.NoError(t, err)
						require.NotNil(t, plan, "the legacy audit shape must actually require ALTER")
						require.NoError(t, scoped.AutoMigrate(&model.CopilotDefinitionSnapshot{}))
						assertAuditActionMySQLRejected(t, db, "copilot rerun partial or unknown schema")
						return
					}
					if scenario == "late-preflight" {
						require.NoError(t, scoped.AutoMigrate(&model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}))
						require.NoError(t, scoped.AutoMigrate(copilotRerunSchemaModels()...))
						require.NoError(t, db.Migrator().DropIndex(&model.StepRun{}, rerunStepJobIndex))
						require.NoError(t, db.Migrator().DropIndex(&model.CopilotRerunSchedule{}, "uk_copilot_rerun_schedule"))
						require.ErrorIs(t, MigrateSchema(ctx, scoped), errRerunSchemaShape)
						require.False(t, db.Migrator().HasIndex(&model.StepRun{}, rerunStepJobIndex))
						require.False(t, db.Migrator().HasTable(&model.User{}))
						return
					}
					longID := strings.Repeat("旧身份", 200) + "-tail"
					if scenario != "fresh" {
						rerunSchemaLegacy(t, scoped)
						run := &model.PipelineRun{RunID: "historical-run"}
						job := &model.JobRun{JobRunID: "historical-job", PipelineRunID: run.RunID}
						step := &model.StepRun{StepRunID: "historical-step", PipelineRunID: run.RunID, JobRunID: longID}
						for _, row := range []any{run, job, step} {
							retryApprovalMySQLJSONDefaults(row)
							require.NoError(t, retryApprovalMySQLValidateJSON(db, row))
						}
						require.NoError(t, db.Omit("CopilotRerunIntentID", "CopilotRerunGateID").Create(run).Error)
						require.NoError(t, db.Omit("CopilotRerunIntentID", "CopilotRerunGateID", "ExpectedSourceCommit").Create(job).Error)
						require.NoError(t, db.Create(step).Error)
					}
					if scenario == "index-only" {
						require.NoError(t, db.Exec("ALTER TABLE step_run ADD INDEX idx_step_run_job_run (job_run_id(191))").Error)
					}
					if scenario == "complete-markers" {
						for _, field := range []string{"CopilotRerunIntentID", "CopilotRerunGateID"} {
							require.NoError(t, db.Migrator().AddColumn(&model.PipelineRun{}, field))
						}
						require.ErrorIs(t, MigrateSchema(ctx, scoped), errRerunSchemaShape)
						require.False(t, db.Migrator().HasIndex(&model.StepRun{}, rerunStepJobIndex))
						require.False(t, db.Migrator().HasTable(&model.CopilotRerunIntent{}))
						return
					}
					if scenario == "view-ledger" {
						require.NoError(t, db.Exec("CREATE VIEW copilot_rerun_intent AS SELECT 1 AS id").Error)
						defer func() { require.NoError(t, db.Exec("DROP VIEW copilot_rerun_intent").Error) }()
						require.ErrorIs(t, MigrateSchema(ctx, scoped), errRerunSchemaShape)
						require.False(t, db.Migrator().HasIndex(&model.StepRun{}, rerunStepJobIndex))
						require.False(t, db.Migrator().HasTable(&model.CopilotDefinitionSnapshot{}))
						return
					}
					if scenario == "partial-marker" {
						require.NoError(t, db.Migrator().AddColumn(&model.PipelineRun{}, "CopilotRerunIntentID"))
						require.ErrorIs(t, MigrateSchema(ctx, scoped), errRerunSchemaShape)
						require.False(t, db.Migrator().HasIndex(&model.StepRun{}, rerunStepJobIndex))
						require.False(t, db.Migrator().HasTable(&model.CopilotRerunIntent{}))
						return
					}
					if scenario == "wrong-prefix" {
						require.NoError(t, db.Exec("ALTER TABLE step_run ADD INDEX idx_step_run_job_run (job_run_id(190))").Error)
						require.ErrorIs(t, MigrateSchema(ctx, scoped), errRerunSchemaShape)
						require.False(t, db.Migrator().HasTable(&model.CopilotRerunIntent{}))
						return
					}
					require.NoError(t, MigrateSchema(ctx, scoped))
					require.NoError(t, MigrateSchema(ctx, scoped))
					columns, err := rerunMySQLColumns(db, "step_run")
					require.NoError(t, err)
					require.Equal(t, "longtext", columns["job_run_id"].ColumnType)
					require.Equal(t, collation, columns["job_run_id"].CollationName.String)
					indexes, err := rerunMySQLIndexes(db, "step_run")
					require.NoError(t, err)
					add, err := validateRerunStepJobShape(columns["job_run_id"], indexes)
					require.NoError(t, err)
					require.False(t, add)
					for _, entity := range copilotRerunSchemaModels() {
						var n int64
						require.NoError(t, db.Model(entity).Count(&n).Error)
						require.Zero(t, n)
						require.NoError(t, preflightRerunLedger(db, entity))
					}
					if scenario == "upgrade" || scenario == "index-only" {
						var oldRun model.PipelineRun
						var oldJob model.JobRun
						var oldStep model.StepRun
						require.NoError(t, db.Where("run_id = ?", "historical-run").First(&oldRun).Error)
						require.NoError(t, db.Where("job_run_id = ?", "historical-job").First(&oldJob).Error)
						require.NoError(t, db.Where("step_run_id = ?", "historical-step").First(&oldStep).Error)
						require.True(t, emptyRunRerun(&oldRun))
						require.True(t, emptyJobRerun(&oldJob))
						require.Equal(t, longID, oldStep.JobRunID)
					} else {
						r, saved, in := rerunConfirmMySQLSeed(t, db)
						receipt, err := r.CopilotRerunConfirm(ctx, in, permitRerun, testRerunReview)
						require.NoError(t, err)
						var before model.CopilotDefinitionSnapshot
						require.NoError(t, db.Where("snapshot_id = ?", saved.SnapshotID).First(&before).Error)
						require.NoError(t, MigrateSchema(ctx, scoped))
						var after model.CopilotDefinitionSnapshot
						require.NoError(t, db.Where("snapshot_id = ?", saved.SnapshotID).First(&after).Error)
						require.Equal(t, before, after)
						observed, err := r.CopilotRerunInspect(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
						require.NoError(t, err)
						require.Equal(t, receipt, observed)
					}
					t.Log("production migration preserved historical data, exact LONGTEXT collation/prefix, and inert authority")
				})
			}
		})
	}
}
