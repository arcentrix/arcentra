// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func newFirstBuildFixture(t *testing.T) adminFixture {
	t.Helper()
	f := newAdminFixture(t)
	sessions := &adminSessions{values: map[string]string{}}
	for user, token := range f.tokens {
		raw, err := sonic.Marshal(apphttp.TokenInfo{AccessToken: token})
		require.NoError(t, err)
		sessions.values[consts.UserTokenKey+user] = string(raw)
	}
	auth := middleware.AuthorizationMiddleware("admin-test-secret", sessions)
	subject := middleware.SubjectMiddleware(f.rt.Services.Repos().TeamMember)
	f.rt.meRouter(f.app, auth, subject)
	f.rt.projectRouter(f.app, auth, subject)
	return f
}

func firstBuildRead(t *testing.T, f adminFixture, user string) (firstBuildDetail, string) {
	t.Helper()
	status, body, raw := f.request(t, user, "GET", "/me/first-build", "")
	require.Equal(t, 200, status, raw)
	data, err := sonic.Marshal(body["detail"])
	require.NoError(t, err)
	var detail firstBuildDetail
	require.NoError(t, sonic.Unmarshal(data, &detail))
	return detail, raw
}

func addFirstBuildGrant(t *testing.T, f adminFixture, id, kind, subject, role, scope string) {
	t.Helper()
	require.NoError(t, f.db.Create(&model.RoleGrant{
		GrantID: id, SubjectType: kind, SubjectID: subject, RoleID: role,
		ScopeType: "org", ScopeID: scope, IsEnabled: 1,
	}).Error)
}

func TestFirstBuildAnonymousOrdinaryAndBootstrapAdmin(t *testing.T) {
	f := newFirstBuildFixture(t)
	status, _, raw := f.request(t, "", "GET", "/me/first-build", "")
	require.Equal(t, 401, status, raw)
	ordinary, raw := firstBuildRead(t, f, "ordinary")
	require.Empty(t, ordinary.ScopeOptions)
	require.False(t, ordinary.CanEnterScope)
	require.False(t, ordinary.ScopeOptionsTruncated)
	require.Equal(t, firstBuildConfiguration{Access: "restricted"}, ordinary.Configuration)
	require.Equal(t, firstBuildAgent{Access: "restricted"}, ordinary.Agent)
	require.NotContains(t, raw, "defaultStorageConfigured")
	require.NotContains(t, raw, "agentSigningConfigured")
	require.NotContains(t, raw, "enabledRecentlySeen")
	require.Contains(t, raw, `"scopeOptions":[]`)
	admin, _ := firstBuildRead(t, f, "admin")
	require.Empty(t, admin.ScopeOptions)
	require.True(t, admin.CanEnterScope)
	require.Equal(t, "visible", admin.Configuration.Access)
	require.NotNil(t, admin.Configuration.DefaultStorageConfigured)
	require.False(t, *admin.Configuration.DefaultStorageConfigured)
	require.False(t, *admin.Configuration.AgentSigningConfigured)
	require.Equal(t, "visible", admin.Agent.Access)
	require.False(t, *admin.Agent.EnabledRecentlySeen)

	request := httptest.NewRequest("GET", "/me/first-build", nil)
	request.Header.Set("Authorization", "Bearer "+f.tokens["admin"])
	response, err := f.app.Test(request, routerTestTimeoutMillis)
	require.NoError(t, err)
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.Equal(t, "private, no-store", response.Header.Get("Cache-Control"))
}

func TestFirstBuildScopesAreCurrentRoleGrantsNotOrganizationsOrMemberships(t *testing.T) {
	f := newFirstBuildFixture(t)
	addFirstBuildGrant(t, f, "own", "user", "ordinary", consts.RoleOrgOwner, "own-scope")
	addFirstBuildGrant(t, f, "read-only", "user", "ordinary", consts.RoleOrgMember, "read-only-scope")
	addFirstBuildGrant(t, f, "foreign", "user", "other", consts.RoleOrgOwner, "foreign-scope")
	require.NoError(t, f.db.Create(&model.Organization{OrgID: "private-org-canary", Name: "private-org-name", IsEnabled: 1}).Error)
	require.NoError(t, f.db.Create(&model.OrganizationMember{OrgID: "private-org-canary", UserID: "ordinary", Status: 1}).Error)
	require.NoError(t, f.db.Create(&model.Project{ProjectID: "private-project-canary", OrgID: "private-org-canary", Name: "Private", CreatedBy: "ordinary"}).Error)

	before := map[string]int64{}
	for _, table := range []string{"organization", "organization_member", "project", "project_member", "role_grant"} {
		var count int64
		require.NoError(t, f.db.Table(table).Count(&count).Error)
		before[table] = count
	}
	for _, path := range []string{"/me/first-build", "/me/first-build?userId=other&teamId=other&orgId=foreign-scope&scopeId=foreign-scope&scopeType=platform"} {
		status, body, raw := f.request(t, "ordinary", "GET", path, "")
		require.Equal(t, 200, status, raw)
		detail := body["detail"].(map[string]any)
		require.Equal(t, []any{map[string]any{"scopeId": "own-scope", "source": "role_grant"}}, detail["scopeOptions"])
		require.False(t, detail["canEnterScope"].(bool))
		for _, forbidden := range []string{"foreign-scope", "read-only-scope", "private-org", "private-project", "membership", "teamId", "grantId", "roleId"} {
			require.NotContains(t, raw, forbidden)
		}
	}
	for table, count := range before {
		var after int64
		require.NoError(t, f.db.Table(table).Count(&after).Error)
		require.Equal(t, count, after, table)
	}
	for _, user := range []string{"project-owner", "org-owner"} {
		detail, _ := firstBuildRead(t, f, user)
		require.Equal(t, "restricted", detail.Configuration.Access)
		require.Equal(t, "restricted", detail.Agent.Access)
		require.False(t, detail.CanEnterScope)
		if user == "project-owner" {
			require.Empty(t, detail.ScopeOptions)
		} else {
			require.Equal(t, []firstBuildScopeOption{{ScopeID: "foreign", Source: "role_grant"}}, detail.ScopeOptions)
		}
	}
}

func TestFirstBuildRefreshExcludesExpiredRevokedDisabledAndDeletedGrants(t *testing.T) {
	for _, mutation := range []string{"revoked", "expired", "role-disabled", "grant-deleted", "user-disabled", "user-deleted"} {
		t.Run(mutation, func(t *testing.T) {
			f := newFirstBuildFixture(t)
			addFirstBuildGrant(t, f, "current", "user", "ordinary", consts.RoleOrgOwner, "scope")
			detail, _ := firstBuildRead(t, f, "ordinary")
			require.Len(t, detail.ScopeOptions, 1)
			switch mutation {
			case "revoked":
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "current").Update("is_enabled", 0).Error)
			case "expired":
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "current").Update("expires_at", time.Now().Add(-time.Second)).Error)
			case "role-disabled":
				require.NoError(t, f.db.Model(&model.Role{}).Where("role_id = ?", consts.RoleOrgOwner).Update("is_enabled", 0).Error)
			case "grant-deleted":
				require.NoError(t, f.db.Where("grant_id = ?", "current").Delete(&model.RoleGrant{}).Error)
			case "user-disabled":
				require.NoError(t, f.db.Model(&model.User{}).Where("user_id = ?", "ordinary").Update("is_enabled", 0).Error)
			case "user-deleted":
				require.NoError(t, f.db.Where("user_id = ?", "ordinary").Delete(&model.User{}).Error)
			}
			if mutation == "user-disabled" || mutation == "user-deleted" {
				status, _, _ := f.request(t, "ordinary", "GET", "/me/first-build", "")
				require.Equal(t, 403, status)
			} else {
				detail, _ = firstBuildRead(t, f, "ordinary")
				require.Empty(t, detail.ScopeOptions)
			}
		})
	}
}

func TestFirstBuildDisabledRoleNeverCreatesCandidateForPlatformAdmin(t *testing.T) {
	f := newFirstBuildFixture(t)
	addFirstBuildGrant(t, f, "disabled-candidate", "user", "admin", consts.RoleOrgOwner, "stale-scope")
	require.NoError(t, f.db.Model(&model.Role{}).Where("role_id = ?", consts.RoleOrgOwner).Update("is_enabled", 0).Error)
	detail, _ := firstBuildRead(t, f, "admin")
	require.True(t, detail.CanEnterScope)
	require.Empty(t, detail.ScopeOptions)
}

func TestFirstBuildTeamSubjectsAreLiveAndDeduplicated(t *testing.T) {
	f := newFirstBuildFixture(t)
	require.NoError(t, f.db.Create(&model.Team{TeamID: "team", Name: "Team", IsEnabled: 1}).Error)
	require.NoError(t, f.db.Create(&model.TeamMember{TeamID: "team", UserID: "ordinary"}).Error)
	addFirstBuildGrant(t, f, "team-shared", "team", "team", consts.RoleOrgOwner, "shared")
	addFirstBuildGrant(t, f, "direct-shared", "user", "ordinary", consts.RoleOrgOwner, "shared")
	addFirstBuildGrant(t, f, "team-only", "team", "team", consts.RoleOrgOwner, "team-only")
	detail, raw := firstBuildRead(t, f, "ordinary")
	require.Equal(t, []firstBuildScopeOption{{ScopeID: "shared", Source: "role_grant"}, {ScopeID: "team-only", Source: "role_grant"}}, detail.ScopeOptions)
	require.NotContains(t, raw, "teamId")
	require.NoError(t, f.db.Model(&model.Team{}).Where("team_id = ?", "team").Update("is_enabled", 0).Error)
	detail, _ = firstBuildRead(t, f, "ordinary")
	require.Equal(t, []firstBuildScopeOption{{ScopeID: "shared", Source: "role_grant"}}, detail.ScopeOptions)
	require.NoError(t, f.db.Model(&model.Team{}).Where("team_id = ?", "team").Update("is_enabled", 1).Error)
	require.NoError(t, f.db.Where("user_id = ?", "ordinary").Delete(&model.TeamMember{}).Error)
	detail, _ = firstBuildRead(t, f, "ordinary")
	require.Equal(t, []firstBuildScopeOption{{ScopeID: "shared", Source: "role_grant"}}, detail.ScopeOptions)
}

func TestFirstBuildPermissionsGateConfigurationAndAgentIndependently(t *testing.T) {
	f := newFirstBuildFixture(t)
	for _, item := range []struct{ role, permission, user string }{{"config-reader", consts.PermOrgUpdate, "ordinary"}, {"agent-reader", consts.PermAgentRead, "other"}} {
		require.NoError(t, f.db.Create(&model.Role{RoleID: item.role, Name: item.role, IsEnabled: 1}).Error)
		require.NoError(t, f.db.Create(&model.RolePermissionBinding{RoleID: item.role, PermissionID: item.permission}).Error)
		require.NoError(t, f.db.Create(&model.RoleGrant{GrantID: item.role, SubjectType: "user", SubjectID: item.user, RoleID: item.role, ScopeType: "platform", IsEnabled: 1}).Error)
	}
	detail, raw := firstBuildRead(t, f, "ordinary")
	require.Equal(t, "visible", detail.Configuration.Access)
	require.Equal(t, "restricted", detail.Agent.Access)
	require.NotContains(t, raw, "enabledRecentlySeen")
	detail, raw = firstBuildRead(t, f, "other")
	require.Equal(t, "restricted", detail.Configuration.Access)
	require.Equal(t, "visible", detail.Agent.Access)
	require.NotContains(t, raw, "agentSigningConfigured")
}

func TestFirstBuildConfigurationRefreshIsSecretFreeAndNotReadiness(t *testing.T) {
	f := newFirstBuildFixture(t)
	before, _ := firstBuildRead(t, f, "admin")
	require.False(t, *before.Configuration.DefaultStorageConfigured)
	require.False(t, *before.Configuration.AgentSigningConfigured)
	require.NoError(t, f.db.Create(&model.StorageConfig{StorageID: "storage-id-canary", Name: "storage-name-canary", IsDefault: 1, IsEnabled: 1,
		Config: datatypes.JSON(`{"endpoint":"endpoint-canary","accessKey":"access-canary","secretKey":"storage-secret-canary"}`)}).Error)
	require.NoError(t, f.db.Create(&model.Setting{Name: consts.SettingNameAgentSecretKey,
		Value: datatypes.JSON(`{"secret_key":"signing-key-canary","salt":"signing-salt-canary"}`)}).Error)
	now := time.Now()
	require.NoError(t, f.db.Create(&model.Agent{AgentID: "agent-id-canary", AgentName: "agent-name-canary", Address: "agent-address-canary", IsEnabled: 1, Status: 4, LastHeartbeat: &now}).Error)
	detail, raw := firstBuildRead(t, f, "admin")
	require.True(t, *detail.Configuration.DefaultStorageConfigured)
	require.True(t, *detail.Configuration.AgentSigningConfigured)
	require.True(t, *detail.Agent.EnabledRecentlySeen)
	for _, forbidden := range []string{"canary", "endpoint", "secret_key", "salt", "config\"", "agentId", "storageId", "count", "ready", "connectivity"} {
		require.NotContains(t, raw, forbidden)
	}
	require.NoError(t, f.db.Model(&model.StorageConfig{}).Where("storage_id = ?", "storage-id-canary").Update("is_enabled", 0).Error)
	require.NoError(t, f.db.Model(&model.Setting{}).Where("name = ?", consts.SettingNameAgentSecretKey).Update("value", datatypes.JSON(`{"secret_key":"signing-key-canary"}`)).Error)
	require.NoError(t, f.db.Model(&model.Agent{}).Where("agent_id = ?", "agent-id-canary").Update("last_heartbeat", now.Add(-3*time.Minute)).Error)
	detail, _ = firstBuildRead(t, f, "admin")
	require.False(t, *detail.Configuration.DefaultStorageConfigured)
	require.False(t, *detail.Configuration.AgentSigningConfigured)
	require.False(t, *detail.Agent.EnabledRecentlySeen)
}

func TestFirstBuildErrorsAreSanitizedAndRestrictedDataIsNotQueried(t *testing.T) {
	for _, table := range []string{"role_grant", "role", "permission", "storage_config", "setting", "agent"} {
		t.Run(table, func(t *testing.T) {
			f := newFirstBuildFixture(t)
			require.NoError(t, f.db.Callback().Query().Before("gorm:query").Register("first-build-fail", func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					tx.AddError(errors.New("sql-secret-endpoint-canary"))
				}
			}))
			status, _, raw := f.request(t, "admin", "GET", "/me/first-build", "")
			require.Equal(t, 503, status, raw)
			require.NotContains(t, raw, "canary")
			require.NotContains(t, raw, "scopeOptions")
			if table == "storage_config" || table == "setting" || table == "agent" {
				status, _, raw = f.request(t, "ordinary", "GET", "/me/first-build", "")
				require.Equal(t, 200, status, raw)
			}
		})
	}
}

func TestFirstBuildBoundedCandidatesFilterSubjectsBeforeLimit(t *testing.T) {
	f := newFirstBuildFixture(t)
	for i := range repo.FirstBuildScopeLimit + 1 {
		addFirstBuildGrant(t, f, fmt.Sprintf("foreign-%03d", i), "user", "other", consts.RoleOrgOwner, fmt.Sprintf("a-foreign-%03d", i))
		addFirstBuildGrant(t, f, fmt.Sprintf("own-%03d", i), "user", "ordinary", consts.RoleOrgOwner, fmt.Sprintf("z-own-%03d", i))
	}
	detail, raw := firstBuildRead(t, f, "ordinary")
	require.True(t, detail.ScopeOptionsTruncated)
	require.Len(t, detail.ScopeOptions, repo.FirstBuildScopeLimit)
	require.Equal(t, "z-own-000", detail.ScopeOptions[0].ScopeID)
	require.Equal(t, "z-own-099", detail.ScopeOptions[99].ScopeID)
	require.NotContains(t, raw, "foreign")
}

func TestFirstBuildTruncationDoesNotClaimMissingCreatePermission(t *testing.T) {
	f := newFirstBuildFixture(t)
	for i := range repo.FirstBuildScopeLimit {
		addFirstBuildGrant(t, f, fmt.Sprintf("read-%03d", i), "user", "ordinary", consts.RoleOrgMember, fmt.Sprintf("a-read-%03d", i))
	}
	addFirstBuildGrant(t, f, "late-create", "user", "ordinary", consts.RoleOrgOwner, "z-creatable")
	detail, _ := firstBuildRead(t, f, "ordinary")
	require.True(t, detail.ScopeOptionsTruncated)
	require.Empty(t, detail.ScopeOptions)
	checker, err := authz.NewLiveAuthorizer(context.Background(), f.rt.Services.Repos())
	require.NoError(t, err)
	allowed, err := checker.Check(context.Background(), authz.Subject{UserID: "ordinary"}, consts.PermOrgCreateProject, authz.ResourceRef{OrgID: "z-creatable"})
	require.NoError(t, err)
	require.True(t, allowed)
}
