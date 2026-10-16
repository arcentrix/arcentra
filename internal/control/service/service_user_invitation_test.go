package service

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/http"
)

func TestInviteUserPersistsIdentityRoleAndLoginCredentials(t *testing.T) {
	ctx := context.Background()
	svc, db := registrationServiceFixture(t, nil)
	if err := db.WithContext(ctx).AutoMigrate(&model.Role{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Menu{}); err != nil {
		t.Fatal(err)
	}
	adapter := registrationTestDatabase{db: db}
	svc.roleRepo = repo.NewRoleRepo(adapter)
	svc.permissionRepo = repo.NewPermissionRepo(adapter)
	svc.menuRepo = repo.NewMenuRepo(adapter)
	role := &model.Role{RoleID: "synthetic-observer", Name: "synthetic-observer", ScopeType: "platform", IsEnabled: 1}
	if err := svc.roleRepo.Create(ctx, role); err != nil {
		t.Fatal(err)
	}
	permission := &model.Permission{PermissionID: "pipeline:read", ResourceType: "pipeline", Action: "read", ScopeType: "project"}
	if err := svc.permissionRepo.CreatePermission(ctx, permission); err != nil {
		t.Fatal(err)
	}
	requestedBindings := []model.RolePermissionBinding{{RoleID: role.RoleID, PermissionID: permission.PermissionID}}
	if err := svc.permissionRepo.BatchUpsertRoleBindings(ctx, requestedBindings); err != nil {
		t.Fatal(err)
	}
	userID, err := svc.InviteUser(ctx, "synthetic-invite@example.invalid", role.RoleID, "synthetic-inviter")
	if err != nil || userID == "" {
		t.Fatalf("invite did not persist the user: %v", err)
	}
	user, err := svc.userRepo.GetUserByUserID(userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != "synthetic-invite" || user.FullName != user.Username || user.Email != "synthetic-invite@example.invalid" ||
		user.IsEnabled != 1 || !strings.HasPrefix(user.Password, "$2") {
		t.Fatal("invited identity or temporary password hash was not persisted", err)
	}
	ext, err := svc.userExtRepo.Get(ctx, userID)
	if err != nil || ext.UserID != userID || ext.Timezone != "UTC" {
		t.Fatal("invited user extension missing", err)
	}
	grants, err := svc.roleGrantRepo.ListBySubject(ctx, "user", userID)
	if err != nil || len(grants) != 1 {
		t.Fatal("invitation did not preserve the exact requested role grant", err)
	}
	if grants[0].RoleID != role.RoleID || grants[0].ScopeType != "platform" ||
		grants[0].ScopeID != "" || grants[0].GrantedBy != "synthetic-inviter" {
		t.Fatal("invitation changed the requested role grant")
	}
	roles, routes, err := svc.GetUserRolesAndRoutes(userID, "")
	if err != nil || len(roles) != 1 || roles[0].RoleID != role.RoleID || len(routes) != 0 {
		t.Fatal("invited role could not be resolved by the login path", err)
	}
	bindings, err := svc.permissionRepo.ListRoleBindings(ctx, []string{role.RoleID})
	if err != nil || len(bindings) != 1 || bindings[0].PermissionID != "pipeline:read" {
		t.Fatal("invitation changed the requested read-only role", err)
	}
	password := "Synthetic-Invited-Password!"
	encoded := base64.StdEncoding.EncodeToString([]byte(password))
	if err := svc.ResetPassword(userID, &model.ResetPasswordReq{NewPassword: encoded}); err != nil {
		t.Fatal(err)
	}
	auth := http.Auth{SecretKey: "synthetic-invitation-signing-key", AccessExpire: time.Minute, RefreshExpire: time.Minute}
	// Exercise real credential and role resolution. Redis/session verification remains
	// in the actual-stack HTTP journey; no fake session store makes this test pass.
	_, err = svc.Login(&model.Login{Username: user.Username, Password: encoded}, auth)
	if err == nil || err.Error() != "cache not available" {
		t.Fatalf("invited login did not reach the intentionally absent session store: %v", err)
	}
	wrong := base64.StdEncoding.EncodeToString([]byte("wrong-synthetic-password"))
	_, err = svc.Login(&model.Login{Username: user.Username, Password: wrong}, auth)
	if err == nil || err.Error() != http.UserIncorrectPassword.Msg {
		t.Fatalf("invited login accepted a wrong password: %v", err)
	}
	if db.Migrator().HasTable(&model.AddUserReq{}) {
		t.Fatal("invite created a request-DTO table")
	}
}

func TestInviteUserReportsPersistenceFailureWithoutGrant(t *testing.T) {
	ctx := context.Background()
	svc, db := registrationServiceFixture(t, nil)
	if err := db.WithContext(ctx).Migrator().DropTable(&model.User{}); err != nil {
		t.Fatal(err)
	}
	userID, err := svc.InviteUser(ctx, "failed-invite@example.invalid", "synthetic-observer", "synthetic-inviter")
	if err == nil || userID != "" || !strings.HasPrefix(err.Error(), "add user:") {
		t.Fatalf("failed insert was reported as a successful invitation: %v", err)
	}
	for _, table := range []any{&model.UserExt{}, &model.RoleGrant{}} {
		var count int64
		if err := db.WithContext(ctx).Model(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("failed insert created extension/grant rows: count=%d, error=%v", count, err)
		}
	}
}
