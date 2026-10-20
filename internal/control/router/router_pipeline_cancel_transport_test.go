// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"encoding/base64"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
)

type registeredCancellationPublisher struct {
	nova.IConsumer
	calls int
}

func (p *registeredCancellationPublisher) EnqueueContext(context.Context, *nova.Task, ...nova.Option) (*nova.Result, error) {
	p.calls++
	return &nova.Result{ID: "existing-control-queue"}, nil
}

// Exercise the actual pipeline route registration with its original authentication,
// subject loading, and scoped pipeline:cancel middleware. The enabled variant is
// compiled only in the explicit sandbox wiring-test build; no runtime is started.
func testRegisteredCancellationRoute(t *testing.T, enabled bool) {
	t.Helper()
	_, rt := collectionRouterFixture(t)
	db := rt.Services.ProjectRepo.(*repo.ProjectRepo).Database()
	repos := repo.NewRepositories(collectionTestDB{db}, nil)
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")))
	runtime := taskqueue.OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowWholeJobRetryApproval: true, AllowedImages: []string{"test/image@sha256:" + strings.Repeat("a", 64)}, MaxCPU: "1", MaxMemory: "256Mi"}
	if enabled {
		for _, key := range []string{"ARCENTRA_CONTAINERD_ACCEPTANCE", "ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", "ARCENTRA_REQUIRE_TERMINAL_RERUN_ACCEPTANCE"} {
			t.Setenv(key, "1")
		}
		t.Setenv("ARCENTRA_SANDBOX_TEST_IMAGE", runtime.AllowedImages[0])
	}
	var err error
	rt.Services, err = service.ProvideServices(collectionTestDB{db}, nil, repos, runtime)
	require.NoError(t, err)
	require.NoError(t, rt.Services.ConfigureRetryApprovalPolicy(&retryapproval.Policy{ID: "cancel-policy", Candidates: []string{"admin"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300}))
	publisher := &registeredCancellationPublisher{}
	require.NoError(t, rt.Services.ConfigureTerminalRerun(enabled, publisher))
	if enabled {
		require.NotNil(t, rt.Services.ProtectedRunCancellation())
		require.NotNil(t, rt.Services.TerminalRerunService())
	} else {
		require.Nil(t, rt.Services.ProtectedRunCancellation())
		require.Nil(t, rt.Services.TerminalRerunService())
	}
	require.Zero(t, publisher.calls)
	require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: "scoped-test", PermissionID: "pipeline:cancel"}).Error)
	require.NoError(t, db.Create(&model.Role{RoleID: "trigger-only", Name: "trigger-only", IsEnabled: 1}).Error)
	require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: "trigger-only", PermissionID: "pipeline:trigger"}).Error)
	require.NoError(t, db.Create(&model.RoleGrant{GrantID: "trigger-only-grant", SubjectType: "user", SubjectID: "outsider", RoleID: "trigger-only", ScopeType: "project", ScopeID: "a", IsEnabled: 1}).Error)
	for _, run := range []model.PipelineRun{
		{RunID: "cancelled-run", PipelineID: "pipe-a", ProjectID: "a", Status: model.PipelineStatusCancelled},
		{RunID: "active-run", PipelineID: "pipe-a", ProjectID: "a", Status: model.PipelineStatusRunning},
		{RunID: "finished-run", PipelineID: "pipe-a", ProjectID: "a", Status: model.PipelineStatusFailed},
		{RunID: "foreign-run", PipelineID: "pipe-b", ProjectID: "b", Status: model.PipelineStatusRunning},
		// A partial protected identity must reach the bounded handler and fail closed,
		// rather than inheriting the legacy already-cancelled success shortcut.
		{RunID: "00000000-0000-4000-8000-000000000001", PipelineID: "pipe-a", ProjectID: "a", Status: model.PipelineStatusCancelled, CopilotRerunIntentID: "00000000-0000-4000-8000-000000000002", CopilotRerunGateID: "00000000-0000-4000-8000-000000000003"},
	} {
		require.NoError(t, db.Create(&run).Error)
	}
	sessions := &adminSessions{values: map[string]string{}}
	rt.Cache = sessions
	authMW := middleware.AuthorizationMiddleware("test-secret", sessions)
	subjectMW := middleware.SubjectMiddleware(rt.Services.TeamMemberRepo)
	app := fiber.New()
	app.Use(middleware.UnifiedResponseMiddleware())
	rt.pipelineRouter(app.Group("/api/v1"), authMW, subjectMW)
	legacy := fiber.New()
	legacy.Use(middleware.UnifiedResponseMiddleware())
	legacy.Post("/api/v1/pipelines/:pipelineID/runs/:runID/stop", authMW, subjectMW, rt.permission("pipeline:cancel", middleware.ResolveFromPathPipelineID("pipelineID", rt)), rt.stopPipeline)
	call := func(app *fiber.App, user, pipeline, run, body, contentType string) (int, map[string]any) {
		t.Helper()
		request := httptest.NewRequest("POST", "/api/v1/pipelines/"+pipeline+"/runs/"+run+"/stop", strings.NewReader(body))
		request.Header.Set("Content-Type", contentType)
		if user != "" {
			token, _, err := jwt.GenToken(user, []byte("test-secret"), time.Hour, time.Hour)
			require.NoError(t, err)
			raw, err := sonic.Marshal(apphttp.TokenInfo{AccessToken: token})
			require.NoError(t, err)
			sessions.values[consts.UserTokenKey+user] = string(raw)
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := app.Test(request, routerTestTimeoutMillis)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		var out map[string]any
		require.NoError(t, sonic.Unmarshal(raw, &out))
		delete(out, "timestamp")
		return response.StatusCode, out
	}
	for _, scenario := range []struct {
		name, user, pipeline, run, body, contentType string
		code                                         int
	}{
		{"cancel-only-success", "project-user", "pipe-a", "cancelled-run", `{"reason":"Cancelled from Build workspace"}`, "application/json", apphttp.Success.Code},
		{"missing", "project-user", "pipe-a", "missing-run", `{}`, "application/json", 404},
		{"cross-pipeline", "project-user", "pipe-a", "foreign-run", `{}`, "application/json", 404},
		{"finished", "project-user", "pipe-a", "finished-run", `{}`, "application/json", 409},
		{"unavailable", "project-user", "pipe-a", "active-run", `{}`, "application/json", 503},
		{"unauthenticated", "", "pipe-a", "active-run", `{}`, "application/json", apphttp.TokenBeEmpty.Code},
		{"trigger-only-denied", "outsider", "pipe-a", "active-run", `{"actor":"project-user"}`, "application/json", 403},
		{"cross-project-denied", "project-user", "pipe-b", "foreign-run", `{}`, "application/json", 403},
		{"empty-body", "project-user", "pipe-a", "cancelled-run", "", "application/json", apphttp.RequestParameterParsingFailed.Code},
		{"ordinary-extra-fields", "project-user", "pipe-a", "cancelled-run", `{"reason":"stop","actor":"ignored"}`, "application/json", apphttp.Success.Code},
		{"ordinary-form", "project-user", "pipe-a", "cancelled-run", "reason=stop", "application/x-www-form-urlencoded", apphttp.Success.Code},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			oldStatus, oldBody := call(legacy, scenario.user, scenario.pipeline, scenario.run, scenario.body, scenario.contentType)
			status, body := call(app, scenario.user, scenario.pipeline, scenario.run, scenario.body, scenario.contentType)
			require.Equal(t, float64(scenario.code), body["code"], body)
			require.Equal(t, oldStatus, status)
			require.Equal(t, oldBody, body)
			require.NotContains(t, body, "detail")
		})
	}
	status, body := call(app, "project-user", "pipe-a", "00000000-0000-4000-8000-000000000001", `{}`, "application/json")
	if enabled {
		require.Equal(t, 403, status)
		require.Equal(t, float64(403), body["code"])
		require.Equal(t, "Cancellation access denied", body["errMsg"])
	} else {
		require.Equal(t, float64(apphttp.Success.Code), body["code"])
	}
	engine := &ordinaryStopCaptureEngine{}
	require.NoError(t, rt.Services.BindPipelineEngine(engine))
	_, body = call(app, "project-user", "pipe-a", "active-run", `{"reason":"Cancelled from Build workspace"}`, "application/json")
	require.Equal(t, float64(apphttp.Success.Code), body["code"])
	require.NotContains(t, body, "detail")
	require.Equal(t, []string{"active-run"}, engine.calls)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "project-grant").Update("is_enabled", 0).Error)
	_, body = call(app, "project-user", "pipe-a", "active-run", `{}`, "application/json")
	require.Equal(t, float64(403), body["code"])
	require.Equal(t, []string{"active-run"}, engine.calls)
	require.Zero(t, publisher.calls)
}
func TestProtectedCancellationRegisteredRouteDefaultOff(t *testing.T) {
	testRegisteredCancellationRoute(t, false)
}
