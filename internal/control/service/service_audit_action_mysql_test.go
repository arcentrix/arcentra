// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/stretchr/testify/require"
)

// The existing retry-approval MySQL lane provides its explicit, disposable
// authorization database. Keep the canonical six-row regression alongside this
// complete denied/sensitive-success ledger proof; SQLite cannot prove capacity.
func TestRetryApprovalMySQLLosslessAuditActionsBothCollations(t *testing.T) {
	for _, collation := range []string{"utf8mb4_0900_ai_ci", "utf8mb4_bin"} {
		t.Run(collation, func(t *testing.T) {
			db, _, prefix := mysqlAuthDatabase(t)
			require.NoError(t, db.AutoMigrate(&model.AuditLog{}))
			var existing int64
			require.NoError(t, db.Model(&model.AuditLog{}).Count(&existing).Error)
			require.Zero(t, existing, "collation acceptance refuses existing audit data")
			var column struct{ ColumnType, IsNullable, CollationName string }
			metadataQuery := `SELECT COLUMN_TYPE AS column_type, IS_NULLABLE AS is_nullable, COLLATION_NAME AS collation_name FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'audit_log' AND COLUMN_NAME = 'action'`
			require.NoError(t, db.Raw(metadataQuery).Scan(&column).Error)
			require.Equal(t, "longtext", column.ColumnType, "AutoMigrate must already use the widened model")
			require.Equal(t, "NO", column.IsNullable)
			var index struct{ SubPart int }
			require.NoError(t, db.Raw(`SELECT SUB_PART AS sub_part FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'audit_log' AND INDEX_NAME = 'idx_audit_log_action'`).Scan(&index).Error)
			require.Equal(t, 191, index.SubPart)
			// Other authorization cases share this empty fixture's tables. Change
			// only the tested column collation, preserving its widened model/index.
			require.NoError(t, db.Exec("ALTER TABLE audit_log MODIFY COLUMN action LONGTEXT CHARACTER SET utf8mb4 COLLATE "+collation+" NOT NULL").Error)
			require.NoError(t, db.Raw(metadataQuery).Scan(&column).Error)
			require.Equal(t, collation, column.CollationName)

			allowed, denied, role := prefix+"allowed", prefix+"denied", prefix+"role"
			require.NoError(t, db.Create(&model.Role{RoleID: role, Name: role, IsEnabled: 1}).Error)
			require.NoError(t, db.Create(&model.RoleGrant{GrantID: prefix + "grant", SubjectType: "user", SubjectID: allowed, RoleID: role, ScopeType: "platform", IsEnabled: 1}).Error)
			actions := []string{
				"secret:" + strings.Repeat("a", 25), "secret:" + strings.Repeat("b", 26),
				"secret:" + strings.Repeat("c", 184), "secret:" + strings.Repeat("d", 185),
				"secret:" + strings.Repeat("監査🔒", 64), "secret:" + strings.Repeat("large", 14000),
				"secret:" + strings.Repeat("p", 184) + "-first-tail", "secret:" + strings.Repeat("p", 184) + "-second-tail",
			}
			for i, n := range []int{32, 33, 191, 192, 199, 70007, 202, 203} {
				require.Equal(t, n, utf8.RuneCountInString(actions[i]))
			}
			permissions := make([]model.Permission, len(actions))
			for i, action := range actions {
				id := fmt.Sprintf("%s%02d", prefix, i)
				id += strings.Repeat("x", 37-len(id))
				require.Len(t, id, 37)
				permissions[i] = model.Permission{PermissionID: id, ResourceType: "secret", Action: strings.TrimPrefix(action, "secret:")}
				require.NoError(t, db.Create(&permissions[i]).Error)
				require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: role, PermissionID: id}).Error)
			}
			t.Cleanup(func() {
				require.NoError(t, db.Where("actor_user_id IN ?", []string{allowed, denied}).Delete(&model.AuditLog{}).Error)
			})
			repositories := repo.NewRepositories(generationServiceDatabase{db}, nil)
			checker, err := authz.NewAuthorizer(repositories, nil)
			require.NoError(t, err)
			ctx := context.Background()
			projected, err := checker.EffectiveActions(ctx, authz.Subject{UserID: allowed}, "platform", "")
			require.NoError(t, err)
			require.ElementsMatch(t, actions, projected)
			projected, err = checker.EffectiveActions(ctx, authz.Subject{UserID: denied}, "platform", "")
			require.NoError(t, err)
			require.Empty(t, projected)

			// Every successful sensitive action and every denied dictionary action
			// must be present, plus one denied *:* check per actor. A successful
			// authorization response alone cannot detect an ignored audit INSERT.
			for _, actor := range []string{allowed, denied} {
				all, total, err := repositories.AuditLog.List(ctx, "", actor, 1, 200)
				require.NoError(t, err)
				require.EqualValues(t, len(actions)+1, total)
				require.Len(t, all, len(actions)+1)
				for _, action := range append(append([]string{}, actions...), "*:*") {
					rows, total, err := repositories.AuditLog.List(ctx, action, actor, 1, 200)
					require.NoError(t, err)
					require.EqualValues(t, 1, total)
					require.Len(t, rows, 1)
					require.Equal(t, action, rows[0].Action)
					require.Equal(t, actor, rows[0].ActorUserID)
					expected := model.AuditResultDenied
					if actor == allowed && action != "*:*" {
						expected = model.AuditResultSuccess
					}
					require.Equal(t, expected, rows[0].Result)
				}
			}
			// Opaque dictionary keys and bindings remain exact, independent of the
			// canonical action projected into the persisted audit ledger.
			for _, original := range permissions {
				var stored model.Permission
				require.NoError(t, db.Where("permission_id = ?", original.PermissionID).First(&stored).Error)
				require.Equal(t, original.PermissionID, stored.PermissionID)
				require.Equal(t, original.Action, stored.Action)
				var count int64
				require.NoError(t, db.Model(&model.RolePermissionBinding{}).Where("role_id = ? AND permission_id = ?", role, original.PermissionID).Count(&count).Error)
				require.EqualValues(t, 1, count)
			}
		})
	}
}
