// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// MySQL DDL is not transactional. A partial prior application requires operator
// review before any other schema change; never silently backfill history or
// infer an owner, request binding, ciphertext version or a provider outcome.
func preflightCopilotHistorySchema(ctx context.Context, db *gorm.DB) error {
	entities := []any{&model.CopilotConversation{}, &model.CopilotTurn{}, &model.CopilotActionPlan{}}
	found := 0
	for _, entity := range entities {
		if db.Migrator().HasTable(entity) {
			found++
		}
	}
	if found == 0 {
		return nil
	}
	if found != len(entities) {
		return errors.New("copilot history partial schema requires operator review")
	}
	for _, entity := range entities {
		statement := &gorm.Statement{DB: db}
		if err := statement.Parse(entity); err != nil {
			return errors.New("copilot history schema preflight unavailable")
		}
		for _, field := range statement.Schema.Fields {
			if field.DBName != "" && !db.Migrator().HasColumn(entity, field.DBName) {
				return errors.New("copilot history partial schema requires operator review")
			}
		}
	}
	for _, check := range []struct{ table, keys string }{
		{"copilot_conversation", "conversation_id"}, {"copilot_conversation", "owner_user_id, project_id, create_request_id"},
		{"copilot_turn", "turn_id"}, {"copilot_turn", "conversation_id, sequence"}, {"copilot_turn", "owner_user_id, project_id, request_id"},
		{"copilot_action_plan", "plan_id"}, {"copilot_action_plan", "owner_user_id, project_id, request_id"},
	} {
		var n int64
		if err := db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).WithContext(ctx).Raw("SELECT COUNT(*) FROM (SELECT " + check.keys + " FROM " + check.table + " GROUP BY " + check.keys + " HAVING COUNT(*) > 1) AS ambiguous").Scan(&n).Error; err != nil {
			return errors.New("copilot history identity preflight unavailable")
		}
		if n != 0 {
			return errors.New("copilot history identity preflight failed")
		}
	}
	return nil
}
