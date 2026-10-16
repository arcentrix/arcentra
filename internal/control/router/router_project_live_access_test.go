// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

const firstBuildProjectBody = `{"orgId":"scope","name":"first-build","repoUrl":"https://example.invalid/source.git","repoType":"git","createdBy":"admin","userId":"admin","teamId":"spoofed"}`

func TestFirstBuildProjectCreateRejectsStaleCachedAuthority(t *testing.T) {
	for _, mutation := range []string{"revoked", "expired", "disabled-role", "disabled-user", "deleted-user", "foreign-scope", "removed-team", "disabled-team"} {
		t.Run(mutation, func(t *testing.T) {
			f := newFirstBuildFixture(t)
			team := mutation == "removed-team" || mutation == "disabled-team"
			if team {
				require.NoError(t, f.db.Create(&model.Team{TeamID: "team", Name: "Team", IsEnabled: 1}).Error)
				require.NoError(t, f.db.Create(&model.TeamMember{TeamID: "team", UserID: "ordinary"}).Error)
				addFirstBuildGrant(t, f, "create", "team", "team", consts.RoleOrgOwner, "scope")
			} else {
				addFirstBuildGrant(t, f, "create", "user", "ordinary", consts.RoleOrgOwner, "scope")
			}
			// The preexisting authorizer is intentionally warmed and never reloaded
			// after mutation. Its stale permission must not authorize the write.
			require.NoError(t, f.rt.Services.Authorizer.Reload(context.Background()))
			subject := authz.Subject{UserID: "ordinary"}
			if team {
				subject.TeamIDs = []string{"team"}
			}
			allowed, err := f.rt.Services.Authorizer.Check(context.Background(), subject, consts.PermOrgCreateProject, authz.ResourceRef{OrgID: "scope"})
			require.NoError(t, err)
			require.True(t, allowed)
			switch mutation {
			case "revoked":
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "create").Update("is_enabled", 0).Error)
			case "expired":
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "create").Update("expires_at", time.Now().Add(-time.Second)).Error)
			case "disabled-role":
				require.NoError(t, f.db.Model(&model.Role{}).Where("role_id = ?", consts.RoleOrgOwner).Update("is_enabled", 0).Error)
			case "disabled-user":
				require.NoError(t, f.db.Model(&model.User{}).Where("user_id = ?", "ordinary").Update("is_enabled", 0).Error)
			case "deleted-user":
				require.NoError(t, f.db.Where("user_id = ?", "ordinary").Delete(&model.User{}).Error)
			case "foreign-scope":
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "create").Update("scope_id", "foreign").Error)
			case "removed-team":
				require.NoError(t, f.db.Where("team_id = ?", "team").Delete(&model.TeamMember{}).Error)
			case "disabled-team":
				require.NoError(t, f.db.Model(&model.Team{}).Where("team_id = ?", "team").Update("is_enabled", 0).Error)
			}
			allowed, err = f.rt.Services.Authorizer.Check(context.Background(), subject, consts.PermOrgCreateProject, authz.ResourceRef{OrgID: "scope"})
			require.NoError(t, err)
			require.True(t, allowed, "fixture must retain stale cached authority")
			status, _, raw := f.request(t, "ordinary", "POST", "/project/?orgId=scope&userId=admin&teamId=team", firstBuildProjectBody)
			require.Equal(t, 403, status, raw)
			var count int64
			require.NoError(t, f.db.Model(&model.Project{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestFirstBuildProjectCreateAllowsCurrentScopeAndAuthenticatedCreator(t *testing.T) {
	for _, actor := range []string{"ordinary", "admin"} {
		t.Run(actor, func(t *testing.T) {
			f := newFirstBuildFixture(t)
			if actor == "ordinary" {
				addFirstBuildGrant(t, f, "create", "user", actor, consts.RoleOrgOwner, "scope")
			}
			var before int64
			require.NoError(t, f.db.Model(&model.RoleGrant{}).Count(&before).Error)
			status, body, raw := f.request(t, actor, "POST", "/project/", firstBuildProjectBody)
			require.Equal(t, 200, status, raw)
			detail := body["detail"].(map[string]any)
			require.Equal(t, "scope", detail["orgId"])
			require.Equal(t, actor, detail["createdBy"])
			var after int64
			require.NoError(t, f.db.Model(&model.RoleGrant{}).Count(&after).Error)
			require.Equal(t, before, after)
			for _, table := range []string{"organization", "organization_member", "project_member"} {
				require.NoError(t, f.db.Table(table).Count(&after).Error)
				require.Zero(t, after, table)
			}
		})
	}
}

func TestFirstBuildProjectCreateAuthenticationTargetAndAuthorizationErrors(t *testing.T) {
	f := newFirstBuildFixture(t)
	status, _, raw := f.request(t, "", "POST", "/project/", firstBuildProjectBody)
	require.Equal(t, 401, status, raw)
	status, _, raw = f.request(t, "ordinary", "POST", "/project/", firstBuildProjectBody)
	require.Equal(t, 403, status, raw)
	for _, body := range []string{`{}`, `{"orgId":"   "}`, `{"orgId":1}`, `{"orgId":`} {
		status, _, raw = f.request(t, "admin", "POST", "/project/", body)
		require.Equal(t, 400, status, raw)
	}
	require.NoError(t, f.db.Callback().Query().Before("gorm:query").Register("project-live-fault", func(tx *gorm.DB) {
		if tx.Statement.Table == "role_grant" {
			tx.AddError(errors.New("secret-database-canary"))
		}
	}))
	status, _, raw = f.request(t, "admin", "POST", "/project/", firstBuildProjectBody)
	require.Equal(t, 503, status, raw)
	require.NotContains(t, raw, "canary")
}

func TestFirstBuildPrimaryAuthorizationSucceedsWithEmptyReplica(t *testing.T) {
	f := newFirstBuildFixture(t)
	require.NoError(t, f.db.Use(dbresolver.Register(dbresolver.Config{
		Replicas: []gorm.Dialector{sqlite.Open(filepath.Join(t.TempDir(), "empty-replica.db"))},
	})))
	detail, _ := firstBuildRead(t, f, "admin")
	require.True(t, detail.CanEnterScope)
	require.Equal(t, "visible", detail.Configuration.Access)
	// A validation-only request proves POST authorization reaches its service
	// without involving existing project persistence reads outside this change.
	status, _, raw := f.request(t, "admin", "POST", "/project/", `{"orgId":"scope","name":"x"}`)
	require.Equal(t, 200, status, raw)
	require.Contains(t, raw, "project name must contain")
	status, _, raw = f.request(t, "ordinary", "POST", "/project/", firstBuildProjectBody)
	require.Equal(t, 403, status, raw)
}

func TestFirstBuildPrimaryAuthorizationRejectsStaleReplicaAuthority(t *testing.T) {
	for _, mutation := range []string{"revoked-grant", "expired-grant", "disabled-role", "removed-team", "disabled-team", "disabled-user"} {
		t.Run(mutation, func(t *testing.T) {
			f := newFirstBuildFixture(t)
			replica := newFirstBuildFixture(t)
			actor := "admin"
			team := mutation == "removed-team" || mutation == "disabled-team"
			if team {
				actor = "ordinary"
				for _, fixture := range []adminFixture{f, replica} {
					require.NoError(t, fixture.db.Create(&model.Team{TeamID: "team", Name: "Team", IsEnabled: 1}).Error)
					require.NoError(t, fixture.db.Create(&model.TeamMember{TeamID: "team", UserID: actor}).Error)
					require.NoError(t, fixture.db.Create(&model.RoleGrant{GrantID: "team-platform", SubjectType: "team", SubjectID: "team", RoleID: consts.RolePlatformSuperAdmin, ScopeType: "platform", IsEnabled: 1}).Error)
				}
			}
			require.NoError(t, f.db.Use(dbresolver.Register(dbresolver.Config{
				Replicas: []gorm.Dialector{sqlite.Open(replica.db.Dialector.(*sqlite.Dialector).DSN)},
			})))
			detail, _ := firstBuildRead(t, f, actor)
			require.True(t, detail.CanEnterScope)
			switch mutation {
			case "revoked-grant":
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("subject_id = ?", actor).Update("is_enabled", 0).Error)
			case "expired-grant":
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("subject_id = ?", actor).Update("expires_at", time.Now().Add(-time.Second)).Error)
			case "disabled-role":
				require.NoError(t, f.db.Model(&model.Role{}).Where("role_id = ?", consts.RolePlatformSuperAdmin).Update("is_enabled", 0).Error)
			case "removed-team":
				require.NoError(t, f.db.Where("team_id = ?", "team").Delete(&model.TeamMember{}).Error)
			case "disabled-team":
				require.NoError(t, f.db.Model(&model.Team{}).Where("team_id = ?", "team").Update("is_enabled", 0).Error)
			case "disabled-user":
				require.NoError(t, f.db.Model(&model.User{}).Where("user_id = ?", actor).Update("is_enabled", 0).Error)
			}
			// Prove the default replica really remains stale and permits this actor.
			staleSubject, err := currentApprovalSubject(context.Background(), f.rt.Services.Repos(), actor)
			require.NoError(t, err)
			staleChecker, err := authz.NewLiveAuthorizer(context.Background(), f.rt.Services.Repos())
			require.NoError(t, err)
			allowed, err := staleChecker.Check(context.Background(), staleSubject, consts.PermOrgCreateProject, authz.ResourceRef{OrgID: "scope"})
			require.NoError(t, err)
			require.True(t, allowed, "replica must retain revoked primary authority")
			if mutation == "disabled-user" {
				status, _, raw := f.request(t, actor, "GET", "/me/first-build", "")
				require.Equal(t, 403, status, raw)
			} else {
				detail, _ = firstBuildRead(t, f, actor)
				require.False(t, detail.CanEnterScope)
				require.Equal(t, "restricted", detail.Configuration.Access)
				require.Equal(t, "restricted", detail.Agent.Access)
			}
			status, _, raw := f.request(t, actor, "POST", "/project/", firstBuildProjectBody)
			require.Equal(t, 403, status, raw)
		})
	}
}

func TestFirstBuildGetNeverWritesAndProjectDenialRetainsPrimaryAudit(t *testing.T) {
	f := newFirstBuildFixture(t)
	writes := 0
	blockWrite := func(tx *gorm.DB) {
		writes++
		tx.AddError(errors.New("read-only GET attempted mutation"))
	}
	require.NoError(t, f.db.Callback().Create().Before("gorm:create").Register("first-build-read-only", blockWrite))
	require.NoError(t, f.db.Callback().Update().Before("gorm:update").Register("first-build-read-only", blockWrite))
	require.NoError(t, f.db.Callback().Delete().Before("gorm:delete").Register("first-build-read-only", blockWrite))
	firstBuildRead(t, f, "ordinary")
	firstBuildRead(t, f, "admin")
	require.Zero(t, writes, "GET may not even attempt audit or business-data writes")
	require.NoError(t, f.db.Callback().Create().Remove("first-build-read-only"))
	require.NoError(t, f.db.Callback().Update().Remove("first-build-read-only"))
	require.NoError(t, f.db.Callback().Delete().Remove("first-build-read-only"))
	addFirstBuildGrant(t, f, "revoked-for-audit", "user", "ordinary", consts.RoleOrgOwner, "scope")
	require.NoError(t, f.rt.Services.Authorizer.Reload(context.Background()))
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "revoked-for-audit").Update("is_enabled", 0).Error)
	require.NoError(t, f.db.Use(dbresolver.Register(dbresolver.Config{
		Replicas: []gorm.Dialector{sqlite.Open(filepath.Join(t.TempDir(), "empty-audit-replica.db"))},
	})))
	status, _, raw := f.request(t, "ordinary", "POST", "/project/?userId=admin&orgId=spoofed", firstBuildProjectBody)
	require.Equal(t, 403, status, raw)
	var entries []model.AuditLog
	require.NoError(t, f.db.Clauses(dbresolver.Write).Find(&entries).Error)
	require.Len(t, entries, 1)
	require.Equal(t, "ordinary", entries[0].ActorUserID)
	require.Equal(t, consts.PermOrgCreateProject, entries[0].Action)
	require.Equal(t, "org", entries[0].ScopeType)
	require.Equal(t, "scope", entries[0].ScopeID)
	require.Equal(t, model.AuditResultDenied, entries[0].Result)
	// Existing best-effort audit semantics are preserved: sink failures never
	// change a denied operation into an allowed operation or expose raw errors.
	require.NoError(t, f.db.Callback().Create().Before("gorm:create").Register("first-build-audit-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_log" {
			tx.AddError(errors.New("audit-secret-canary"))
		}
	}))
	status, _, raw = f.request(t, "ordinary", "POST", "/project/", firstBuildProjectBody)
	require.Equal(t, 403, status, raw)
	require.NotContains(t, raw, "canary")
}
