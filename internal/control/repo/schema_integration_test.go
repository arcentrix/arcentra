// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"testing"
)

// SQLite checks the combined preflight ordering without claiming MySQL ALTER
// coverage; the audit-preflight-order scenario in the MySQL gate covers that.
func TestMigrationPreflightsPreserveLegacyAuditOnPartialRerun(t *testing.T) {
	db := executionSchemaDB(t)
	require.NoError(t, db.AutoMigrate(&auditActionLegacyLog{}))
	original := auditActionLegacyLog{LogID: "retained", Action: "secret:retained", Result: model.AuditResultDenied}
	require.NoError(t, db.Create(&original).Error)
	require.NoError(t, db.First(&original).Error)
	require.NoError(t, db.AutoMigrate(&model.CopilotDefinitionSnapshot{}))
	var before, after []struct{ Type, Name, SQL string }
	const definitions = "SELECT type,name,sql FROM sqlite_master WHERE tbl_name = 'audit_log' ORDER BY type,name"
	require.NoError(t, db.Raw(definitions).Scan(&before).Error)
	recorder := &auditActionDDLRecorder{Interface: logger.Default.LogMode(logger.Silent)}
	require.ErrorIs(t, MigrateSchema(context.Background(), db.Session(&gorm.Session{Logger: recorder})), errRerunSchemaShape)
	require.Empty(t, recorder.statements)
	require.NoError(t, db.Raw(definitions).Scan(&after).Error)
	require.Equal(t, before, after)
	var retained auditActionLegacyLog
	require.NoError(t, db.First(&retained).Error)
	require.Equal(t, original, retained)
	require.False(t, db.Migrator().HasTable(&model.User{}))
}

// Shared current source fixtures must shed newer authority columns when used
// specifically to model a historical database without any rerun ledgers.
func removeLaterRerunColumnsForMigration(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, entry := range []struct {
		row    any
		fields []string
	}{
		{&model.PipelineRun{}, []string{"CopilotRerunIntentID", "CopilotRerunGateID"}},
		{&model.JobRun{}, []string{"CopilotRerunIntentID", "CopilotRerunGateID", "ExpectedSourceCommit"}},
	} {
		for _, field := range entry.fields {
			require.NoError(t, db.Migrator().DropColumn(entry.row, field))
		}
	}
}
