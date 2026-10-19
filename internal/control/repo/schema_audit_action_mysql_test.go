// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A separate frozen model builds the shipped schema, independently of the new
// AuditLog tags. Never manufacture an upgrade by first migrating the new model.
type auditActionLegacyLog struct {
	model.BaseModel
	LogID        string         `gorm:"column:log_id;not null;uniqueIndex;type:varchar(64)"`
	ActorUserID  string         `gorm:"column:actor_user_id;not null;default:'';index;type:varchar(64)"`
	ActorIP      string         `gorm:"column:actor_ip;not null;default:''"`
	Action       string         `gorm:"column:action;not null;index:idx_audit_log_action;type:varchar(32)"`
	ResourceType string         `gorm:"column:resource_type;not null;default:'';index:idx_resource,priority:1;type:varchar(32)"`
	ResourceID   string         `gorm:"column:resource_id;not null;default:'';index:idx_resource,priority:2;type:varchar(64)"`
	ScopeType    string         `gorm:"column:scope_type;not null;default:''"`
	ScopeID      string         `gorm:"column:scope_id;not null;default:''"`
	Result       string         `gorm:"column:result;not null"`
	Reason       string         `gorm:"column:reason;not null;default:''"`
	Payload      datatypes.JSON `gorm:"column:payload;type:json"`
}

func (auditActionLegacyLog) TableName() string { return "audit_log" }

type auditActionDDLRecorder struct {
	logger.Interface
	statements []string
}

func (r *auditActionDDLRecorder) Trace(ctx context.Context, begin time.Time, query func() (string, int64), err error) {
	statement, rows := query()
	upper := strings.ToUpper(strings.TrimSpace(statement))
	for _, prefix := range []string{"CREATE ", "ALTER ", "DROP ", "RENAME ", "TRUNCATE "} {
		if strings.HasPrefix(upper, prefix) {
			r.statements = append(r.statements, statement)
			break
		}
	}
	r.Interface.Trace(ctx, begin, func() (string, int64) { return statement, rows }, err)
}

func auditActionMySQLFixture(t *testing.T) *gorm.DB {
	t.Helper()
	required := os.Getenv("ARCENTRA_REQUIRE_INBOX_MYSQL")
	require.True(t, required == "" || required == "1", "ARCENTRA_REQUIRE_INBOX_MYSQL must be 1 or unset")
	dsn := os.Getenv("ARCENTRA_INBOX_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required audit-action MySQL fixture missing")
		}
		t.Skip("explicit disposable Inbox MySQL fixture absent; no audit-action MySQL acceptance claim")
	}
	cfg, err := manualInboxMySQLConfig(dsn)
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("explicit audit-action MySQL connection unavailable")
	}
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.Empty(t, tables, "audit migration acceptance requires a fresh empty disposable schema")
	return db
}

func auditActionMySQLClean(t *testing.T, db *gorm.DB) {
	t.Helper()
	// The fixture required an empty database. Only models created by this test's
	// explicit migration are removed; no database, grants or existing data.
	t.Cleanup(func() {
		models := schemaModels()
		for i := len(models) - 1; i >= 0; i-- {
			require.NoError(t, db.Migrator().DropTable(models[i]))
		}
	})
}

func auditActionMySQLDefinition(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var name, definition string
	require.NoError(t, db.Raw("SHOW CREATE TABLE audit_log").Row().Scan(&name, &definition))
	require.Equal(t, "audit_log", name)
	return definition
}

func assertAuditActionMySQLMetadata(t *testing.T, db *gorm.DB, collation string) {
	t.Helper()
	var columns []struct {
		DataType, ColumnType, IsNullable, CharacterSetName, CollationName string
		ColumnDefault                                                     sql.NullString
	}
	require.NoError(t, db.Raw(`SELECT DATA_TYPE AS data_type, COLUMN_TYPE AS column_type, IS_NULLABLE AS is_nullable, CHARACTER_SET_NAME AS character_set_name, COLLATION_NAME AS collation_name, COLUMN_DEFAULT AS column_default FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'audit_log' AND COLUMN_NAME = 'action'`).Scan(&columns).Error)
	require.Len(t, columns, 1)
	require.Equal(t, "longtext", columns[0].DataType)
	require.Equal(t, "longtext", columns[0].ColumnType)
	require.Equal(t, "NO", columns[0].IsNullable)
	require.Equal(t, "utf8mb4", columns[0].CharacterSetName)
	require.Equal(t, collation, columns[0].CollationName)
	require.False(t, columns[0].ColumnDefault.Valid)
	var indexes []struct {
		IndexName, ColumnName, IndexType, IsVisible string
		NonUnique, SeqInIndex                       int
		SubPart                                     sql.NullInt64
		Expression                                  sql.NullString
	}
	require.NoError(t, db.Raw(`SELECT INDEX_NAME AS index_name, COLUMN_NAME AS column_name, INDEX_TYPE AS index_type, IS_VISIBLE AS is_visible, NON_UNIQUE AS non_unique, SEQ_IN_INDEX AS seq_in_index, SUB_PART AS sub_part, EXPRESSION AS expression FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'audit_log' AND INDEX_NAME = 'idx_audit_log_action' ORDER BY SEQ_IN_INDEX`).Scan(&indexes).Error)
	require.Len(t, indexes, 1)
	require.Equal(t, "idx_audit_log_action", indexes[0].IndexName)
	require.Equal(t, "action", indexes[0].ColumnName)
	require.Equal(t, "BTREE", indexes[0].IndexType)
	require.Equal(t, "YES", indexes[0].IsVisible)
	require.Equal(t, 1, indexes[0].NonUnique)
	require.Equal(t, 1, indexes[0].SeqInIndex)
	require.Equal(t, sql.NullInt64{Int64: 191, Valid: true}, indexes[0].SubPart)
	require.False(t, indexes[0].Expression.Valid)
}

func assertAuditActionMySQLPersistence(t *testing.T, ctx context.Context, db *gorm.DB) {
	t.Helper()
	r := NewAuditLogRepo(artifactTestDatabase{db})
	prefix := strings.Repeat("p", 191)
	actions := []string{
		strings.Repeat("a", 32), strings.Repeat("b", 33),
		strings.Repeat("c", 191), strings.Repeat("d", 192),
		strings.Repeat("監査🔒", 64), strings.Repeat("large", 14000) + "終",
		prefix + "-first-tail", prefix + "-second-tail",
	}
	for i, n := range []int{32, 33, 191, 192, 192, 70001, 202, 203} {
		require.Equal(t, n, utf8.RuneCountInString(actions[i]))
	}
	for i, action := range actions {
		entry := &model.AuditLog{LogID: fmt.Sprintf("lossless-%d", i), ActorUserID: "lossless-actor", Action: action, Result: model.AuditResultDenied, Reason: "retain exact content", Payload: datatypes.JSON(`{"original":true}`)}
		require.NoError(t, r.Create(ctx, entry))
	}
	// Filtering must compare the complete value, not the 191-character key.
	for i, action := range actions {
		entries, total, err := r.List(ctx, action, "lossless-actor", 1, 200)
		require.NoError(t, err)
		require.EqualValues(t, 1, total)
		require.Len(t, entries, 1)
		require.Equal(t, fmt.Sprintf("lossless-%d", i), entries[0].LogID)
		require.Equal(t, action, entries[0].Action)
		require.Equal(t, "retain exact content", entries[0].Reason)
		require.JSONEq(t, `{"original":true}`, string(entries[0].Payload))
	}
	for _, absent := range []string{prefix, prefix + "-missing-tail"} {
		entries, total, err := r.List(ctx, absent, "lossless-actor", 1, 200)
		require.NoError(t, err)
		require.Zero(t, total)
		require.Empty(t, entries)
	}
	entries, total, err := r.List(ctx, "", "lossless-actor", 1, 200)
	require.NoError(t, err)
	require.EqualValues(t, len(actions), total)
	stored := make([]string, 0, len(entries))
	for _, entry := range entries {
		stored = append(stored, entry.Action)
	}
	require.ElementsMatch(t, actions, stored)
}

func TestAuditActionMySQLLosslessSchema(t *testing.T) {
	db := auditActionMySQLFixture(t)
	for _, collation := range []string{"utf8mb4_0900_ai_ci", "utf8mb4_bin"} {
		t.Run(collation, func(t *testing.T) {
			for _, shape := range []string{"fresh", "upgrade"} {
				t.Run(shape, func(t *testing.T) {
					auditActionMySQLClean(t, db)
					ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
					defer cancel()
					q := func() *gorm.DB {
						return db.Session(&gorm.Session{NewDB: true}).Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation)
					}
					legacy := auditActionLegacyLog{LogID: "legacy", ActorUserID: "original-actor", Action: "secret:legacy", Result: model.AuditResultDenied, Reason: "unchanged", Payload: datatypes.JSON(`{"original":"監査🔒"}`)}
					if shape == "upgrade" {
						require.NoError(t, q().AutoMigrate(&auditActionLegacyLog{}))
						require.NoError(t, q().Create(&legacy).Error)
						require.NoError(t, q().Where("log_id = ?", legacy.LogID).First(&legacy).Error)
						// A column may differ from the table default. Its own collation
						// must survive, rather than being replaced by that default.
						other := "utf8mb4_bin"
						if collation == other {
							other = "utf8mb4_0900_ai_ci"
						}
						require.NoError(t, q().Exec("ALTER TABLE audit_log DEFAULT CHARACTER SET utf8mb4 COLLATE "+other).Error)
					}
					recorder := &auditActionDDLRecorder{Interface: logger.Default.LogMode(logger.Silent)}
					require.NoError(t, MigrateSchema(ctx, q().Session(&gorm.Session{Logger: recorder})))
					assertAuditActionMySQLMetadata(t, db, collation)
					if shape == "upgrade" {
						var original auditActionLegacyLog
						require.NoError(t, db.Where("log_id = ?", "legacy").First(&original).Error)
						require.Equal(t, legacy, original, "migration must retain all original row content")
						var auditDDL []string
						for _, statement := range recorder.statements {
							if strings.Contains(statement, "audit_log") {
								auditDDL = append(auditDDL, statement)
							}
						}
						require.Len(t, auditDDL, 1, "column and index must change in one atomic ALTER")
						require.Contains(t, strings.ToUpper(auditDDL[0]), "LONGTEXT")
						require.Contains(t, strings.ToUpper(auditDDL[0]), "DROP INDEX")
						require.Contains(t, strings.ToUpper(auditDDL[0]), "ADD INDEX")
					}
					assertAuditActionMySQLPersistence(t, ctx, db)
					before := auditActionMySQLDefinition(t, db)
					recorder.statements = nil
					require.NoError(t, MigrateSchema(ctx, q().Session(&gorm.Session{Logger: recorder})))
					require.Equal(t, before, auditActionMySQLDefinition(t, db))
					for _, statement := range recorder.statements {
						require.NotContains(t, statement, "audit_log", "replay must not alter the completed audit schema")
					}
					assertAuditActionMySQLMetadata(t, db, collation)
					entries, total, err := NewAuditLogRepo(artifactTestDatabase{db}).List(ctx, strings.Repeat("large", 14000)+"終", "lossless-actor", 1, 200)
					require.NoError(t, err)
					require.EqualValues(t, 1, total)
					require.Len(t, entries, 1)
					require.Equal(t, strings.Repeat("large", 14000)+"終", entries[0].Action)
				})
			}
			t.Run("unknown_shapes_rejected_before_ddl", func(t *testing.T) {
				for _, tc := range []struct{ name, alteration string }{
					{"varchar_33", "MODIFY COLUMN action VARCHAR(33) NOT NULL"},
					{"nullable", "MODIFY COLUMN action VARCHAR(32) NULL"},
					{"default", "MODIFY COLUMN action VARCHAR(32) NOT NULL DEFAULT ''"},
					{"missing_index", "DROP INDEX idx_audit_log_action"},
					{"wrong_column", "DROP INDEX idx_audit_log_action, ADD INDEX idx_audit_log_action (result(32))"},
					{"composite_index", "DROP INDEX idx_audit_log_action, ADD INDEX idx_audit_log_action (action, actor_user_id)"},
					{"unique_index", "DROP INDEX idx_audit_log_action, ADD UNIQUE INDEX idx_audit_log_action (action)"},
					{"legacy_prefix", "DROP INDEX idx_audit_log_action, ADD INDEX idx_audit_log_action (action(16))"},
					{"wrong_longtext_prefix", "MODIFY COLUMN action LONGTEXT NOT NULL, DROP INDEX idx_audit_log_action, ADD INDEX idx_audit_log_action (action(190))"},
					{"extra_action_index", "ADD INDEX idx_unknown_action (action)"},
					{"functional_index", "ADD INDEX idx_unknown_action_expression ((LEFT(action, 16)))"},
					{"invisible_index", "ALTER INDEX idx_audit_log_action INVISIBLE"},
					{"descending_index", "DROP INDEX idx_audit_log_action, ADD INDEX idx_audit_log_action (action DESC)"},
				} {
					t.Run(tc.name, func(t *testing.T) {
						auditActionMySQLClean(t, db)
						q := func() *gorm.DB {
							return db.Session(&gorm.Session{NewDB: true}).Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation)
						}
						require.NoError(t, q().AutoMigrate(&auditActionLegacyLog{}))
						require.NoError(t, db.Create(&auditActionLegacyLog{LogID: "retained", Action: "secret:retained", Result: model.AuditResultDenied}).Error)
						require.NoError(t, db.Exec("ALTER TABLE audit_log "+tc.alteration).Error)
						assertAuditActionMySQLRejected(t, db, "audit action")
					})
				}
			})
			t.Run("unrelated_preflight_rejected_before_audit_ddl", func(t *testing.T) {
				auditActionMySQLClean(t, db)
				q := func() *gorm.DB {
					return db.Session(&gorm.Session{NewDB: true}).Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation)
				}
				require.NoError(t, q().AutoMigrate(&auditActionLegacyLog{}))
				require.NoError(t, db.Create(&auditActionLegacyLog{LogID: "retained", Action: "secret:retained", Result: model.AuditResultDenied}).Error)
				// This intentionally partial, unrelated table must block the
				// already-known audit upgrade before any DDL on any table.
				require.NoError(t, db.Exec("CREATE TABLE copilot_execution_intent (id BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB").Error)
				assertAuditActionMySQLRejected(t, db, "copilot execution partial schema")
			})
		})
	}
}

func assertAuditActionMySQLRejected(t *testing.T, db *gorm.DB, message string) {
	t.Helper()
	before := auditActionMySQLDefinition(t, db)
	tablesBefore, err := db.Migrator().GetTables()
	require.NoError(t, err)
	recorder := &auditActionDDLRecorder{Interface: logger.Default.LogMode(logger.Silent)}
	err = MigrateSchema(context.Background(), db.Session(&gorm.Session{Logger: recorder}))
	require.ErrorContains(t, err, message)
	require.Empty(t, recorder.statements, "all preflights must complete before any DDL")
	require.Equal(t, before, auditActionMySQLDefinition(t, db))
	tablesAfter, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.ElementsMatch(t, tablesBefore, tablesAfter)
	var retained []auditActionLegacyLog
	require.NoError(t, db.Find(&retained).Error)
	require.Len(t, retained, 1)
	require.Equal(t, "retained", retained[0].LogID)
	require.Equal(t, "secret:retained", retained[0].Action)
}
