// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func permissionProjectionFixture(t *testing.T, db *gorm.DB, prefix string) (*service.MeService, *repo.Repositories, string) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&model.Permission{}, &model.RolePermissionBinding{}, &model.Role{}, &model.RoleGrant{}, &model.Menu{}, &model.AuditLog{}))
	actor, role := prefix+"actor", prefix+"role"
	require.NoError(t, db.Create(&model.Role{RoleID: role, Name: role, IsEnabled: 1}).Error)
	require.NoError(t, db.Create(&model.RoleGrant{GrantID: prefix + "grant", SubjectType: "user", SubjectID: actor, RoleID: role, ScopeType: "platform", IsEnabled: 1}).Error)
	// The long dictionary ID is deliberately retained, including on MySQL.
	opaque := prefix + strings.Repeat("x", 37-len(prefix))
	require.Len(t, opaque, 37)
	for _, p := range []model.Permission{
		{PermissionID: opaque, ResourceType: "approval", Action: "read"},
		{PermissionID: prefix + "alias", ResourceType: "approval", Action: "read"},
		{PermissionID: prefix + "denied", ResourceType: "secret", Action: "write"},
	} {
		require.NoError(t, db.Create(&p).Error)
	}
	require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: role, PermissionID: opaque}).Error)
	for _, m := range []model.Menu{
		{MenuID: prefix + "allowed", Name: prefix + "allowed", Path: "/allowed", PermissionID: opaque, IsEnabled: 1, IsVisible: 1, Meta: "{}"},
		{MenuID: prefix + "denied", Name: prefix + "denied", Path: "/denied", PermissionID: prefix + "denied", IsEnabled: 1, IsVisible: 1, Meta: "{}"},
		{MenuID: prefix + "unknown", Name: prefix + "unknown", Path: "/unknown", PermissionID: "approval:read", IsEnabled: 1, IsVisible: 1, Meta: "{}"},
	} {
		require.NoError(t, db.Create(&m).Error)
	}
	repositories := repo.NewRepositories(generationServiceDatabase{db}, nil)
	checker, err := authz.NewAuthorizer(repositories, nil)
	require.NoError(t, err)
	svc := service.NewMeService(repositories.Menu, nil, service.NewMenuService(repositories.Menu), checker, repositories.Permission)
	return svc, repositories, actor
}

func assertPermissionProjection(t *testing.T, svc *service.MeService, actor string) {
	t.Helper()
	ctx := context.Background()
	actions, err := svc.GetEffectiveActions(ctx, actor, "platform", "")
	require.NoError(t, err)
	require.Equal(t, []string{"approval:read"}, actions)
	menus, err := svc.GetMenus(ctx, actor, "platform", "")
	require.NoError(t, err)
	require.Len(t, menus, 1)
	require.Equal(t, "approval:read", menus[0].PermissionAction)
	routes, err := svc.GetRoutes(ctx, actor, "platform", "")
	require.NoError(t, err)
	require.Equal(t, []string{"/allowed"}, routes)
}

func TestMePermissionDictionaryProjection(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "permissions.db")), &gorm.Config{})
	require.NoError(t, err)
	svc, repositories, actor := permissionProjectionFixture(t, db, "test-")
	assertPermissionProjection(t, svc, actor)
	// Stored dictionary identity stays unchanged while response metadata is canonical.
	var menu model.Menu
	require.NoError(t, db.Where("menu_id = ?", "test-allowed").First(&menu).Error)
	require.Len(t, menu.PermissionID, 37)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "test-grant").Update("is_enabled", 0).Error)
	actions, err := svc.GetEffectiveActions(context.Background(), actor, "platform", "")
	require.NoError(t, err)
	require.Empty(t, actions)
	menus, err := svc.GetMenus(context.Background(), actor, "platform", "")
	require.NoError(t, err)
	require.Empty(t, menus)
	routes, err := svc.GetRoutes(context.Background(), actor, "platform", "")
	require.NoError(t, err)
	require.Empty(t, routes)
	failed := projectionFailPermission{IPermissionRepository: repositories.Permission}
	denied := service.NewMeService(repositories.Menu, nil, service.NewMenuService(repositories.Menu), projectionEmptyAuthorizer{}, failed)
	_, err = denied.GetMenus(context.Background(), actor, "platform", "")
	require.ErrorIs(t, err, errProjectionDictionary)
	_, err = denied.GetRoutes(context.Background(), actor, "platform", "")
	require.ErrorIs(t, err, errProjectionDictionary)
}

var errProjectionDictionary = errors.New("dictionary unavailable")

type projectionFailPermission struct{ repo.IPermissionRepository }

func (projectionFailPermission) ListPermissions(context.Context) ([]model.Permission, error) {
	return nil, errProjectionDictionary
}

type projectionEmptyAuthorizer struct{ authz.IAuthorizer }

func (projectionEmptyAuthorizer) EffectiveActions(context.Context, authz.Subject, string, string) ([]string, error) {
	return []string{}, nil
}

// Picked up by the existing dedicated real-MySQL authorization service lane.
func TestRetryApprovalMySQLCanonicalPermissionProjection(t *testing.T) {
	db, _, prefix := mysqlAuthDatabase(t)
	svc, _, actor := permissionProjectionFixture(t, db, prefix)
	t.Cleanup(func() {
		require.NoError(t, db.Where("actor_user_id = ?", actor).Delete(&model.AuditLog{}).Error)
		require.NoError(t, db.Where("menu_id LIKE ?", prefix+"%").Delete(&model.Menu{}).Error)
	})
	assertPermissionProjection(t, svc, actor)
	var audits []model.AuditLog
	require.NoError(t, db.Where("actor_user_id = ?", actor).Find(&audits).Error)
	// Each of effective-actions, menus and routes denies secret:write and *:*.
	// Exact persisted content proves no ignored INSERT failure or truncation.
	require.Len(t, audits, 6)
	for _, entry := range audits {
		require.Contains(t, []string{"secret:write", "*:*"}, entry.Action)
		require.Equal(t, model.AuditResultDenied, entry.Result)
	}
}

func TestMePermissionDictionaryIdentityAndWildcard(t *testing.T) {
	for _, mode := range []string{"default", "spoof", "wildcard", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "menu.db")), &gorm.Config{})
			require.NoError(t, err)
			svc, _, actor := permissionProjectionFixture(t, db, "test-")
			id, resource, action := "approval:approve", "approval", "approve"
			switch mode {
			case "spoof":
				id, resource, action = "secret:write", "approval", "read"
			case "wildcard":
				id, resource, action = "wildcard-definition", "*", "*"
			case "malformed":
				id, resource, action = "bad-definition", " approval", "read"
			}
			require.NoError(t, db.Create(&model.Permission{PermissionID: id, ResourceType: resource, Action: action}).Error)
			require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: "test-role", PermissionID: id}).Error)
			require.NoError(t, db.Create(&model.Menu{MenuID: "extra", Name: "extra", Path: "/extra", PermissionID: id, IsEnabled: 1, IsVisible: 1, Meta: "{}"}).Error)
			actions, err := svc.GetEffectiveActions(context.Background(), actor, "platform", "")
			require.NoError(t, err)
			menus, err := svc.GetMenus(context.Background(), actor, "platform", "")
			require.NoError(t, err)
			routes, err := svc.GetRoutes(context.Background(), actor, "platform", "")
			require.NoError(t, err)
			switch mode {
			case "default":
				require.Equal(t, []string{"approval:approve", "approval:read"}, actions)
				require.Equal(t, []string{"/allowed", "/extra"}, routes)
			case "spoof":
				require.Equal(t, []string{"approval:read"}, actions)
				require.Equal(t, []string{"/allowed", "/extra"}, routes)
				for _, menu := range menus {
					require.Equal(t, "approval:read", menu.PermissionAction)
				}
			case "wildcard":
				require.Contains(t, actions, "*:*")
				require.Equal(t, []string{"/allowed", "/denied", "/extra"}, routes)
			case "malformed":
				require.Equal(t, []string{"approval:read"}, actions)
				require.Equal(t, []string{"/allowed"}, routes)
			}
		})
	}
}
