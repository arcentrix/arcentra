// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Uses the existing explicit loopback-only disposable MySQL acceptance contract.
// The callback exercises the transaction's current locking view, while the service
// SQLite/HTTP suites exercise the production Casbin callback and strict DTOs.
func organizationMySQLAuthorize(ctx context.Context, view *Repositories, actor string) (ScopedAccess, error) {
	user, err := view.User.(IExecutionActorReader).GetExecutionActor(ctx, actor)
	if err != nil {
		return nil, err
	}
	if user.UserID != actor || user.IsEnabled != 1 {
		return nil, ErrOrganizationForbidden
	}
	if _, err := view.TeamMember.ListUserTeams(ctx, actor); err != nil {
		return nil, err
	}
	if _, err := view.Permission.ListPermissions(ctx); err != nil {
		return nil, err
	}
	if _, err := view.Permission.ListAllRoleBindings(ctx); err != nil {
		return nil, err
	}
	roles, err := view.Role.BatchGet(ctx, []string{consts.RolePlatformSuperAdmin, consts.RoleOrgOwner, consts.RoleOrgAdmin, consts.RoleOrgMember, consts.RoleProjectOwner, consts.RoleProjectDeveloper})
	if err != nil {
		return nil, err
	}
	enabled := map[string]bool{}
	for _, r := range roles {
		enabled[r.RoleID] = r.IsEnabled == 1
	}
	grants, err := view.RoleGrant.ListAllEnabled(ctx)
	if err != nil {
		return nil, err
	}
	return func(kind, id, org string) (ScopedCapabilities, error) {
		c := ScopedCapabilities{}
		for _, g := range grants {
			if g.SubjectType != "user" || g.SubjectID != actor || g.IsEnabled != 1 || !enabled[g.RoleID] || (g.ExpiresAt != nil && !g.ExpiresAt.After(time.Now())) {
				continue
			}
			if g.ScopeType == "platform" && g.ScopeID == "" && g.RoleID == consts.RolePlatformSuperAdmin {
				return ScopedCapabilities{Read: true, Update: true, ManageMembers: true, AssignOwner: true, Create: true, CreateProject: true, AssignableRoleIDs: []string{consts.RoleOrgOwner, consts.RoleOrgAdmin, consts.RoleOrgMember, consts.RoleProjectOwner, consts.RoleProjectMaintainer, consts.RoleProjectDeveloper, consts.RoleProjectReporter, consts.RoleProjectGuest}}, nil
			}
			if g.ScopeType == kind && g.ScopeID == id {
				switch g.RoleID {
				case consts.RoleOrgOwner, consts.RoleProjectOwner:
					c.Read = true
					c.Update = true
					c.ManageMembers = true
					c.AssignOwner = true
					c.CreateProject = true
				case consts.RoleOrgAdmin:
					c.Read = true
					c.Update = true
					c.ManageMembers = true
					c.CreateProject = true
				case consts.RoleOrgMember:
					c.Read = true
				}
			}
		}
		if c.ManageMembers {
			c.AssignableRoleIDs = []string{consts.RoleOrgAdmin, consts.RoleOrgMember}
			if c.AssignOwner {
				c.AssignableRoleIDs = append(c.AssignableRoleIDs, consts.RoleOrgOwner)
			}
		}
		return c, nil
	}, nil
}
func organizationMySQLFixture(t *testing.T) (context.Context, *gorm.DB, *gorm.DB, IOrganizationRepository) {
	t.Helper()
	db, other := openRetryApprovalMySQL(t), openRetryApprovalMySQL(t)
	models := []any{&model.Organization{}, &model.OrganizationMember{}, &model.Project{}, &model.ProjectMember{}, &model.User{}, &model.Team{}, &model.TeamMember{}, &model.Permission{}, &model.Role{}, &model.RolePermissionBinding{}, &model.RoleGrant{}, &model.AuditLog{}, &model.Menu{}}
	for _, m := range models {
		if db.Migrator().HasTable(m) {
			var n int64
			require.NoError(t, db.Model(m).Count(&n).Error)
			require.Zero(t, n, "MySQL acceptance refuses existing fixture rows")
		}
	}
	require.NoError(t, db.AutoMigrate(models...))
	require.NoError(t, seedAuthorization(context.Background(), db))
	t.Cleanup(func() {
		for i := len(models) - 1; i >= 0; i-- {
			require.NoError(t, db.WithContext(context.Background()).Where("1 = 1").Delete(models[i]).Error)
		}
	})
	for _, user := range []string{"admin", "alice", "bob", "other"} {
		require.NoError(t, db.Create(&model.User{UserID: user, Username: user, IsEnabled: 1}).Error)
	}
	require.NoError(t, db.Create(&model.RoleGrant{GrantID: "organization-platform", SubjectType: "user", SubjectID: "admin", RoleID: consts.RolePlatformSuperAdmin, ScopeType: "platform", IsEnabled: 1}).Error)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx, db, other, NewOrganizationRepo(statusTestDB{db})
}
func TestOrganizationMySQLConcurrentMemberCASAndScopeIsolation(t *testing.T) {
	ctx, db, other, r := organizationMySQLFixture(t)
	o, err := r.Create(ctx, "admin", model.OrganizationInput{Name: "concurrent", DisplayName: "Concurrent"}, organizationMySQLAuthorize)
	require.NoError(t, err)
	second := NewOrganizationRepo(statusTestDB{other})
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, repository := range []IOrganizationRepository{r, second} {
		repository := repository
		safe.Go(func() {
			<-start
			_, e := repository.ChangeMember(ctx, "admin", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "alice", RoleID: consts.RoleOrgMember}, organizationMySQLAuthorize)
			results <- e
		})
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		e := awaitRetryMySQL(t, results)
		if e == nil {
			successes++
		} else if errors.Is(e, ErrOrganizationConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	start = make(chan struct{})
	for _, repository := range []IOrganizationRepository{r, second} {
		repository := repository
		safe.Go(func() {
			<-start
			_, e := repository.ChangeMember(ctx, "admin", "org", o.OrgID, "update", model.ScopedMemberInput{UserID: "alice", RoleID: consts.RoleOrgAdmin, Version: 1}, organizationMySQLAuthorize)
			results <- e
		})
	}
	close(start)
	successes, conflicts = 0, 0
	for range 2 {
		e := awaitRetryMySQL(t, results)
		if e == nil {
			successes++
		} else if errors.Is(e, ErrOrganizationConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	foreign, err := r.Create(ctx, "admin", model.OrganizationInput{Name: "foreign", DisplayName: "Foreign"}, organizationMySQLAuthorize)
	require.NoError(t, err)
	_, err = r.ChangeMember(ctx, "alice", "org", foreign.OrgID, "add", model.ScopedMemberInput{UserID: "bob", RoleID: consts.RoleOrgMember}, organizationMySQLAuthorize)
	require.ErrorIs(t, err, ErrOrganizationForbidden)
	var grants int64
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("subject_id = ? AND scope_id = ? AND is_enabled = ?", "alice", o.OrgID, 1).Count(&grants).Error)
	require.Equal(t, int64(1), grants)
}
func TestOrganizationMySQLRevocationSerializesBeforeMemberAdmission(t *testing.T) {
	ctx, db, other, r := organizationMySQLFixture(t)
	o, err := r.Create(ctx, "admin", model.OrganizationInput{Name: "revocation", DisplayName: "Revocation"}, organizationMySQLAuthorize)
	require.NoError(t, err)
	_, err = r.ChangeMember(ctx, "admin", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "alice", RoleID: consts.RoleOrgAdmin}, organizationMySQLAuthorize)
	require.NoError(t, err)
	revoke := other.WithContext(ctx).Begin()
	require.NoError(t, revoke.Error)
	t.Cleanup(func() { _ = revoke.Rollback().Error })
	require.NoError(t, revoke.Model(&model.RoleGrant{}).Where("subject_id = ? AND scope_id = ?", "alice", o.OrgID).Update("is_enabled", 0).Error)
	result := make(chan error, 1)
	safe.Go(func() {
		_, e := r.ChangeMember(ctx, "alice", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "bob", RoleID: consts.RoleOrgMember}, organizationMySQLAuthorize)
		result <- e
	})
	assertRetryMySQLWaiting(t, result)
	require.NoError(t, revoke.Commit().Error)
	require.ErrorIs(t, awaitRetryMySQL(t, result), ErrOrganizationForbidden)
	var count int64
	require.NoError(t, db.Model(&model.OrganizationMember{}).Where("org_id = ? AND user_id = ?", o.OrgID, "bob").Count(&count).Error)
	require.Zero(t, count)
}
func TestOrganizationMySQLArchiveSerializesProjectCreation(t *testing.T) {
	ctx, _, other, r := organizationMySQLFixture(t)
	o, err := r.Create(ctx, "admin", model.OrganizationInput{Name: "archive", DisplayName: "Archive"}, organizationMySQLAuthorize)
	require.NoError(t, err)
	creator := NewProjectRepo(statusTestDB{other}).(IProjectCreationRepository)
	start := make(chan struct{})
	archiveResult, createResult := make(chan error, 1), make(chan error, 1)
	safe.Go(func() {
		<-start
		_, e := r.Update(ctx, "admin", o.OrgID, model.OrganizationInput{Version: 1}, true, organizationMySQLAuthorize)
		archiveResult <- e
	})
	safe.Go(func() {
		<-start
		e := creator.CreateAuthorized(ctx, &model.Project{ProjectID: "concurrent-project", OrgID: o.OrgID, Name: "project", CreatedBy: "admin", Status: model.ProjectStatusActive, IsEnabled: 1, Settings: datatypes.JSON("{}"), EnvVars: datatypes.JSON("{}"), BuildConfig: datatypes.JSON("{}")}, organizationMySQLAuthorize)
		createResult <- e
	})
	close(start)
	archived, created := awaitRetryMySQL(t, archiveResult), awaitRetryMySQL(t, createResult)
	if archived == nil {
		require.ErrorIs(t, created, ErrOrganizationConflict)
	} else {
		require.ErrorIs(t, archived, ErrOrganizationConflict)
		require.NoError(t, created)
	}
}
func TestOrganizationMySQLCaseCollidingIdentityDenies(t *testing.T) {
	ctx, _, _, r := organizationMySQLFixture(t)
	o, err := r.Create(ctx, "admin", model.OrganizationInput{Name: "case-test", DisplayName: "Case"}, organizationMySQLAuthorize)
	require.NoError(t, err)
	_, err = r.Get(ctx, "ADMIN", o.OrgID, organizationMySQLAuthorize)
	require.Error(t, err)
	_, err = r.ChangeMember(ctx, "admin", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "ALICE", RoleID: consts.RoleOrgMember}, organizationMySQLAuthorize)
	require.ErrorIs(t, err, ErrOrganizationNotFound)
}

func TestOrganizationMySQLLastOwnerMutationsSerialize(t *testing.T) {
	ctx, db, other, r := organizationMySQLFixture(t)
	o, err := r.Create(ctx, "admin", model.OrganizationInput{Name: "owners", DisplayName: "Owners"}, organizationMySQLAuthorize)
	require.NoError(t, err)
	for _, user := range []string{"alice", "bob"} {
		_, err = r.ChangeMember(ctx, "admin", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: user, RoleID: consts.RoleOrgOwner}, organizationMySQLAuthorize)
		require.NoError(t, err)
	}
	// Simulate a separately revoked creation-owner grant. The immutable owner row
	// remains, but the last enabled managed owner still cannot be removed.
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("scope_id = ? AND subject_id = ?", o.OrgID, "admin").Update("is_enabled", 0).Error)
	start := make(chan struct{})
	result := make(chan error, 2)
	second := NewOrganizationRepo(statusTestDB{other})
	for i, repository := range []IOrganizationRepository{r, second} {
		repository := repository
		user := []string{"alice", "bob"}[i]
		safe.Go(func() {
			<-start
			_, e := repository.ChangeMember(ctx, "admin", "org", o.OrgID, "remove", model.ScopedMemberInput{UserID: user, Version: 1}, organizationMySQLAuthorize)
			result <- e
		})
	}
	close(start)
	success, conflict := 0, 0
	for range 2 {
		e := awaitRetryMySQL(t, result)
		if e == nil {
			success++
		} else if errors.Is(e, ErrOrganizationConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, conflict)
}
func TestOrganizationMySQLCollationAliasCannotBypassFrozenLifecycle(t *testing.T) {
	ctx, db, _, _ := organizationMySQLFixture(t)
	require.NoError(t, db.Create(&model.Organization{OrgID: "frozen-org", Name: "frozen", Status: model.OrgStatusFrozen, IsEnabled: 0, Settings: datatypes.JSON("{}")}).Error)
	var matches int64
	require.NoError(t, db.Model(&model.Organization{}).Where("org_id = ?", "FROZEN-ORG").Count(&matches).Error)
	creator := NewProjectRepo(statusTestDB{db}).(IProjectCreationRepository)
	p := &model.Project{ProjectID: "alias-project", OrgID: "FROZEN-ORG", Name: "alias", CreatedBy: "admin", Status: model.ProjectStatusActive, IsEnabled: 1, Settings: datatypes.JSON("{}"), EnvVars: datatypes.JSON("{}"), BuildConfig: datatypes.JSON("{}")}
	err := creator.CreateAuthorized(ctx, p, organizationMySQLAuthorize)
	if matches > 0 {
		require.ErrorIs(t, err, ErrOrganizationConflict)
	} else {
		require.NoError(t, err, "binary collation treats the uppercase legacy scope as a genuinely separate absent ID")
	}
}

func TestOrganizationMySQLListAndCreateDoNotInvertDomainLocks(t *testing.T) {
	ctx, _, other, r := organizationMySQLFixture(t)
	_, err := r.Create(ctx, "admin", model.OrganizationInput{Name: "existing", DisplayName: "Existing"}, organizationMySQLAuthorize)
	require.NoError(t, err)
	listReady, allowList, createReady, allowCreate := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	listAuth := func(ctx context.Context, view *Repositories, actor string) (ScopedAccess, error) {
		close(listReady)
		<-allowList
		return organizationMySQLAuthorize(ctx, view, actor)
	}
	createAuth := func(ctx context.Context, view *Repositories, actor string) (ScopedAccess, error) {
		access, err := organizationMySQLAuthorize(ctx, view, actor)
		close(createReady)
		<-allowCreate
		return access, err
	}
	listed, created := make(chan error, 1), make(chan error, 1)
	safe.Go(func() { _, e := r.List(ctx, "admin", "", 1, 20, listAuth); listed <- e })
	select {
	case <-listReady:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	safe.Go(func() {
		_, e := NewOrganizationRepo(statusTestDB{other}).Create(ctx, "admin", model.OrganizationInput{Name: "during-list", DisplayName: "During list"}, createAuth)
		created <- e
	})
	select {
	case <-createReady:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(allowList)
	assertRetryMySQLWaiting(t, listed)
	close(allowCreate)
	require.NoError(t, awaitRetryMySQL(t, created))
	require.NoError(t, awaitRetryMySQL(t, listed))
}
func TestOrganizationMySQLMigrationUpgradesOldShape(t *testing.T) {
	db := openRetryApprovalMySQL(t)
	// Only this explicitly empty disposable schema may be reshaped to emulate a
	// pre-upgrade installation. Existing rows abort before any table is dropped.
	for _, entity := range schemaModels() {
		if db.Migrator().HasTable(entity) {
			var count int64
			require.NoError(t, db.Model(entity).Count(&count).Error)
			require.Zero(t, count, "old-shape acceptance refuses existing rows")
		}
	}
	for _, entity := range []any{&model.OrganizationMember{}, &model.ProjectMember{}, &model.Organization{}, &model.Project{}} {
		require.NoError(t, db.Migrator().DropTable(entity))
	}
	t.Cleanup(func() {
		for _, entity := range schemaModels() {
			if db.Migrator().HasTable(entity) {
				require.NoError(t, db.WithContext(context.Background()).Where("1 = 1").Delete(entity).Error)
			}
		}
	})
	seedOldOrganizationShape(t, db)
	assertOrganizationMySQLLegacyColumnTypes(t, db, false)
	for _, id := range []string{"existing-org", "EXISTING-ORG"} {
		duplicate := legacyOrganizationShape{OrgID: id, Name: "duplicate-fixture", Status: model.OrgStatusActive, IsEnabled: 1}
		require.NoError(t, db.Create(&duplicate).Error)
		var matches int64
		require.NoError(t, db.Model(&legacyOrganizationShape{}).Where("org_id = ?", "existing-org").Count(&matches).Error)
		if matches > 1 {
			require.ErrorContains(t, MigrateSchema(context.Background(), db), "preflight")
			require.False(t, db.Migrator().HasColumn(&model.Organization{}, "Version"), "ambiguous baseline must fail before any schema mutation")
			assertOrganizationMySQLLegacyColumnTypes(t, db, false)
		}
		require.NoError(t, db.Where("id = ?", duplicate.ID).Delete(&legacyOrganizationShape{}).Error)
	}
	verifyOldOrganizationUpgrade(t, db)
	assertOrganizationMySQLLegacyColumnTypes(t, db, true)
}

// Assert the real baseline's unbounded strings are narrowed by the explicit
// migration, not by fixture setup; project_member was already varchar(64).
func assertOrganizationMySQLLegacyColumnTypes(t *testing.T, db *gorm.DB, upgraded bool) {
	t.Helper()
	for _, column := range []struct{ table, name string }{
		{"organization", "org_id"}, {"organization", "name"},
		{"organization_member", "org_id"}, {"organization_member", "user_id"},
		{"project", "project_id"}, {"project_member", "project_id"}, {"project_member", "user_id"},
	} {
		var value struct {
			DataType               string
			CharacterMaximumLength int64
		}
		require.NoError(t, db.Raw("SELECT data_type, character_maximum_length FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?", column.table, column.name).Scan(&value).Error)
		if upgraded || column.table == "project_member" {
			require.Equal(t, "varchar", value.DataType, "%s.%s", column.table, column.name)
			require.Equal(t, int64(64), value.CharacterMaximumLength, "%s.%s", column.table, column.name)
		} else {
			require.Equal(t, "longtext", value.DataType, "%s.%s", column.table, column.name)
			require.Greater(t, value.CharacterMaximumLength, int64(64), "%s.%s", column.table, column.name)
		}
	}
}
