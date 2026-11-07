// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
)

// TestCopilotHTTPScopedMemberRemovalRevokesContext 验证真实成员删除路由同步撤销 Copilot 状态及上下文读取
func TestCopilotHTTPScopedMemberRemovalRevokesContext(t *testing.T) {
	f := organizationHTTPFixture(t)
	sessions := &adminSessions{values: map[string]string{}}
	for user, token := range f.tokens {
		raw, err := sonic.Marshal(apphttp.TokenInfo{AccessToken: token})
		require.NoError(t, err)
		sessions.values[consts.UserTokenKey+user] = string(raw)
	}
	f.rt.copilotRouter(f.app, middleware.AuthorizationMiddleware("admin-test-secret", sessions), middleware.SubjectMiddleware(f.rt.Services.Repos().TeamMember))
	for _, row := range []any{
		&model.Project{ProjectID: "scoped-project", OrgID: "legacy", Name: "project", CreatedBy: "admin", IsEnabled: 1, Status: model.ProjectStatusActive},
		&model.Project{ProjectID: "foreign-project", OrgID: "legacy", Name: "foreign", CreatedBy: "admin", IsEnabled: 1, Status: model.ProjectStatusActive},
		&model.Pipeline{PipelineID: "review-pipeline", ProjectID: "scoped-project", IsEnabled: 1},
		&model.PipelineRun{RunID: "review-run", PipelineID: "review-pipeline"},
		&model.StepRun{StepRunID: "review-step", PipelineID: "review-pipeline", PipelineRunID: "review-run", Name: "private-step-canary"},
	} {
		require.NoError(t, f.db.Create(row).Error)
	}
	status, _, raw := f.request(t, "admin", "POST", "/project/scoped-project/members", `{"userId":"ordinary","roleId":"project_reporter"}`)
	require.Equal(t, 200, status, raw)
	// A newly granted actor can read without relying on a separate cached reload.
	status, _, raw = f.request(t, "ordinary", "GET", "/projects/scoped-project/copilot/status", "")
	require.Equal(t, 200, status, raw)
	status, _, raw = f.request(t, "ordinary", "GET", "/projects/scoped-project/copilot/runs/review-run/context", "")
	require.Equal(t, 200, status, raw)
	require.Contains(t, raw, "private-step-canary")
	status, _, raw = f.request(t, "ordinary", "GET", "/projects/foreign-project/copilot/status", "")
	require.Equal(t, 403, status, raw)
	// Warm the old generic enforcer so the regression proves that its stale
	// allow cannot expose content after the supported transactional removal.
	require.NoError(t, f.rt.Services.Authorizer.Reload(context.Background()))
	status, _, raw = f.request(t, "admin", "DELETE", "/project/scoped-project/members/ordinary", `{"version":1}`)
	require.Equal(t, 200, status, raw)
	for _, path := range []string{
		"/project/scoped-project",
		"/projects/scoped-project/copilot/status",
		"/projects/scoped-project/copilot/runs/review-run/context",
	} {
		status, _, raw = f.request(t, "ordinary", "GET", path, "")
		require.Equal(t, 403, status, raw)
		require.NotContains(t, raw, "private-step-canary")
	}
}
