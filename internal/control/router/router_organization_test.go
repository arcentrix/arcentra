// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/plugin/dbresolver"
)

func organizationHTTPFixture(t *testing.T) adminFixture {
	f := newFirstBuildFixture(t)
	sessions := &adminSessions{values: map[string]string{}}
	for user, token := range f.tokens {
		raw, err := sonic.Marshal(apphttp.TokenInfo{AccessToken: token})
		require.NoError(t, err)
		sessions.values[consts.UserTokenKey+user] = string(raw)
	}
	f.rt.organizationRouter(f.app, middleware.AuthorizationMiddleware("admin-test-secret", sessions))
	return f
}
func TestOrganizationHTTPUsesAuthenticatedActorStrictInputAndVersions(t *testing.T) {
	f := organizationHTTPFixture(t)
	for _, user := range []string{"", "ordinary", "org-owner"} {
		status, _, _ := f.request(t, user, "POST", "/organization/", `{"name":"team"}`)
		if user == "" {
			require.Equal(t, 401, status)
		} else {
			require.Equal(t, 403, status)
		}
	}
	for _, body := range []string{`{"name":"team","ownerUserId":"other"}`, `{"name":"team","orgId":"existing"}`, `{"name":"team","roleId":"platform_super_admin"}`, `{"name":"team"} {}`, `null`, `{"Name":"team"}`, `{"name":"team","name":"other"}`, `{"name":"team","description":null}`, `{"name":"team","version":null}`, "{\"name\":\"team\",\"description\":\"\xff\"}"} {
		status, _, raw := f.request(t, "admin", "POST", "/organization/", body)
		require.Equal(t, 400, status, raw)
	}
	status, body, raw := f.request(t, "admin", "POST", "/organization/?userId=other", `{"name":"team"}`)
	require.Equal(t, 200, status, raw)
	o := body["detail"].(map[string]any)
	require.Equal(t, "admin", o["ownerUserId"])
	org := o["orgId"].(string)
	status, _, raw = f.request(t, "admin", "POST", "/organization/"+org+"/members", `{"userId":"ordinary","roleId":"org_admin"}`)
	require.Equal(t, 200, status, raw)
	status, _, raw = f.request(t, "ordinary", "POST", "/organization/"+org+"/members", `{"userId":"other","roleId":"org_owner"}`)
	require.Equal(t, 403, status, raw)
	status, _, raw = f.request(t, "admin", "PUT", "/organization/"+org+"/members/ordinary", `{"roleId":"org_member","version":1,"userId":"admin"}`)
	require.Equal(t, 400, status, raw)
	status, _, raw = f.request(t, "admin", "PUT", "/organization/"+org+"/members/ordinary", `{"roleId":"org_member","version":1}`)
	require.Equal(t, 200, status, raw)
	status, _, raw = f.request(t, "admin", "DELETE", "/organization/"+org+"/members/ordinary", `{"version":1}`)
	require.Equal(t, 409, status, raw)
	status, _, raw = f.request(t, "admin", "DELETE", "/organization/"+org+"/members/ordinary", `{"version":2}`)
	require.Equal(t, 200, status, raw)
	status, _, raw = f.request(t, "ordinary", "GET", "/organization/"+org, "")
	require.Equal(t, 403, status, raw)
	var audits []model.AuditLog
	require.NoError(t, f.db.Where("scope_id = ?", org).Find(&audits).Error)
	require.Len(t, audits, 4)
	for _, a := range audits {
		require.Equal(t, "admin", a.ActorUserID)
		require.NotContains(t, string(a.Payload), "password")
	}
}
func TestScopedProjectRoleChangesRefreshOrdinaryRequestAuthority(t *testing.T) {
	f := organizationHTTPFixture(t)
	require.NoError(t, f.db.Create(&model.Project{ProjectID: "scoped-project", OrgID: "legacy", Name: "project", CreatedBy: "admin", IsEnabled: 1, Status: model.ProjectStatusActive}).Error)
	status, _, _ := f.request(t, "ordinary", "GET", "/project/scoped-project", "")
	require.Equal(t, 403, status)
	status, body, raw := f.request(t, "admin", "POST", "/project/scoped-project/members", `{"userId":"ordinary","roleId":"project_reporter"}`)
	require.Equal(t, 200, status, raw)
	require.Equal(t, "project_reporter", body["detail"].(map[string]any)["roleId"])
	status, _, raw = f.request(t, "ordinary", "GET", "/project/scoped-project", "")
	require.Equal(t, 200, status, raw)
	// Deliberately warm the old startup enforcer; it must never authorize later requests.
	require.NoError(t, f.rt.Services.Authorizer.Reload(context.Background()))
	status, _, raw = f.request(t, "admin", "DELETE", "/project/scoped-project/members/ordinary", `{"version":1}`)
	require.Equal(t, 200, status, raw)
	status, _, raw = f.request(t, "ordinary", "GET", "/project/scoped-project", "")
	require.Equal(t, 403, status, raw)
	status, body, raw = f.request(t, "ordinary", "GET", "/project/", "")
	require.Equal(t, 200, status, raw)
	require.Equal(t, float64(0), body["detail"].(map[string]any)["total"])
	status, _, raw = f.request(t, "admin", "POST", "/project/scoped-project/members", `{"userId":"ordinary","roleId":"project_reporter"}`)
	require.Equal(t, 200, status, raw)
	require.NoError(t, f.db.Model(&model.User{}).Where("user_id = ?", "ordinary").Update("is_enabled", 0).Error)
	status, _, raw = f.request(t, "ordinary", "GET", "/project/scoped-project", "")
	require.Equal(t, 403, status, raw)
}

func TestOrganizationHTTPPrimaryReadsBypassEmptyReplica(t *testing.T) {
	f := organizationHTTPFixture(t)
	require.NoError(t, f.db.Use(dbresolver.Register(dbresolver.Config{Replicas: []gorm.Dialector{sqlite.Open(filepath.Join(t.TempDir(), "empty-replica.db"))}})))
	status, body, raw := f.request(t, "admin", "POST", "/organization/", `{"name":"primary"}`)
	require.Equal(t, 200, status, raw)
	id := body["detail"].(map[string]any)["orgId"].(string)
	status, _, raw = f.request(t, "admin", "POST", "/organization/"+id+"/members", `{"userId":"ordinary","roleId":"org_member"}`)
	require.Equal(t, 200, status, raw)
	status, body, raw = f.request(t, "ordinary", "GET", "/organization/", "")
	require.Equal(t, 200, status, raw)
	require.Equal(t, float64(1), body["detail"].(map[string]any)["total"])
	status, _, raw = f.request(t, "admin", "POST", "/project/", `{"orgId":"`+id+`","name":"primary-project","repoUrl":"https://example.invalid/repo.git","repoType":"git"}`)
	require.Equal(t, 200, status, raw)
}

func TestProjectCollectionDoesNotTrustCaseCollidingOrganizationFilter(t *testing.T) {
	f := organizationHTTPFixture(t)
	require.NoError(t, f.db.Create(&model.Project{ProjectID: "exact-project", OrgID: "org-a", Name: "exact", IsEnabled: 1, Status: model.ProjectStatusActive}).Error)
	// SQLite's ordinary text equality is case-sensitive; emulate MySQL ai_ci for
	// the collection predicate only, without altering authorization's byte keys.
	require.NoError(t, f.db.Callback().Query().Before("gorm:query").Register("collection-org-collation", func(tx *gorm.DB) {
		if tx.Statement.Table != "project" {
			return
		}
		if where, ok := tx.Statement.Clauses["WHERE"]; ok {
			if condition, ok := where.Expression.(clause.Where); ok {
				for i, expr := range condition.Exprs {
					if e, ok := expr.(clause.Expr); ok && e.SQL == "org_id = ?" {
						e.SQL = "org_id = ? COLLATE NOCASE"
						condition.Exprs[i] = e
					}
				}
				where.Expression = condition
				tx.Statement.Clauses["WHERE"] = where
			}
		}
	}))
	require.NoError(t, f.db.Create(&model.RoleGrant{GrantID: "upper-only", SubjectType: "user", SubjectID: "ordinary", RoleID: consts.RoleOrgMember, ScopeType: "org", ScopeID: "ORG-A", IsEnabled: 1}).Error)
	status, body, raw := f.request(t, "ordinary", "GET", "/project/?orgId=ORG-A", "")
	require.Equal(t, 200, status, raw)
	require.Equal(t, float64(0), body["detail"].(map[string]any)["total"])
	require.NotContains(t, raw, "exact-project")
	status, body, raw = f.request(t, "admin", "GET", "/project/?orgId=ORG-A", "")
	require.Equal(t, 200, status, raw)
	require.Equal(t, float64(1), body["detail"].(map[string]any)["total"])
}

// The live scoped permission boundary has an explicit HTTP 403/code 403
// contract. A read-only observer must never reach trigger admission.
func TestScopedPipelineObserverTriggerDenialEnvelopeHasNoRunSideEffect(t *testing.T) {
	f := organizationHTTPFixture(t)
	sessions := &adminSessions{values: map[string]string{}}
	for user, token := range f.tokens {
		raw, err := sonic.Marshal(apphttp.TokenInfo{AccessToken: token})
		require.NoError(t, err)
		sessions.values[consts.UserTokenKey+user] = string(raw)
	}
	f.rt.pipelineRouter(f.app, middleware.AuthorizationMiddleware("admin-test-secret", sessions), middleware.SubjectMiddleware(f.rt.Services.Repos().TeamMember))
	require.NoError(t, f.db.Create(&model.Project{ProjectID: "observer-project", OrgID: "legacy", Name: "observer", CreatedBy: "admin", IsEnabled: 1, Status: model.ProjectStatusActive}).Error)
	require.NoError(t, f.db.Create(&model.Pipeline{PipelineID: "observer-pipeline", ProjectID: "observer-project", Name: "observer"}).Error)
	status, _, raw := f.request(t, "admin", "POST", "/project/observer-project/members", `{"userId":"ordinary","roleId":"project_reporter"}`)
	require.Equal(t, 200, status, raw)
	status, _, raw = f.request(t, "ordinary", "GET", "/project/observer-project", "")
	require.Equal(t, 200, status, raw)
	var before, after int64
	require.NoError(t, f.db.Model(&model.PipelineRun{}).Count(&before).Error)
	status, body, raw := f.request(t, "ordinary", "POST", "/pipelines/observer-pipeline/trigger", `{}`)
	require.Equal(t, 403, status, raw)
	require.Equal(t, float64(403), body["code"], raw)
	require.NoError(t, f.db.Model(&model.PipelineRun{}).Count(&after).Error)
	require.Equal(t, before, after)
}
