// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func executionSchemaDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migration.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	return db
}

func TestCopilotExecutionSchemaUpgradeDoesNotInventAuthority(t *testing.T) {
	db := executionSchemaDB(t)
	// Simulate migration 013: the complete preceding schema, no execution table.
	for _, entity := range schemaModels() {
		if _, execution := entity.(*model.CopilotExecutionIntent); execution || isRerunSchemaModel(entity) {
			continue
		}
		require.NoError(t, db.AutoMigrate(entity))
	}
	removeLaterRerunColumnsForMigration(t, db)
	require.NoError(t, inboxSchemaIndexes(context.Background(), db))
	require.NoError(t, db.Create(&model.User{UserID: "legacy-owner", Username: "legacy-owner", IsEnabled: 1}).Error)
	require.NoError(t, db.Create(&model.CopilotActionPlan{PlanID: "legacy-plan", RequestID: "legacy-request", OwnerUserID: "legacy-owner", ProjectID: "legacy-project", Kind: "retry_approval_review", State: "reviewed", PlanDigest: "legacy-digest", Version: 2, SchemaVersion: 1, CipherVersion: 1, PayloadCiphertext: []byte("opaque-legacy-ciphertext"), PayloadPresent: true, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}).Error)
	for n := 0; n < 2; n++ {
		require.NoError(t, MigrateSchema(context.Background(), db))
	}
	for _, entity := range []any{&model.CopilotExecutionIntent{}, &model.ApprovalDecision{}, &model.RoleGrant{}, &model.JobRun{}} {
		var count int64
		require.NoError(t, db.Model(entity).Count(&count).Error)
		require.Zero(t, count)
	}
	var plan model.CopilotActionPlan
	require.NoError(t, db.First(&plan).Error)
	require.Equal(t, "reviewed", plan.State)
	require.Equal(t, []byte("opaque-legacy-ciphertext"), plan.PayloadCiphertext)
	for _, name := range []string{"uk_copilot_execution_intent", "uk_copilot_execution_request", "uk_copilot_execution_decision", "idx_copilot_execution_owner", "idx_copilot_execution_expiry"} {
		require.True(t, db.Migrator().HasIndex(&model.CopilotExecutionIntent{}, name), name)
	}
}

func TestCopilotExecutionSchemaPartialFailsBeforeDDL(t *testing.T) {
	for _, shape := range []string{"column", "missing-index", "nonunique-index", "wrong-columns"} {
		t.Run(shape, func(t *testing.T) {
			db := executionSchemaDB(t)
			if shape == "column" {
				require.NoError(t, db.Exec("CREATE TABLE copilot_execution_intent (id INTEGER PRIMARY KEY, intent_id TEXT)").Error)
			} else {
				require.NoError(t, db.AutoMigrate(&model.CopilotExecutionIntent{}))
				require.NoError(t, db.Migrator().DropIndex(&model.CopilotExecutionIntent{}, "uk_copilot_execution_intent"))
				if shape == "nonunique-index" {
					require.NoError(t, db.Exec("CREATE INDEX uk_copilot_execution_intent ON copilot_execution_intent(intent_id)").Error)
				}
				if shape == "wrong-columns" {
					require.NoError(t, db.Exec("CREATE UNIQUE INDEX uk_copilot_execution_intent ON copilot_execution_intent(request_id)").Error)
				}
			}
			require.ErrorContains(t, MigrateSchema(context.Background(), db), "copilot execution partial schema")
			require.False(t, db.Migrator().HasTable(&model.User{}), "preflight must precede all DDL")
		})
	}
}

func TestCopilotExecutionSchemaHasOnlyOpaqueBindingsAndNoAuthorityDefaults(t *testing.T) {
	db := executionSchemaDB(t)
	statement := &gorm.Statement{DB: db}
	require.NoError(t, statement.Parse(&model.CopilotExecutionIntent{}))
	expected := map[string]bool{}
	for _, name := range []string{"id", "intent_id", "owner_user_id", "project_id", "request_id", "pipeline_id", "run_id", "job_run_id", "approval_id", "conversation_id", "plan_id", "plan_version", "plan_digest", "evidence_hash", "evidence_digest", "decision_id", "intent_digest", "state", "version", "created_at", "updated_at", "expires_at", "submitted_at", "cancelled_at"} {
		expected[name] = true
	}
	for _, field := range statement.Schema.Fields {
		require.True(t, expected[field.DBName], "unexpected ledger column %s: review for sensitive data", field.DBName)
		if field.DBName != "id" {
			require.False(t, field.HasDefaultValue, "must not infer authority defaults for %s", field.DBName)
		}
	}
	require.Empty(t, statement.Schema.Relationships.Relations, "detached intent must not cascade from history")
}

func TestCopilotExecutionSchemaUniqueBindingsEnforced(t *testing.T) {
	db := executionSchemaDB(t)
	require.NoError(t, MigrateSchema(context.Background(), db))
	now := time.Now().UTC()
	base := model.CopilotExecutionIntent{IntentID: "intent-one", OwnerUserID: "owner", ProjectID: "project", RequestID: "request", PipelineID: "pipeline", RunID: "run", JobRunID: "job", ApprovalID: "approval", ConversationID: "conversation", PlanID: "plan", PlanVersion: 2, PlanDigest: "opaque-plan-digest", EvidenceHash: "opaque-evidence-hash", EvidenceDigest: "opaque-evidence-digest", DecisionID: "decision-one", IntentDigest: "opaque-intent-digest", State: "prepared", Version: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}
	require.NoError(t, db.Create(&base).Error)
	for _, conflict := range []string{"intent", "decision", "request"} {
		copy := base
		copy.ID = 0
		switch conflict {
		case "intent":
			copy.RequestID = "other-request"
			copy.DecisionID = "other-decision"
		case "decision":
			copy.IntentID = "other-intent"
			copy.RequestID = "other-request"
		case "request":
			copy.IntentID = "other-intent"
			copy.DecisionID = "other-decision"
		}
		require.Error(t, db.Create(&copy).Error, conflict)
	}
	require.NoError(t, MigrateSchema(context.Background(), db))
	var count int64
	require.NoError(t, db.Model(&model.CopilotExecutionIntent{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	// Corrupt identity rows are not repaired or converted into authority.
	require.NoError(t, db.Model(&model.CopilotExecutionIntent{}).Where("id = ?", base.ID).Update("owner_user_id", " padded-owner ").Error)
	require.ErrorContains(t, MigrateSchema(context.Background(), db), "copilot execution identity preflight failed")
}
