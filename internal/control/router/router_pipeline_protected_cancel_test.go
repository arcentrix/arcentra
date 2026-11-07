// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
)

type protectedStopCapture struct {
	subject authz.Subject
	request *pipelinev1.StopPipelineRequest
	calls   int
	err     error
}

func (s *protectedStopCapture) StopPipelineAs(_ context.Context, subject authz.Subject, in *pipelinev1.StopPipelineRequest, _ *service.ProtectedRunCancellationService, body ...service.PipelineStopBodyValidation) (service.PipelineRunStopReceipt, error) {
	if len(body) > 0 && !body[0].ProtectedValid {
		return service.PipelineRunStopReceipt{}, repo.ErrCopilotRerunInvalid
	}
	s.calls++
	s.subject = subject
	s.request = in
	return service.PipelineRunStopReceipt{RunID: in.RunId, JobRunID: "fixed-job", Protected: true, State: "cancellation_recorded", ControllerRequested: true, NotificationStatus: "unknown"}, s.err
}
func TestProtectedPipelineStopHandlerAuthenticatedSubjectAndStaticErrors(t *testing.T) {
	_, rt := collectionRouterFixture(t)
	db := rt.Services.ProjectRepo.(*repo.ProjectRepo).Database()
	repos := repo.NewRepositories(collectionTestDB{db}, nil)
	svc, err := service.NewProtectedRunCancellationService(repos, nil)
	require.NoError(t, err)
	captured := &protectedStopCapture{}
	sessions := &adminSessions{values: map[string]string{}}
	app := fiber.New()
	app.Use(middleware.UnifiedResponseMiddleware())
	// Explicit test-only installation; production registration remains unchanged.
	app.Post("/pipelines/:pipelineID/runs/:runID/stop", copilotHTTPStatus, middleware.AuthorizationMiddleware("test-secret", sessions), middleware.SubjectMiddleware(rt.Services.TeamMemberRepo), ProtectedPipelineStopHandler(captured, svc))
	call := func(user, body string, status int) string {
		t.Helper()
		req := httptest.NewRequest("POST", "/pipelines/pipe-a/runs/run-a/stop", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if user != "" {
			token, _, err := jwt.GenToken(user, []byte("test-secret"), time.Hour, time.Hour)
			require.NoError(t, err)
			raw, err := sonic.Marshal(apphttp.TokenInfo{AccessToken: token})
			require.NoError(t, err)
			sessions.values[consts.UserTokenKey+user] = string(raw)
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := app.Test(req, routerTestTimeoutMillis)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, status, response.StatusCode, string(raw))
		require.Equal(t, "private, no-store", response.Header.Get("Cache-Control"))
		return string(raw)
	}
	call("", `{"reason":"stop"}`, 401)
	require.Zero(t, captured.calls)
	for _, body := range []string{`{"reason":"stop","triggeredBy":"admin"}`, `{"actor":"admin"}`, `{"ownerUserId":"admin"}`, `{"claimId":"different"}`, `{} {}`, strings.Repeat("x", 2049)} {
		call("project-user", body, 400)
	}
	require.Zero(t, captured.calls)
	raw := call("project-user", `{"reason":"requested stop"}`, 200)
	require.Equal(t, 1, captured.calls)
	require.Equal(t, "project-user", captured.subject.UserID)
	require.Equal(t, "pipe-a", captured.request.PipelineId)
	require.Equal(t, "run-a", captured.request.RunId)
	require.Equal(t, "requested stop", captured.request.Reason)
	require.Contains(t, raw, `"notificationStatus":"unknown"`)
	require.Contains(t, raw, `"agentTerminalAcknowledged":false`)
	for _, scenario := range []struct {
		err     error
		code    int
		message string
	}{{repo.ErrCopilotRerunForbidden, 403, "Cancellation access denied"}, {repo.ErrCopilotRerunConflict, 409, "Cancellation state changed; inspect the existing run"}, {errors.New("private SQL, ciphertext, token"), 503, "Cancellation outcome is unknown; inspect the existing run"}} {
		captured.err = scenario.err
		raw = call("project-user", `{}`, scenario.code)
		require.Contains(t, raw, scenario.message)
		require.NotContains(t, raw, "private SQL")
		require.NotContains(t, raw, "ciphertext")
	}
}
func TestProtectedPipelineStopHandlerRejectsClaimsWithoutSubject(t *testing.T) {
	app := fiber.New()
	app.Use(middleware.UnifiedResponseMiddleware())
	called := &protectedStopCapture{}
	app.Post("/stop", func(c *fiber.Ctx) error { c.Locals("claims", &jwt.AuthClaims{UserID: "admin"}); return c.Next() }, ProtectedPipelineStopHandler(called, &service.ProtectedRunCancellationService{}))
	response, err := app.Test(httptest.NewRequest("POST", "/stop", strings.NewReader(`{}`)), routerTestTimeoutMillis)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, 403, response.StatusCode)
	require.Zero(t, called.calls)
}

type ordinaryStopCaptureEngine struct {
	service.IPipelineEngine
	calls []string
}

func (e *ordinaryStopCaptureEngine) CancelRun(id string) error {
	e.calls = append(e.calls, id)
	return nil
}

func TestProtectedPipelineStopPreservesOrdinaryWireAndBodyContract(t *testing.T) {
	_, rt := collectionRouterFixture(t)
	db := rt.Services.ProjectRepo.(*repo.ProjectRepo).Database()
	require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: "scoped-test", PermissionID: "pipeline:cancel"}).Error)
	for _, run := range []model.PipelineRun{
		{RunID: "cancelled-run", PipelineID: "pipe-a", ProjectID: "a", Status: model.PipelineStatusCancelled},
		{RunID: "finished-run", PipelineID: "pipe-a", ProjectID: "a", Status: model.PipelineStatusFailed},
		{RunID: "active-run", PipelineID: "pipe-a", ProjectID: "a", Status: model.PipelineStatusRunning},
		{RunID: "foreign-run", PipelineID: "pipe-b", ProjectID: "b", Status: model.PipelineStatusRunning},
	} {
		require.NoError(t, db.Create(&run).Error)
	}
	protected, err := service.NewProtectedRunCancellationService(repo.NewRepositories(collectionTestDB{db}, nil), nil)
	require.NoError(t, err)
	sessions := &adminSessions{values: map[string]string{}}
	rt.Cache = sessions
	token, _, err := jwt.GenToken("project-user", []byte("test-secret"), time.Hour, time.Hour)
	require.NoError(t, err)
	raw, err := sonic.Marshal(apphttp.TokenInfo{AccessToken: token})
	require.NoError(t, err)
	sessions.values[consts.UserTokenKey+"project-user"] = string(raw)
	makeApp := func(handler fiber.Handler) *fiber.App {
		app := fiber.New()
		app.Use(middleware.UnifiedResponseMiddleware())
		app.Post("/pipelines/:pipelineID/runs/:runID/stop", middleware.AuthorizationMiddleware("test-secret", sessions), middleware.SubjectMiddleware(rt.Services.TeamMemberRepo), rt.permission("pipeline:cancel", middleware.ResolveFromPathPipelineID("pipelineID", rt)), handler)
		return app
	}
	oldApp, newApp := makeApp(rt.stopPipeline), makeApp(ProtectedPipelineStopHandler(rt.pipelineService(), protected))
	for _, scenario := range []struct {
		name, run, body, contentType string
		code                         float64
	}{
		{"success", "cancelled-run", `{"reason":"Cancelled from Build workspace"}`, "application/json", float64(apphttp.Success.Code)},
		{"not-found", "missing-run", `{}`, "application/json", 404},
		{"wrong-pipeline", "foreign-run", `{}`, "application/json", 404},
		{"conflict", "finished-run", `{}`, "application/json", 409},
		{"engine-unavailable", "active-run", `{}`, "application/json", 503},
		{"unknown-body-fields", "cancelled-run", `{"reason":"stop","actor":"ignored","triggeredBy":"ignored"}`, "application/json", float64(apphttp.Success.Code)},
		{"empty-body", "cancelled-run", "", "application/json", float64(apphttp.RequestParameterParsingFailed.Code)},
		{"invalid-json", "cancelled-run", "{", "application/json", float64(apphttp.RequestParameterParsingFailed.Code)},
		{"form-body", "cancelled-run", "reason=stop", "application/x-www-form-urlencoded", float64(apphttp.Success.Code)},
		{"large-reason", "cancelled-run", `{"reason":"` + strings.Repeat("r", 2050) + `"}`, "application/json", float64(apphttp.Success.Code)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var oldStatus int
			var oldBody map[string]any
			for i, app := range []*fiber.App{oldApp, newApp} {
				req := httptest.NewRequest("POST", "/pipelines/pipe-a/runs/"+scenario.run+"/stop", strings.NewReader(scenario.body))
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", scenario.contentType)
				response, err := app.Test(req, routerTestTimeoutMillis)
				require.NoError(t, err)
				raw, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				response.Body.Close()
				var body map[string]any
				require.NoError(t, sonic.Unmarshal(raw, &body))
				delete(body, "timestamp")
				require.Equal(t, scenario.code, body["code"], string(raw))
				require.NotContains(t, body, "detail")
				if i == 0 {
					oldStatus, oldBody = response.StatusCode, body
				} else {
					require.Equal(t, oldStatus, response.StatusCode)
					require.Equal(t, oldBody, body)
				}
			}
		})
	}
	engine := &ordinaryStopCaptureEngine{}
	require.NoError(t, rt.Services.BindPipelineEngine(engine))
	for _, app := range []*fiber.App{oldApp, newApp} {
		req := httptest.NewRequest("POST", "/pipelines/pipe-a/runs/active-run/stop", strings.NewReader(`{"reason":"Cancelled from Build workspace"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response, err := app.Test(req, routerTestTimeoutMillis)
		require.NoError(t, err)
		raw, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		response.Body.Close()
		var body map[string]any
		require.NoError(t, sonic.Unmarshal(raw, &body))
		require.Equal(t, float64(apphttp.Success.Code), body["code"])
		require.NotContains(t, body, "detail")
		require.Equal(t, 200, response.StatusCode)
	}
	require.Equal(t, []string{"active-run", "active-run"}, engine.calls)
	pipeline, err := rt.Services.PipelineRepo.Get(context.Background(), "pipe-a")
	require.NoError(t, err)
	require.Equal(t, "Cancelled from Build workspace", pipeline.LastSyncMessage)

}
