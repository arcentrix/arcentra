// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func testCopilotRerunFullRouteGroupIdentityAndLegacyIsolation(t *testing.T) {
	t.Helper()
	_, rt := collectionRouterFixture(t)
	rt.AppConf = &config.AppConfig{}
	db := rt.Services.ProjectRepo.(*repo.ProjectRepo).Database()
	repos := repo.NewRepositories(collectionTestDB{db}, nil)
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")))
	sessions := &adminSessions{values: map[string]string{}}
	rt.Cache = sessions
	policy := &retryapproval.Policy{ID: "rerun-policy", Candidates: []string{"admin"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300}
	runtime := taskqueue.OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowWholeJobRetryApproval: true, AllowedImages: []string{"test/image@sha256:" + strings.Repeat("a", 64)}, MaxCPU: "1", MaxMemory: "256Mi"}
	for _, key := range []string{"ARCENTRA_CONTAINERD_ACCEPTANCE", "ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", "ARCENTRA_REQUIRE_TERMINAL_RERUN_ACCEPTANCE"} {
		t.Setenv(key, "1")
	}
	t.Setenv("ARCENTRA_SANDBOX_TEST_IMAGE", runtime.AllowedImages[0])
	var err error
	rt.Services, err = service.ProvideServices(collectionTestDB{db}, nil, repos, runtime)
	require.NoError(t, err)
	require.NoError(t, rt.Services.ConfigureRetryApprovalPolicy(policy))
	publisher := &registeredCancellationPublisher{}
	require.NoError(t, rt.Services.ConfigureTerminalRerun(true, publisher))
	require.NotNil(t, rt.Services.TerminalRerunService())
	require.NotNil(t, rt.Services.ProtectedRunCancellation())
	require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: "scoped-test", PermissionID: "pipeline:trigger"}).Error)
	now := time.Now()
	require.NoError(t, db.Create(&model.PipelineRun{RunID: "legacy-run", ProjectID: "a", PipelineID: "pipe-a", Status: model.PipelineStatusFailed, TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL), InboxOrigin: model.RunInboxOriginVerifiedManual, InboxActorID: "project-user", TriggeredBy: "project-user", EndTime: &now, TotalJobs: 1}).Error)
	require.NoError(t, db.Create(&model.JobRun{JobRunID: "legacy-job", PipelineRunID: "legacy-run", PipelineID: "pipe-a", Status: model.JobRunStatusFailed, EndTime: &now}).Error)
	// Register the actual production group once, from the real startup bundle.
	// No dispatcher is started and no model provider or old vote opt-in is needed.
	app := rt.Router()
	call := func(user, method, path, body string, status int) map[string]any {
		t.Helper()
		return copilotRerunRouterRequest(t, app, sessions, user, method, path, body, status)
	}
	base := "/projects/a/copilot/rerun-intents"
	call("", "GET", base+"/availability", "", 401)
	call("outsider", "GET", base+"/availability", "", 403)
	call("project-user", "GET", "/projects/b/copilot/rerun-intents/availability", "", 403)
	available := call("project-user", "GET", base+"/availability", "", 200)
	require.Equal(t, true, available["detail"].(map[string]any)["available"])
	eligible := call("project-user", "GET", base+"/eligibility?originalJobRunId=legacy-job", "", 200)
	require.Equal(t, false, eligible["detail"].(map[string]any)["eligible"])
	call("outsider", "GET", base+"/eligibility?originalJobRunId=legacy-job", "", 403)
	call("outsider", "GET", base+"/eligibility?originalJobRunId=missing-job", "", 403)
	call("project-user", "GET", "/projects/b/copilot/rerun-intents/eligibility?originalJobRunId=legacy-job", "", 403)
	call("project-user", "GET", base+"/by-request/request-1234567890", "", 404)
	call("outsider", "GET", base+"/by-request/request-1234567890", "", 403)
	for _, path := range []string{base, base + "/00000000-0000-4000-8000-000000000001/confirm", base + "/00000000-0000-4000-8000-000000000001/cancel-prepared"} {
		for _, body := range []string{`{"triggeredBy":"admin"}`, `{"ownerUserId":"admin"}`, `{"expiresAt":"2099-01-01"}`, `{} {}`, strings.Repeat("x", 2049)} {
			call("project-user", "POST", path, body, 400)
		}
	}
	call("project-user", "POST", base, `{"requestId":"request-1234567890","originalJobRunId":"legacy-job","snapshotDigest":"v1:`+strings.Repeat("a", 64)+`"}`, 409)
	call("project-user", "POST", base+"/00000000-0000-4000-8000-000000000001/confirm", `{"expectedVersion":1,"intentDigest":"v1:`+strings.Repeat("a", 64)+`","reviewedRunContextDigest":"v1:`+strings.Repeat("b", 64)+`"}`, 400)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "project-grant").Update("is_enabled", 0).Error)
	call("project-user", "GET", base+"/availability", "", 403)
	call("project-user", "GET", base+"/by-request/request-1234567890", "", 403)
	// Existing vote API still has its original separate route and validation.
	call("project-user", "POST", "/projects/a/copilot/execution-intents/intent/confirm", `{"triggeredBy":"admin"}`, 400)
	require.Zero(t, publisher.calls)
}

func TestCopilotRerunHTTPStaticErrors(t *testing.T) {
	for _, err := range []error{errors.New("sensitive SQL password=secret-canary"), repo.ErrCopilotRerunForbidden, repo.ErrCopilotRerunConflict, repo.ErrCopilotRerunExpired} {
		app := fiber.New()
		app.Get("/", func(c *fiber.Ctx) error { return copilotRerunHTTPError(c, err) })
		response, e := app.Test(httptest.NewRequest("GET", "/", nil))
		require.NoError(t, e)
		raw, e := io.ReadAll(response.Body)
		require.NoError(t, e)
		require.NoError(t, response.Body.Close())
		require.NotContains(t, string(raw), "secret-canary")
		require.NotContains(t, string(raw), "SQL")
	}
}
func TestCopilotRerunProductionRouterDefaultOff(t *testing.T) {
	_, rt := collectionRouterFixture(t)
	rt.AppConf = &config.AppConfig{}
	require.NoError(t, rt.Services.ConfigureTerminalRerun(false, nil))
	require.Nil(t, rt.Services.TerminalRerunService())
	require.Nil(t, rt.Services.ProtectedRunCancellation())
	sessions := &adminSessions{values: map[string]string{}}
	rt.Cache = sessions
	app := rt.Router()
	for _, endpoint := range []struct{ method, path string }{
		{"GET", "/availability"}, {"GET", "/eligibility"},
		{"GET", "/by-request/:requestID"}, {"POST", ""},
		{"GET", "/:intentID"}, {"POST", "/:intentID/confirm"},
		{"POST", "/:intentID/cancel-prepared"},
	} {
		matches := 0
		for _, route := range app.GetRoutes(true) {
			if route.Method == endpoint.method && strings.TrimSuffix(route.Path, "/") == "/api/v1/projects/:projectID/copilot/rerun-intents"+endpoint.path {
				matches++
			}
		}
		require.Equal(t, 1, matches, "endpoint must be registered exactly once: %s %s", endpoint.method, endpoint.path)
	}

	// Observe every ORM write after fixture setup, including accidental writes
	// from availability, malformed mutation requests, or the legacy vote route.
	db := rt.Services.ProjectRepo.(*repo.ProjectRepo).Database()
	var writes atomic.Int32
	countWrite := func(*gorm.DB) { writes.Add(1) }
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("rerun_default_off_create", countWrite))
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("rerun_default_off_update", countWrite))
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("rerun_default_off_delete", countWrite))
	for _, endpoint := range []struct{ method, path string }{
		{"GET", "/availability"},
		{"GET", "/eligibility?originalJobRunId=missing-job"},
		{"GET", "/by-request/request-1234567890"},
		{"POST", ""},
		{"GET", "/00000000-0000-4000-8000-000000000001"},
		{"POST", "/00000000-0000-4000-8000-000000000001/confirm"},
		{"POST", "/00000000-0000-4000-8000-000000000001/cancel-prepared"},
	} {
		t.Run(endpoint.method+endpoint.path, func(t *testing.T) {
			base := "/projects/a/copilot/rerun-intents"
			copilotRerunRouterRequest(t, app, sessions, "", endpoint.method, base+endpoint.path, `{}`, 401)
			// Disabled capability discloses no project or intent existence. Scoped
			// 403 authorization is separately proved with an enabled startup bundle.
			for _, user := range []string{"project-user", "outsider"} {
				for _, project := range []string{"a", "b", "missing-project"} {
					out := copilotRerunRouterRequest(t, app, sessions, user, endpoint.method, "/projects/"+project+"/copilot/rerun-intents"+endpoint.path, `{"triggeredBy":"admin"}`, 503)
					require.Equal(t, float64(503), out["code"])
					require.Equal(t, "Whole-job new execution is temporarily unavailable", out["errMsg"])
					require.NotContains(t, out, "detail")
				}
			}
		})
	}
	// The previous retry-vote API keeps its own body validation and route.
	copilotRerunRouterRequest(t, app, sessions, "project-user", "POST", "/projects/a/copilot/execution-intents/intent/confirm", `{"triggeredBy":"admin"}`, 400)
	require.Zero(t, writes.Load())
}

func copilotRerunRouterRequest(t *testing.T, app *fiber.App, sessions *adminSessions, user, method, path, body string, status int) map[string]any {
	t.Helper()
	request := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
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
	require.Equal(t, status, response.StatusCode, string(raw))
	require.Equal(t, "private, no-store", response.Header.Get("Cache-Control"))
	var out map[string]any
	require.NoError(t, sonic.Unmarshal(raw, &out))
	for _, secret := range []string{"ownerUserId", "payloadCiphertext", "nonce", "originalEvidenceJSON", "encryptKey"} {
		require.NotContains(t, string(raw), secret)
	}
	return out
}
