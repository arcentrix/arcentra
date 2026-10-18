// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
)

// Preflight all new trusted-origin shapes before any DDL (MySQL DDL implicitly
// commits). Never infer/backfill actors from TriggeredBy, timestamps or scope.
func preflightInboxSchema(ctx context.Context, db *gorm.DB) error {
	if db.Migrator().HasTable(&model.PipelineRun{}) && db.Migrator().HasColumn(&model.PipelineRun{}, "inbox_actor_id") != db.Migrator().HasColumn(&model.PipelineRun{}, "inbox_origin") {
		return errors.New("inbox partial schema requires operator review")
	}
	for _, table := range []string{"pipeline_run", "approval_request"} {
		if !db.Migrator().HasTable(table) {
			continue
		}
		var duplicates int64
		business := "run_id"
		if table == "approval_request" {
			business = "approval_id"
		}
		if err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM (SELECT " + business + " FROM " + table + " GROUP BY " + business + " HAVING COUNT(*) > 1) AS ambiguous").Scan(&duplicates).Error; err != nil {
			return errors.New("inbox identity preflight unavailable")
		}
		if duplicates != 0 {
			return errors.New("inbox identity preflight failed")
		}
		if db.Migrator().HasColumn(table, "inbox_origin") {
			q := db.WithContext(ctx).Table(table).Where("inbox_origin IS NULL OR inbox_origin NOT IN (0,1)")
			if err := q.Count(&duplicates).Error; err != nil {
				return errors.New("inbox origin preflight unavailable")
			}
			if duplicates != 0 {
				return errors.New("inbox origin preflight failed")
			}
		}
	}
	if db.Migrator().HasTable(&model.PipelineRun{}) && db.Migrator().HasColumn(&model.PipelineRun{}, "inbox_actor_id") {
		if !db.Migrator().HasColumn(&model.PipelineRun{}, "inbox_origin") {
			return errors.New("inbox partial schema requires operator review")
		}
		var invalid int64
		if err := db.WithContext(ctx).Table("pipeline_run").Where("inbox_actor_id IS NULL OR (inbox_origin = 0 AND inbox_actor_id <> '') OR (inbox_origin = 1 AND (inbox_actor_id = '' OR LENGTH(inbox_actor_id) > 64 OR TRIM(inbox_actor_id) <> inbox_actor_id))").Count(&invalid).Error; err != nil {
			return errors.New("inbox actor preflight unavailable")
		}
		if invalid > 0 {
			return errors.New("inbox actor preflight failed")
		}
	}
	return nil
}
func inboxSchemaIndexes(ctx context.Context, db *gorm.DB) error {
	for _, index := range []struct{ table, name string }{{"pipeline_run", "idx_run_inbox_repair"}, {"approval_request", "idx_approval_inbox_repair"}} {
		if !db.Migrator().HasIndex(index.table, index.name) {
			if err := db.WithContext(ctx).Exec("CREATE INDEX " + index.name + " ON " + index.table + " (inbox_origin, id)").Error; err != nil {
				return errors.New("inbox repair index migration unavailable")
			}
		}
	}
	return nil
}
