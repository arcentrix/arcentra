// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
)

// The real acceptance fixture must never revoke the actor's approval authority
// when exercising notification-only visibility. Return the exact restore row.
func revokeContainerInboxRead(ctx context.Context, db *gorm.DB, grant, reviewer string) (model.RolePermissionBinding, error) {
	var bindings []model.RolePermissionBinding
	err := db.WithContext(ctx).Model(&model.RolePermissionBinding{}).
		Joins("JOIN role_grant ON role_grant.role_id = role_permission_binding.role_id").
		Joins("JOIN permission ON permission.permission_id = role_permission_binding.permission_id").
		Where("role_grant.grant_id = ? AND role_grant.subject_type = ? AND role_grant.subject_id = ? AND role_grant.is_enabled = ? AND permission.resource_type = ? AND permission.action = ?", grant, "user", reviewer, 1, "approval", "read").
		Select("role_permission_binding.*").Find(&bindings).Error
	if err != nil {
		return model.RolePermissionBinding{}, err
	}
	if len(bindings) != 1 {
		return model.RolePermissionBinding{}, fmt.Errorf("expected exactly one notification read binding, got %d", len(bindings))
	}
	binding := bindings[0]
	result := db.WithContext(ctx).Where("role_id = ? AND permission_id = ?", binding.RoleID, binding.PermissionID).Delete(&model.RolePermissionBinding{})
	if result.Error != nil {
		return binding, result.Error
	}
	if result.RowsAffected != 1 {
		return binding, fmt.Errorf("exact notification read binding was not removed")
	}
	return binding, nil
}

func TestContainerInboxRevocationPreservesApprovalAuthority(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "inbox.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RoleGrant{}, &model.Permission{}, &model.RolePermissionBinding{}))
	grant := model.RoleGrant{GrantID: "grant", SubjectType: "user", SubjectID: "reviewer", RoleID: "role", ScopeType: "project", ScopeID: "project", IsEnabled: 1}
	require.NoError(t, db.Create(&grant).Error)
	require.NoError(t, db.First(&grant).Error)
	for _, action := range []string{"read", "approve", "reject"} {
		require.NoError(t, db.Create(&model.Permission{PermissionID: action, ResourceType: "approval", Action: action}).Error)
		require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: "role", PermissionID: action}).Error)
	}
	binding, err := revokeContainerInboxRead(context.Background(), db, "grant", "reviewer")
	require.NoError(t, err)
	require.Equal(t, "read", binding.PermissionID)
	var after model.RoleGrant
	require.NoError(t, db.First(&after).Error)
	require.Equal(t, grant, after)
	var remaining []model.RolePermissionBinding
	require.NoError(t, db.Order("permission_id").Find(&remaining).Error)
	require.Equal(t, []model.RolePermissionBinding{{RoleID: "role", PermissionID: "approve"}, {RoleID: "role", PermissionID: "reject"}}, remaining)
	require.NoError(t, db.Create(&binding).Error)
	var count int64
	require.NoError(t, db.Model(&model.RolePermissionBinding{}).Count(&count).Error)
	require.EqualValues(t, 3, count)
}
