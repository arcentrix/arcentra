// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A new installation may add the entire detached ledger. A partial application
// cannot silently repair authority or deduplication, especially under MySQL's
// nontransactional DDL. Run this before any production migration writes.
func preflightCopilotExecutionSchema(ctx context.Context, db *gorm.DB) error {
	db = db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	entity := &model.CopilotExecutionIntent{}
	if !db.Migrator().HasTable(entity) {
		return nil
	}
	partial := errors.New("copilot execution partial schema requires operator review")
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(entity); err != nil {
		return errors.New("copilot execution schema preflight unavailable")
	}
	for _, field := range stmt.Schema.Fields {
		if field.DBName != "" && !db.Migrator().HasColumn(entity, field.DBName) {
			return partial
		}
	}
	indexes, err := db.Migrator().GetIndexes(entity)
	if err != nil {
		return errors.New("copilot execution index preflight unavailable")
	}
	for _, expected := range stmt.Schema.ParseIndexes() {
		found := false
		for _, actual := range indexes {
			if actual.Name() != expected.Name {
				continue
			}
			columns := actual.Columns()
			if len(columns) != len(expected.Fields) {
				return partial
			}
			for i, field := range expected.Fields {
				if columns[i] != field.DBName {
					return partial
				}
			}
			if expected.Class == "UNIQUE" {
				if unique, known := actual.Unique(); !known || !unique {
					return partial
				}
			}
			found = true
		}
		if !found {
			return partial
		}
	}
	// Use database comparison semantics, including case-insensitive collations.
	for _, keys := range []string{"intent_id", "decision_id", "owner_user_id, project_id, request_id"} {
		var count int64
		if err := db.Raw("SELECT COUNT(*) FROM (SELECT " + keys + " FROM copilot_execution_intent GROUP BY " + keys + " HAVING COUNT(*) > 1) AS ambiguous").Scan(&count).Error; err != nil {
			return errors.New("copilot execution identity preflight unavailable")
		}
		if count != 0 {
			return errors.New("copilot execution identity preflight failed")
		}
	}
	// Required identity columns must never be reconstructed from history.
	for _, field := range stmt.Schema.Fields {
		if strings.HasSuffix(field.DBName, "_id") && field.DBName != "id" {
			var count int64
			if err := db.Model(entity).Where(field.DBName + " IS NULL OR " + field.DBName + " = '' OR TRIM(" + field.DBName + ") <> " + field.DBName).Count(&count).Error; err != nil {
				return errors.New("copilot execution identity preflight unavailable")
			}
			if count != 0 {
				return errors.New("copilot execution identity preflight failed")
			}
		}
	}
	return nil
}
