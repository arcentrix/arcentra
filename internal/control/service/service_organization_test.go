// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type organizationTestDB struct{ db *gorm.DB }

func (d organizationTestDB) Database() *gorm.DB { return d.db }
func organizationFixture(t *testing.T) (*gorm.DB, *OrganizationService) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "organization.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.Organization{}, &model.OrganizationMember{}, &model.Project{}, &model.ProjectMember{}, &model.User{}, &model.Team{}, &model.TeamMember{}, &model.Permission{}, &model.Role{}, &model.RolePermissionBinding{}, &model.RoleGrant{}, &model.AuditLog{}))
	for _, p := range consts.SeedBuiltinPermissions() {
		require.NoError(t, db.Create(&model.Permission{PermissionID: p.PermissionID, ResourceType: p.ResourceType, Action: p.Action, ScopeType: string(p.ScopeType), IsSystem: 1}).Error)
	}
	for _, r := range consts.SeedBuiltinRoles() {
		require.NoError(t, db.Create(&model.Role{RoleID: r.RoleID, Name: r.Name, ScopeType: string(r.ScopeType), IsSystem: 1, IsEnabled: 1}).Error)
	}
	for role, permissions := range consts.SeedBuiltinRolePermissions() {
		for _, permission := range permissions {
			require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: role, PermissionID: permission}).Error)
		}
	}
	for _, id := range []string{"admin", "alice", "bob", "outsider"} {
		require.NoError(t, db.Create(&model.User{UserID: id, Username: id, FullName: id, IsEnabled: 1}).Error)
	}
	require.NoError(t, db.Create(&model.RoleGrant{GrantID: "platform-admin", SubjectType: "user", SubjectID: "admin", RoleID: consts.RolePlatformSuperAdmin, ScopeType: "platform", IsEnabled: 1}).Error)
	return db, NewOrganizationService(repo.NewOrganizationRepo(organizationTestDB{db}))
}
func createTestOrganization(t *testing.T, s *OrganizationService, name string) *model.OrganizationDetail {
	t.Helper()
	o, err := s.Create(context.Background(), "admin", model.OrganizationInput{Name: name})
	require.NoError(t, err)
	return o
}

func TestOrganizationCRUDUsesRealRowsLiveAuthorityAndCAS(t *testing.T) {
	db, s := organizationFixture(t)
	ctx := context.Background()
	_, err := s.Create(ctx, "outsider", model.OrganizationInput{Name: "denied"})
	require.ErrorIs(t, err, repo.ErrOrganizationForbidden)
	require.NoError(t, db.Create(&model.RoleGrant{GrantID: "legacy-scope", SubjectType: "user", SubjectID: "outsider", RoleID: consts.RoleOrgOwner, ScopeType: "org", ScopeID: "legacy-only", IsEnabled: 1}).Error)
	list, err := s.List(ctx, "outsider", "", 1, 20)
	require.NoError(t, err)
	require.Empty(t, list.List)
	require.False(t, list.CanCreate)
	o := createTestOrganization(t, s, "sample-org")
	require.True(t, o.IsMember)
	require.Equal(t, uint64(1), o.Version)
	require.NoError(t, db.Create(&model.Project{ProjectID: "old-project", OrgID: "legacy-only", Name: "legacy", IsEnabled: 1, Status: model.ProjectStatusActive}).Error)
	list, err = s.List(ctx, "admin", "", 1, 20)
	require.NoError(t, err)
	require.Len(t, list.List, 1)
	require.Equal(t, 1, list.Total)
	_, err = s.Get(ctx, "outsider", o.OrgID)
	require.ErrorIs(t, err, repo.ErrOrganizationForbidden)
	updated, err := s.Update(ctx, "admin", o.OrgID, model.OrganizationInput{DisplayName: "Updated", Version: o.Version})
	require.NoError(t, err)
	require.Equal(t, uint64(2), updated.Version)
	_, err = s.Update(ctx, "admin", o.OrgID, model.OrganizationInput{DisplayName: "Stale", Version: o.Version})
	require.ErrorIs(t, err, repo.ErrOrganizationConflict)
	_, err = s.Create(ctx, "admin", model.OrganizationInput{Name: "sample-org"})
	require.ErrorIs(t, err, repo.ErrOrganizationConflict)
	_, err = s.Archive(ctx, "admin", o.OrgID, updated.Version)
	require.NoError(t, err)
	_, err = s.Get(ctx, "admin", o.OrgID)
	require.ErrorIs(t, err, repo.ErrOrganizationNotFound)
	var persisted model.Organization
	require.NoError(t, db.Where("org_id = ?", o.OrgID).First(&persisted).Error)
	require.Equal(t, model.OrgStatusDeleted, persisted.Status)
	var count int64
	require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", "old-project").Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestScopedMembershipRoleLifecyclePreservesOtherGrants(t *testing.T) {
	db, s := organizationFixture(t)
	ctx := context.Background()
	o := createTestOrganization(t, s, "members")
	added, err := s.ChangeMember(ctx, "admin", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "alice", RoleID: consts.RoleOrgAdmin})
	require.NoError(t, err)
	require.Equal(t, consts.RoleOrgAdmin, added.RoleID)
	_, err = s.ChangeMember(ctx, "admin", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "alice", RoleID: consts.RoleOrgAdmin})
	require.ErrorIs(t, err, repo.ErrOrganizationConflict)
	_, err = s.ChangeMember(ctx, "alice", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "bob", RoleID: consts.RoleOrgOwner})
	require.ErrorIs(t, err, repo.ErrOrganizationForbidden)
	_, err = s.ChangeMember(ctx, "admin", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "bob", RoleID: consts.RolePlatformSuperAdmin})
	require.ErrorIs(t, err, repo.ErrOrganizationInvalid)
	changed, err := s.ChangeMember(ctx, "admin", "org", o.OrgID, "update", model.ScopedMemberInput{UserID: "alice", RoleID: consts.RoleOrgMember, Version: added.Version})
	require.NoError(t, err)
	require.Equal(t, uint64(2), changed.Version)
	_, err = s.ChangeMember(ctx, "admin", "org", o.OrgID, "update", model.ScopedMemberInput{UserID: "alice", RoleID: consts.RoleOrgAdmin, Version: added.Version})
	require.ErrorIs(t, err, repo.ErrOrganizationConflict)
	_, err = s.ChangeMember(ctx, "alice", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "bob", RoleID: consts.RoleOrgMember})
	require.ErrorIs(t, err, repo.ErrOrganizationForbidden)
	require.NoError(t, db.Create(&model.RoleGrant{GrantID: "unmanaged", SubjectType: "user", SubjectID: "alice", RoleID: consts.RoleOrgMember, ScopeType: "org", ScopeID: o.OrgID, IsEnabled: 1}).Error)
	_, err = s.ChangeMember(ctx, "admin", "org", o.OrgID, "remove", model.ScopedMemberInput{UserID: "alice", Version: changed.Version})
	require.NoError(t, err)
	var grant model.RoleGrant
	require.NoError(t, db.Where("grant_id = ?", "unmanaged").First(&grant).Error)
	require.Equal(t, 1, grant.IsEnabled)
	again, err := s.ChangeMember(ctx, "admin", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "alice", RoleID: consts.RoleOrgMember})
	require.NoError(t, err)
	require.Equal(t, uint64(4), again.Version)
	_, err = s.ChangeMember(ctx, "admin", "org", o.OrgID, "remove", model.ScopedMemberInput{UserID: "alice", Version: changed.Version})
	require.ErrorIs(t, err, repo.ErrOrganizationConflict)
	_, err = s.ChangeMember(ctx, "admin", "org", o.OrgID, "remove", model.ScopedMemberInput{UserID: "admin", Version: 1})
	require.ErrorIs(t, err, repo.ErrOrganizationConflict)
	members, err := s.Members(ctx, "admin", "org", o.OrgID)
	require.NoError(t, err)
	require.Len(t, members.List, 2)
	require.True(t, members.CanAssignOwner)
}

func TestScopedMembershipRevocationDisabledRoleExpiryAndScopeDenial(t *testing.T) {
	db, s := organizationFixture(t)
	ctx := context.Background()
	o := createTestOrganization(t, s, "first-org")
	other := createTestOrganization(t, s, "second-org")
	_, err := s.ChangeMember(ctx, "admin", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "alice", RoleID: consts.RoleOrgAdmin})
	require.NoError(t, err)
	_, err = s.ChangeMember(ctx, "alice", "org", other.OrgID, "add", model.ScopedMemberInput{UserID: "bob", RoleID: consts.RoleOrgMember})
	require.ErrorIs(t, err, repo.ErrOrganizationForbidden)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("subject_id = ? AND scope_id = ?", "alice", o.OrgID).Update("is_enabled", 0).Error)
	_, err = s.Members(ctx, "alice", "org", o.OrgID)
	require.ErrorIs(t, err, repo.ErrOrganizationForbidden)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("subject_id = ? AND scope_id = ?", "alice", o.OrgID).Updates(map[string]any{"is_enabled": 1, "expires_at": time.Now().Add(-time.Minute)}).Error)
	_, err = s.Members(ctx, "alice", "org", o.OrgID)
	require.ErrorIs(t, err, repo.ErrOrganizationForbidden)
	require.NoError(t, db.Model(&model.Role{}).Where("role_id = ?", consts.RoleOrgMember).Update("is_enabled", 0).Error)
	_, err = s.ChangeMember(ctx, "admin", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "bob", RoleID: consts.RoleOrgMember})
	require.ErrorIs(t, err, repo.ErrOrganizationConflict)
}

func TestScopedMembershipAuditFailureRollsBackAndRoleDriftDenies(t *testing.T) {
	db, s := organizationFixture(t)
	ctx := context.Background()
	o := createTestOrganization(t, s, "atomic-org")
	require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: consts.RoleOrgMember, PermissionID: consts.PermAgentDelete}).Error)
	_, err := s.ChangeMember(ctx, "admin", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "alice", RoleID: consts.RoleOrgMember})
	require.ErrorIs(t, err, repo.ErrOrganizationConflict)
	require.NoError(t, db.Where("role_id = ? AND permission_id = ?", consts.RoleOrgMember, consts.PermAgentDelete).Delete(&model.RolePermissionBinding{}).Error)
	require.NoError(t, db.Migrator().DropTable(&model.AuditLog{}))
	_, err = s.ChangeMember(ctx, "admin", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "alice", RoleID: consts.RoleOrgMember})
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&model.OrganizationMember{}).Where("org_id = ? AND user_id = ?", o.OrgID, "alice").Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("scope_id = ? AND subject_id = ?", o.OrgID, "alice").Count(&count).Error)
	require.Zero(t, count)
}

func TestProjectMembershipLegacyScopesAndOrganizationArchive(t *testing.T) {
	db, s := organizationFixture(t)
	ctx := context.Background()
	o := createTestOrganization(t, s, "project-org")
	projectRepo := repo.NewProjectRepo(organizationTestDB{db})
	p := model.Project{ProjectID: "project", OrgID: o.OrgID, CreatedBy: "admin", Name: "project", Status: model.ProjectStatusActive, IsEnabled: 1}
	require.NoError(t, projectRepo.Create(ctx, &p))
	_, err := s.Archive(ctx, "admin", o.OrgID, o.Version)
	require.ErrorIs(t, err, repo.ErrOrganizationConflict)
	m, err := s.ChangeMember(ctx, "admin", "project", "project", "add", model.ScopedMemberInput{UserID: "bob", RoleID: consts.RoleProjectDeveloper})
	require.NoError(t, err)
	_, err = s.ChangeMember(ctx, "admin", "project", "project", "update", model.ScopedMemberInput{UserID: "bob", RoleID: consts.RoleOrgAdmin, Version: m.Version})
	require.ErrorIs(t, err, repo.ErrOrganizationInvalid)
	_, err = s.ChangeMember(ctx, "admin", "project", "project", "remove", model.ScopedMemberInput{UserID: "bob", Version: m.Version})
	require.NoError(t, err)
	existing, err := repo.NewProjectMemberRepo(organizationTestDB{db}).GetUserProjects(ctx, "bob")
	require.NoError(t, err)
	require.Empty(t, existing)
	require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", "project").Update("status", model.ProjectStatusArchived).Error)
	_, err = s.Archive(ctx, "admin", o.OrgID, o.Version)
	require.NoError(t, err)
	p.ProjectID = "new-project"
	p.ID = 0
	err = projectRepo.Create(ctx, &p)
	require.ErrorIs(t, err, repo.ErrOrganizationConflict)
	legacy := model.Project{ProjectID: "legacy", OrgID: "scope-only", Name: "legacy", IsEnabled: 1, Status: model.ProjectStatusActive}
	require.NoError(t, projectRepo.Create(ctx, &legacy))
	_, err = s.ChangeMember(ctx, "admin", "project", "legacy", "add", model.ScopedMemberInput{UserID: "bob", RoleID: consts.RoleProjectGuest})
	require.NoError(t, err)
	list, err := s.List(ctx, "admin", "", 1, 20)
	require.NoError(t, err)
	require.Empty(t, list.List)
}

func TestScopedMembershipCannotDelegateBeyondActorPermissions(t *testing.T) {
	db, s := organizationFixture(t)
	ctx := context.Background()
	o := createTestOrganization(t, s, "ceiling-org")
	require.NoError(t, db.Create(&model.Role{RoleID: "member-manager-only", Name: "Manager", ScopeType: "org", IsEnabled: 1}).Error)
	require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: "member-manager-only", PermissionID: consts.PermOrgManageMember}).Error)
	require.NoError(t, db.Create(&model.RoleGrant{GrantID: "limited-manager", SubjectType: "user", SubjectID: "alice", RoleID: "member-manager-only", ScopeType: "org", ScopeID: o.OrgID, IsEnabled: 1}).Error)
	members, err := s.Members(ctx, "alice", "org", o.OrgID)
	require.NoError(t, err)
	require.True(t, members.CanManageMembers)
	require.Empty(t, members.AssignableRoleIDs)
	for _, role := range []string{consts.RoleOrgMember, consts.RoleOrgAdmin, consts.RoleOrgOwner} {
		_, err = s.ChangeMember(ctx, "alice", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "alice", RoleID: role})
		require.ErrorIs(t, err, repo.ErrOrganizationForbidden)
	}
}
func TestScopedMembershipRejectsManagedGrantDrift(t *testing.T) {
	db, s := organizationFixture(t)
	ctx := context.Background()
	o := createTestOrganization(t, s, "drift-org")
	m, err := s.ChangeMember(ctx, "admin", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "alice", RoleID: consts.RoleOrgMember})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("scope_id = ? AND subject_id = ?", o.OrgID, "alice").Update("role_id", consts.RoleOrgAdmin).Error)
	_, err = s.ChangeMember(ctx, "admin", "org", o.OrgID, "update", model.ScopedMemberInput{UserID: "alice", RoleID: consts.RoleOrgMember, Version: m.Version})
	require.ErrorIs(t, err, repo.ErrOrganizationConflict)
}

func TestOrganizationSQLiteConcurrentCASAndLastOwner(t *testing.T) {
	db, s := organizationFixture(t)
	ctx := context.Background()
	o := createTestOrganization(t, s, "sqlite-race")
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		safe.Go(func() {
			<-start
			_, err := s.ChangeMember(ctx, "admin", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "alice", RoleID: consts.RoleOrgOwner})
			results <- err
		})
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, repo.ErrOrganizationConflict) {
			conflicts++
		} else {
			require.NoError(t, err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	_, err := s.ChangeMember(ctx, "admin", "org", o.OrgID, "add", model.ScopedMemberInput{UserID: "bob", RoleID: consts.RoleOrgOwner})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("scope_id = ? AND subject_id = ?", o.OrgID, "admin").Update("is_enabled", 0).Error)
	start = make(chan struct{})
	for _, user := range []string{"alice", "bob"} {
		user := user
		safe.Go(func() {
			<-start
			_, err := s.ChangeMember(ctx, "admin", "org", o.OrgID, "remove", model.ScopedMemberInput{UserID: user, Version: 1})
			results <- err
		})
	}
	close(start)
	successes, conflicts = 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, repo.ErrOrganizationConflict) {
			conflicts++
		} else {
			require.NoError(t, err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
}
