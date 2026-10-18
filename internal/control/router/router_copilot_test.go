package router

import (
	"context"
	"encoding/base64"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/bytedance/sonic"
)

func TestCopilotHTTPAuthorizationPreviewAndValidation(t *testing.T) {
	app, rt := collectionRouterFixture(t)
	db := rt.Services.ProjectRepo.(*repo.ProjectRepo).Database()
	repos := repo.NewRepositories(collectionTestDB{db}, nil)
	if err := repos.Pipeline.CreateRun(context.Background(), &model.PipelineRun{RunID: "run-a", PipelineID: "pipe-a"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.StepRun.Create(context.Background(), &model.StepRun{StepRunID: "step-a", PipelineRunID: "run-a", PipelineID: "pipe-a"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Log.CreateBatch(context.Background(), []*model.StepRunLog{{LogID: "log", StepRunID: "step-a", LineNumber: 1, Content: "compile error persisted-opaque-q7Kx global-opaque-z8J pipeline-opaque-t9Z"}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")))
	secretService := service.NewSecretService(repos.Secret)
	fixtureSecret := &model.Secret{Name: "fixture", Scope: "global", SecretType: "token", SecretValue: "persisted-opaque-q7Kx"}
	if err := secretService.CreateSecret(context.Background(), fixtureSecret, "project-user"); err != nil {
		t.Fatal(err)
	}
	// Synthetic historical project row; platform mutation no longer accepts project scope.
	if err := db.Model(&model.Secret{}).Where("secret_id = ?", fixtureSecret.SecretID).Updates(map[string]any{"scope": "project", "scope_id": "a"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, secret := range []*model.Secret{
		{Name: "global-fixture", Scope: "global", SecretType: "token", SecretValue: "global-opaque-z8J"},
		{Name: "pipeline-fixture", Scope: "pipeline", ScopeID: "pipe-a", SecretType: "token", SecretValue: "pipeline-opaque-t9Z"},
	} {
		if err := secretService.CreateSecret(context.Background(), secret, "project-user"); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("COPILOT_HTTP_FAKE_KEY", "fake-test-only")
	providerConfig := copilot.Config{Enabled: true, AllowedProjectIDs: []string{"a"}, Providers: []copilot.ProviderConfig{{ID: "test", Kind: "openai-compatible", BaseURL: "https://provider.invalid/v1", APIKeyEnv: "COPILOT_HTTP_FAKE_KEY", Models: []string{"test-model"}}}}
	// Exercise the complete production registration order, including deployment
	// and artifact middleware, with real token/session validation and scoped roles.
	rt.Services = service.NewServices(collectionTestDB{db}, nil, repos)
	rt.AppConf = &config.AppConfig{Copilot: providerConfig}
	sessions := &adminSessions{values: map[string]string{}}
	rt.Cache = sessions
	app = rt.Router()
	valid := `{"requestId":"request-1234567890","runId":"run-a","providerId":"test","model":"test-model","question":"why?","context":{"stepRunIds":["step-a"],"artifactIds":[],"fromLine":0},"shareSelectedContext":true}`
	for _, tc := range []struct {
		name, user, method, path, body string
		status                         int
	}{
		{"auth", "", "GET", "/projects/a/copilot/status", "", 401},
		{"denied", "outsider", "GET", "/projects/a/copilot/status", "", 403},
		{"foreign", "project-user", "GET", "/projects/b/copilot/status", "", 403},
		{"deployment-auth", "", "GET", "/projects/a/environments", "", 401},
		{"deployment-denied", "outsider", "GET", "/projects/a/environments", "", 403},
		{"deployment-foreign", "project-user", "GET", "/projects/b/environments", "", 403},
		{"deployment-status", "project-user", "GET", "/projects/a/environments", "", 200},
		{"deployment-write-denied", "project-user", "POST", "/projects/a/environments", `{}`, 403},
		{"status", "project-user", "GET", "/projects/a/copilot/status", "", 200},
		{"source", "project-user", "GET", "/projects/a/copilot/runs/run-a/context", "", 200},
		{"preview", "project-user", "POST", "/projects/a/copilot/preview", valid, 200},
		{"missing-preview", "project-user", "POST", "/projects/a/copilot/analysis", valid, 409},
		{"unknown-field", "project-user", "POST", "/projects/a/copilot/analysis", strings.TrimSuffix(valid, "}") + `,"baseUrl":"https://attacker.invalid"}`, 400},
		{"source-default-off", "project-user", "POST", "/projects/a/copilot/preview", strings.Replace(valid, `"stepRunIds":["step-a"]`, `"stepRunIds":[],"sourceFiles":[{"jobRunId":"job","path":"main.go"}]`, 1), 409},
		{"source-repo-injection", "project-user", "POST", "/projects/a/copilot/preview", strings.Replace(valid, `"stepRunIds":["step-a"]`, `"stepRunIds":[],"sourceFiles":[{"jobRunId":"job","path":"main.go","repository":"https://evil.invalid/repo"}]`, 1), 400},
		{"source-commit-injection", "project-user", "POST", "/projects/a/copilot/preview", strings.Replace(valid, `"stepRunIds":["step-a"]`, `"stepRunIds":[],"sourceFiles":[{"jobRunId":"job","path":"main.go","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]`, 1), 400},
		{"source-path-traversal", "project-user", "POST", "/projects/a/copilot/preview", strings.Replace(valid, `"stepRunIds":["step-a"]`, `"stepRunIds":[],"sourceFiles":[{"jobRunId":"job","path":"../main.go"}]`, 1), 400},
		{"trailing-json", "project-user", "POST", "/projects/a/copilot/analysis", valid + `{}`, 400},
		{"oversized", "project-user", "POST", "/projects/a/copilot/analysis", strings.Repeat("x", 16385), 400},
		{"cancel", "project-user", "POST", "/projects/a/copilot/analysis/another-request-123/cancel", `{}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/v1"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.user != "" {
				token, _, err := jwt.GenToken(tc.user, []byte("test-secret"), time.Hour, time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := sonic.Marshal(apphttp.TokenInfo{AccessToken: token})
				if err != nil {
					t.Fatal(err)
				}
				sessions.values[consts.UserTokenKey+tc.user] = string(raw)
				req.Header.Set("Authorization", "Bearer "+token)
			}
			response, err := app.Test(req, routerTestTimeoutMillis)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.StatusCode, tc.status, body)
			}
			var envelope map[string]any
			if err := sonic.Unmarshal(body, &envelope); err != nil {
				t.Fatal(err)
			}
			if tc.status == 200 && envelope["code"] != float64(200) {
				t.Fatalf("successful HTTP status with failure envelope: %s", body)
			}
			if tc.status != 200 && envelope["code"] == float64(200) {
				t.Fatalf("failed HTTP status with success envelope: %s", body)
			}
			if response.Header.Get("Cache-Control") != "private, no-store" {
				t.Fatal("private response cacheable")
			}
			if strings.Contains(string(body), "provider.invalid") || strings.Contains(string(body), "fake-test-only") || strings.Contains(string(body), "persisted-opaque-q7Kx") || strings.Contains(string(body), "global-opaque-z8J") || strings.Contains(string(body), "pipeline-opaque-t9Z") {
				t.Fatal("provider config leaked")
			}
		})
	}
	// Deployment may be deliberately absent while Copilot is usable. The
	// unrelated subsystem must not replace Copilot's response or auth envelope.
	rt.Services.Deployment = nil
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/projects/a/copilot/status", 200},
		{"/projects/a/environments", 503},
		{"/projects/a/releases", 503},
	} {
		t.Run("deployment-unavailable"+tc.path, func(t *testing.T) {
			var info apphttp.TokenInfo
			if err := sonic.UnmarshalString(sessions.values[consts.UserTokenKey+"project-user"], &info); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", "/api/v1"+tc.path, nil)
			req.Header.Set("Authorization", "Bearer "+info.AccessToken)
			response, err := app.Test(req, routerTestTimeoutMillis)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.StatusCode, tc.status, body)
			}
		})
	}

}
