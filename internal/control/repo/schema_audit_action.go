// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"

	"gorm.io/gorm"
)

const auditActionIndex = "idx_audit_log_action"

type auditActionColumn struct {
	ColumnType           string
	IsNullable           string
	ColumnDefault        sql.NullString
	CharacterSetName     string
	CollationName        string
	Extra                string
	ColumnComment        string
	GenerationExpression string
}
type auditActionIndexColumn struct {
	IndexName  string
	NonUnique  int
	SeqInIndex int
	ColumnName sql.NullString
	SubPart    sql.NullInt64
	Collation  sql.NullString
	IndexType  string
	IsVisible  string
	Expression sql.NullString
}
type auditActionMigration struct{ charset, collation string }

var auditActionSQLIdentifier = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
var errAuditActionShape = errors.New("audit action schema is unknown; operator review required")

// Only the exact shipped old shape and the completed new shape are accepted.
// Reading all action-containing indexes also prevents an unbounded secondary
// index from making the LONGTEXT alteration fail after unrelated DDL has run.
func validateAuditActionShape(column auditActionColumn, indexes []auditActionIndexColumn) (*auditActionMigration, error) {
	if column.IsNullable != "NO" || column.ColumnDefault.Valid || column.Extra != "" || column.ColumnComment != "" || column.GenerationExpression != "" || !auditActionSQLIdentifier.MatchString(column.CharacterSetName) || !auditActionSQLIdentifier.MatchString(column.CollationName) {
		return nil, errAuditActionShape
	}
	if len(indexes) != 1 {
		return nil, errAuditActionShape
	}
	index := indexes[0]
	if index.IndexName != auditActionIndex || index.NonUnique != 1 || index.SeqInIndex != 1 || !index.ColumnName.Valid || index.ColumnName.String != "action" || !index.Collation.Valid || index.Collation.String != "A" || index.IndexType != "BTREE" || index.IsVisible != "YES" || index.Expression.Valid {
		return nil, errAuditActionShape
	}
	switch column.ColumnType {
	case "varchar(32)":
		if index.SubPart.Valid {
			return nil, errAuditActionShape
		}
		return &auditActionMigration{charset: column.CharacterSetName, collation: column.CollationName}, nil
	case "longtext":
		if !index.SubPart.Valid || index.SubPart.Int64 != 191 {
			return nil, errAuditActionShape
		}
		return nil, nil
	default:
		return nil, errAuditActionShape
	}
}

// preflightAuditActionSchema performs no DDL or audit-content reads. The caller
// must finish every other schema preflight before applying the returned change.
func preflightAuditActionSchema(ctx context.Context, db *gorm.DB) (*auditActionMigration, error) {
	if db.Dialector.Name() != "mysql" {
		return nil, nil
	}
	db = db.WithContext(ctx)
	var tables []struct {
		TableType string
		Engine    string
	}
	if err := db.Raw(`SELECT TABLE_TYPE AS table_type, ENGINE AS engine FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'audit_log'`).Scan(&tables).Error; err != nil {
		return nil, errors.New("audit action table preflight unavailable")
	}
	if len(tables) == 0 {
		return nil, nil
	}
	if len(tables) != 1 || tables[0].TableType != "BASE TABLE" || tables[0].Engine != "InnoDB" {
		return nil, errAuditActionShape
	}
	var columns []auditActionColumn
	if err := db.Raw(`SELECT COLUMN_TYPE AS column_type, IS_NULLABLE AS is_nullable, COLUMN_DEFAULT AS column_default, CHARACTER_SET_NAME AS character_set_name, COLLATION_NAME AS collation_name, EXTRA AS extra, COLUMN_COMMENT AS column_comment, GENERATION_EXPRESSION AS generation_expression FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'audit_log' AND COLUMN_NAME = 'action'`).Scan(&columns).Error; err != nil {
		return nil, errors.New("audit action column preflight unavailable")
	}
	if len(columns) != 1 {
		return nil, errAuditActionShape
	}
	var indexes []auditActionIndexColumn
	// Include all members of a matching index: a composite or additional action
	// index is not the known migration baseline, even if its first column matches.
	if err := db.Raw(`SELECT INDEX_NAME AS index_name, NON_UNIQUE AS non_unique, SEQ_IN_INDEX AS seq_in_index, COLUMN_NAME AS column_name, SUB_PART AS sub_part, COLLATION AS collation, INDEX_TYPE AS index_type, IS_VISIBLE AS is_visible, EXPRESSION AS expression FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'audit_log' ORDER BY INDEX_NAME, SEQ_IN_INDEX`).Scan(&indexes).Error; err != nil {
		return nil, errors.New("audit action index preflight unavailable")
	}
	relevant := map[string]bool{auditActionIndex: true}
	for _, index := range indexes {
		// Functional indexes may depend on action indirectly; do not guess their
		// preservation semantics during a lossless known-shape migration.
		if index.Expression.Valid {
			return nil, errAuditActionShape
		}
		if index.ColumnName.Valid && index.ColumnName.String == "action" {
			relevant[index.IndexName] = true
		}
	}
	var actionIndexes []auditActionIndexColumn
	for _, index := range indexes {
		if relevant[index.IndexName] {
			actionIndexes = append(actionIndexes, index)
		}
	}
	return validateAuditActionShape(columns[0], actionIndexes)
}

func (migration *auditActionMigration) apply(ctx context.Context, db *gorm.DB) error {
	if migration == nil {
		return nil
	}
	if !auditActionSQLIdentifier.MatchString(migration.charset) || !auditActionSQLIdentifier.MatchString(migration.collation) {
		return errAuditActionShape
	}
	// One MySQL 8 atomic ALTER replaces the full VARCHAR key and widens its
	// content together. AutoMigrate must never attempt the column change first.
	statement := fmt.Sprintf("ALTER TABLE `audit_log` MODIFY COLUMN `action` LONGTEXT CHARACTER SET %s COLLATE %s NOT NULL, DROP INDEX `idx_audit_log_action`, ADD INDEX `idx_audit_log_action` (`action`(191))", migration.charset, migration.collation)
	if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
		return errors.New("audit action schema migration failed; inspect schema before retry")
	}
	return nil
}
