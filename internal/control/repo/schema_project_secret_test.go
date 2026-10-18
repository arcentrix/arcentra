// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type legacySecretSchema struct {
	ID          uint64 `gorm:"primaryKey"`
	SecretID    string
	Name        string
	Scope       string
	ScopeID     string
	SecretValue string
}

func (legacySecretSchema) TableName() string { return "secret" }
func TestProjectSecretsSchemaPreflightFailsBeforeAnyWrite(t *testing.T) {
	for _, test := range []struct {
		name string
		rows []legacySecretSchema
	}{
		{"duplicate-id", []legacySecretSchema{{SecretID: "one", Name: "A", Scope: "project", ScopeID: "p"}, {SecretID: "one", Name: "B", Scope: "project", ScopeID: "other"}}},
		{"case-id", []legacySecretSchema{{SecretID: "one", Name: "A", Scope: "project", ScopeID: "p"}, {SecretID: "ONE", Name: "B", Scope: "project", ScopeID: "p"}}},
		{"case-name", []legacySecretSchema{{SecretID: "one", Name: "TOKEN", Scope: "project", ScopeID: "p"}, {SecretID: "two", Name: "token", Scope: "project", ScopeID: "p"}}},
		{"empty-id", []legacySecretSchema{{Name: "A", Scope: "global"}}},
		{"padded-id", []legacySecretSchema{{SecretID: "padded ", Name: "A", Scope: "global"}}},
		{"tab-padded-id", []legacySecretSchema{{SecretID: "padded\t", Name: "A", Scope: "global"}}},
		{"invalid-project-name", []legacySecretSchema{{SecretID: "one", Name: "deploy-token", Scope: "project", ScopeID: "p"}}},
		{"digit-project-name", []legacySecretSchema{{SecretID: "one", Name: "1TOKEN", Scope: "project", ScopeID: "p"}}},
		{"scope-alias", []legacySecretSchema{{SecretID: "one", Name: "A", Scope: "PROJECT", ScopeID: "p"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sql, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sql.Close() })
			require.NoError(t, db.AutoMigrate(&legacySecretSchema{}))
			for _, row := range test.rows {
				row.SecretValue = "untouched-synthetic-ciphertext"
				require.NoError(t, db.Create(&row).Error)
			}
			err = MigrateSchema(context.Background(), db)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "untouched-synthetic")
			require.False(t, db.Migrator().HasTable(&model.Organization{}))
			require.False(t, db.Migrator().HasColumn(&model.Secret{}, "version"))
			var unchanged []legacySecretSchema
			require.NoError(t, db.Find(&unchanged).Error)
			require.Len(t, unchanged, len(test.rows))
			for _, row := range unchanged {
				require.Equal(t, "untouched-synthetic-ciphertext", row.SecretValue)
			}
		})
	}
}

type legacySecretRunSchema struct {
	ID         uint64 `gorm:"primaryKey"`
	RunID      string `gorm:"type:varchar(64)"`
	PipelineID string `gorm:"type:varchar(64)"`
	Env        string `gorm:"type:json"`
}

func (legacySecretRunSchema) TableName() string { return "pipeline_run" }

func TestProjectSecretsSchemaUpgradePreservesLegacyCiphertextAndUnknownRunScope(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "upgrade.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sql.Close() })
	require.NoError(t, db.AutoMigrate(&legacySecretSchema{}, &legacySecretRunSchema{}))
	require.NoError(t, db.Create(&legacySecretSchema{SecretID: "legacy-secret", Name: "TOKEN", Scope: "project", ScopeID: "original-project", SecretValue: "untouched-legacy-ciphertext"}).Error)
	require.NoError(t, db.Create(&legacySecretRunSchema{RunID: "legacy-run", PipelineID: "later-reassigned-pipeline", Env: "{}"}).Error)
	require.NoError(t, MigrateSchema(context.Background(), db))
	assertProjectSecretUpgrade(t, db)
	var grants int64
	require.NoError(t, db.Model(&model.RoleGrant{}).Count(&grants).Error)
	require.Zero(t, grants)
}
func assertProjectSecretUpgrade(t *testing.T, db *gorm.DB) {
	t.Helper()
	var secret model.Secret
	require.NoError(t, db.Where("secret_id = ?", "legacy-secret").Take(&secret).Error)
	require.Equal(t, "untouched-legacy-ciphertext", secret.SecretValue)
	require.EqualValues(t, 1, secret.Version)
	require.Equal(t, 1, secret.IsEnabled)
	require.Equal(t, "original-project", secret.ScopeID)
	var run model.PipelineRun
	require.NoError(t, db.Where("run_id = ?", "legacy-run").Take(&run).Error)
	require.Empty(t, run.ProjectID)
	require.True(t, db.Migrator().HasIndex(&model.Secret{}, "uk_secret_business_id"))
	require.True(t, db.Migrator().HasIndex(&model.Secret{}, "uk_secret_scope_name"))
	require.Error(t, db.Create(&model.Secret{SecretID: "legacy-secret", Name: "OTHER", Scope: "global", SecretValue: "synthetic"}).Error)
	require.Error(t, db.Create(&model.Secret{SecretID: "new-secret", Name: "TOKEN", Scope: "project", ScopeID: "original-project", SecretValue: "synthetic"}).Error)
}

func TestProjectSecretsMySQLSchemaUpgradePreservesLegacyCiphertext(t *testing.T) {
	db := openRetryApprovalMySQL(t)
	// Only a confirmed empty, explicitly selected disposable schema may be reshaped.
	for _, m := range []any{&model.Secret{}, &model.PipelineRun{}} {
		if db.Migrator().HasTable(m) {
			var count int64
			require.NoError(t, db.Model(m).Count(&count).Error)
			require.Zero(t, count, "secret migration acceptance refuses existing fixture rows")
		}
	}
	require.NoError(t, db.Migrator().DropTable(&model.Secret{}, &model.PipelineRun{}))
	require.NoError(t, db.AutoMigrate(&legacySecretSchema{}, &legacySecretRunSchema{}))
	t.Cleanup(func() {
		require.NoError(t, db.Where("secret_id = ?", "legacy-secret").Delete(&legacySecretSchema{}).Error)
		require.NoError(t, db.Where("run_id = ?", "legacy-run").Delete(&legacySecretRunSchema{}).Error)
	})
	require.NoError(t, db.Create(&legacySecretSchema{SecretID: "legacy-secret", Name: "TOKEN", Scope: "project", ScopeID: "original-project", SecretValue: "untouched-legacy-ciphertext"}).Error)
	require.NoError(t, db.Create(&legacySecretRunSchema{RunID: "legacy-run", PipelineID: "later-reassigned-pipeline", Env: "{}"}).Error)
	require.NoError(t, preflightProjectSecretSchema(context.Background(), db))
	require.NoError(t, db.AutoMigrate(&model.Secret{}, &model.PipelineRun{}))
	assertProjectSecretUpgrade(t, db)
}
