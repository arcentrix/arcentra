// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// preflightProjectSecretSchema 在任何 DDL 前拒绝有歧义的旧业务键，不读取或重写密文
func preflightProjectSecretSchema(ctx context.Context, db *gorm.DB) error {
	db = db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	if !db.Migrator().HasTable(&model.Secret{}) {
		return nil
	}
	for _, key := range []struct {
		column string
		max    int
		empty  bool
	}{{"secret_id", 64, false}, {"name", 128, false}, {"scope", 16, false}, {"scope_id", 64, true}} {
		var count int64
		query := db.Model(&model.Secret{}).Where(key.column+" IS NULL OR LENGTH("+key.column+") > ? OR LENGTH("+key.column+") <> LENGTH(TRIM("+key.column+"))", key.max)
		if !key.empty {
			query = query.Or("TRIM("+key.column+") = ?", "")
		}
		if err := query.Count(&count).Error; err != nil {
			return errors.New("project secret migration preflight unavailable")
		}
		if count > 0 {
			return errors.New("project secret migration preflight: invalid legacy key; operator review required")
		}
	}
	for _, columns := range [][]string{{"secret_id"}, {"scope", "scope_id", "name"}, {"LOWER(secret_id)"}, {"LOWER(scope)", "LOWER(scope_id)", "LOWER(name)"}} {
		var duplicates []struct{ DuplicateCount int64 }
		if err := db.Model(&model.Secret{}).Select("COUNT(*) AS duplicate_count").Group(strings.Join(columns, ", ")).Having("COUNT(*) > ?", 1).Limit(1).Scan(&duplicates).Error; err != nil {
			return errors.New("project secret migration preflight unavailable")
		}
		if len(duplicates) > 0 {
			return errors.New("project secret migration preflight: ambiguous legacy identity or scoped name; operator review required")
		}
	}
	// Read identity metadata only, in bounded batches. SQL TRIM is not
	// Unicode-aware; Go checks reject tabs/newlines and other padded identities.
	var rows []model.Secret
	namePattern := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	err := db.Model(&model.Secret{}).Select("id", "secret_id", "scope", "scope_id", "name").FindInBatches(&rows, 500, func(_ *gorm.DB, _ int) error {
		for _, row := range rows {
			for _, value := range []string{row.SecretID, row.Scope, row.ScopeID, row.Name} {
				if !utf8.ValidString(value) || strings.TrimSpace(value) != value {
					return errors.New("project secret migration preflight: invalid legacy key; operator review required")
				}
			}
			switch row.Scope {
			case "global", "pipeline", "user", "project", "team":
			default:
				return errors.New("project secret migration preflight: invalid legacy scope; operator review required")
			}
			if row.Scope == "project" && (row.ScopeID == "" || !namePattern.MatchString(row.Name)) {
				return errors.New("project secret migration preflight: invalid legacy project scope or name; operator review required")
			}
		}
		return nil
	}).Error
	if err != nil {
		return errors.New("project secret migration preflight: invalid or unavailable legacy metadata; operator review required")
	}
	return nil
}
