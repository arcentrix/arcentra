// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func TestAuditActionKnownSchemaShapes(t *testing.T) {
	column := auditActionColumn{ColumnType: "varchar(32)", IsNullable: "NO", CharacterSetName: "utf8mb4", CollationName: "utf8mb4_bin"}
	index := auditActionIndexColumn{IndexName: auditActionIndex, NonUnique: 1, SeqInIndex: 1, ColumnName: sql.NullString{String: "action", Valid: true}, Collation: sql.NullString{String: "A", Valid: true}, IndexType: "BTREE", IsVisible: "YES"}
	migration, err := validateAuditActionShape(column, []auditActionIndexColumn{index})
	if err != nil || migration == nil || migration.charset != "utf8mb4" || migration.collation != "utf8mb4_bin" {
		t.Fatalf("legacy shape: %+v %v", migration, err)
	}
	column.ColumnType = "longtext"
	index.SubPart = sql.NullInt64{Int64: 191, Valid: true}
	migration, err = validateAuditActionShape(column, []auditActionIndexColumn{index})
	if err != nil || migration != nil {
		t.Fatalf("completed shape: %+v %v", migration, err)
	}
	for _, name := range []string{"unknown-type", "nullable", "default", "extra", "comment", "generated", "bad-charset", "bad-collation", "missing-index", "additional-index", "wrong-index-name", "unique", "composite", "wrong-column", "descending", "wrong-method", "invisible", "expression", "wrong-prefix", "missing-prefix"} {
		t.Run(name, func(t *testing.T) {
			c, i := column, index
			indexes := []auditActionIndexColumn{i}
			switch name {
			case "unknown-type":
				c.ColumnType = "varchar(4096)"
			case "nullable":
				c.IsNullable = "YES"
			case "default":
				c.ColumnDefault = sql.NullString{String: "", Valid: true}
			case "extra":
				c.Extra = "DEFAULT_GENERATED"
			case "comment":
				c.ColumnComment = "custom"
			case "generated":
				c.GenerationExpression = "other"
			case "bad-charset":
				c.CharacterSetName = "utf8mb4;"
			case "bad-collation":
				c.CollationName = "utf8mb4_bin NOT NULL"
			case "missing-index":
				indexes = nil
			case "additional-index":
				indexes = append(indexes, i)
			case "wrong-index-name":
				indexes[0].IndexName = "custom"
			case "unique":
				indexes[0].NonUnique = 0
			case "composite":
				indexes[0].SeqInIndex = 2
			case "wrong-column":
				indexes[0].ColumnName.String = "reason"
			case "descending":
				indexes[0].Collation.String = "D"
			case "wrong-method":
				indexes[0].IndexType = "FULLTEXT"
			case "invisible":
				indexes[0].IsVisible = "NO"
			case "expression":
				indexes[0].Expression = sql.NullString{String: "lower(action)", Valid: true}
			case "wrong-prefix":
				indexes[0].SubPart.Int64 = 32
			case "missing-prefix":
				indexes[0].SubPart.Valid = false
			}
			if migration, err := validateAuditActionShape(c, indexes); err == nil || migration != nil {
				t.Fatalf("unknown shape accepted: %+v %v", migration, err)
			}
		})
	}
	column.ColumnType = "varchar(32)"
	if _, err := validateAuditActionShape(column, []auditActionIndexColumn{index}); err == nil {
		t.Fatal("prefixed legacy index accepted")
	}
}

// SQLite proves full-value repository behavior and model migration portability;
// only the required MySQL gate establishes the actual column/index DDL contract.
func TestAuditActionLosslessListSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "audit.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	actions := []string{strings.Repeat("a", 32), strings.Repeat("b", 33), strings.Repeat("c", 191), strings.Repeat("d", 192), strings.Repeat("界", 192), strings.Repeat("same-prefix-", 20) + "first", strings.Repeat("same-prefix-", 20) + "second", strings.Repeat("large-action-", 8192)}
	repository := NewAuditLogRepo(schemaAuditDatabase{db})
	for i, action := range actions {
		if err := repository.Create(context.Background(), &model.AuditLog{LogID: fmt.Sprintf("audit-%d", i), Action: action, Result: model.AuditResultDenied}); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range actions {
		rows, count, err := repository.List(context.Background(), action, "", 1, 20)
		if err != nil || count != 1 || len(rows) != 1 || rows[0].Action != action {
			t.Fatalf("full equality/content mismatch length=%d count=%d error=%v", len(action), count, err)
		}
	}
}

type schemaAuditDatabase struct{ *gorm.DB }

func (d schemaAuditDatabase) Database() *gorm.DB { return d.DB }

func TestAuditActionModelContract(t *testing.T) {
	parsed, err := schema.Parse(&model.AuditLog{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	action := parsed.LookUpField("Action")
	dialect := mysql.Dialector{Config: &mysql.Config{}}
	if action == nil || !action.NotNull || dialect.DataTypeOf(action) != "longtext" {
		t.Fatal("audit action must preserve full LONGTEXT")
	}
	found := false
	for _, index := range parsed.ParseIndexes() {
		if index.Name != auditActionIndex {
			continue
		}
		if len(index.Fields) != 1 || index.Fields[0].DBName != "action" || index.Fields[0].Length != 191 || index.Class != "" {
			t.Fatal("audit index must be nonunique single 191-character prefix")
		}
		found = true
	}
	if !found {
		t.Fatal("fixed audit action index missing")
	}
}
