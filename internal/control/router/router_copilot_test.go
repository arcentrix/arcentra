package router

import (
	"context"
	"encoding/base64"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
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
	if err := secretService.CreateSecret(context.Background(), &model.Secret{Name: "fixture", Scope: "project", ScopeID: "a", SecretType: "token", SecretValue: "persisted-opaque-q7Kx"}, "project-user"); err != nil {
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
	registry := copilot.NewRegistry(copilot.Config{Enabled: true, AllowedProjectIDs: []string{"a"}, Providers: []copilot.ProviderConfig{{ID: "test", Kind: "openai-compatible", BaseURL: "https://provider.invalid/v1", APIKeyEnv: "COPILOT_HTTP_FAKE_KEY", Models: []string{"test-model"}}}})
	svc := service.NewCopilotService(repos, rt.Services.Authorizer, nil, registry)
	authMW := func(c *fiber.Ctx) error {
		claims, err := jwt.ParseToken(strings.TrimPrefix(c.Get("Authorization"), "Bearer "), "test-secret")
		if err != nil {
			return apphttp.Err(c, apphttp.Unauthorized.Code, apphttp.Unauthorized.Msg)
		}
		c.Locals("claims", claims)
		return c.Next()
	}
	registerCopilotRoutes(app, authMW, middleware.SubjectMiddleware(nil), svc)
	valid := `{"requestId":"request-1234567890","runId":"run-a","providerId":"test","model":"test-model","question":"why?","context":{"stepRunIds":["step-a"],"artifactIds":[],"fromLine":0},"shareSelectedContext":true}`
	for _, tc := range []struct {
		name, user, method, path, body string
		status                         int
	}{
		{"auth", "", "GET", "/projects/a/copilot/status", "", 401},
		{"denied", "outsider", "GET", "/projects/a/copilot/status", "", 403},
		{"foreign", "project-user", "GET", "/projects/b/copilot/status", "", 403},
		{"status", "project-user", "GET", "/projects/a/copilot/status", "", 200},
		{"source", "project-user", "GET", "/projects/a/copilot/runs/run-a/context", "", 200},
		{"preview", "project-user", "POST", "/projects/a/copilot/preview", valid, 200},
		{"missing-preview", "project-user", "POST", "/projects/a/copilot/analysis", valid, 409},
		{"unknown-field", "project-user", "POST", "/projects/a/copilot/analysis", strings.TrimSuffix(valid, "}") + `,"baseUrl":"https://attacker.invalid"}`, 400},
		{"trailing-json", "project-user", "POST", "/projects/a/copilot/analysis", valid + `{}`, 400},
		{"oversized", "project-user", "POST", "/projects/a/copilot/analysis", strings.Repeat("x", 16385), 400},
		{"cancel", "project-user", "POST", "/projects/a/copilot/analysis/another-request-123/cancel", `{}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.user != "" {
				token, _, err := jwt.GenToken(tc.user, []byte("test-secret"), time.Hour, time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+token)
			}
			response, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.StatusCode, tc.status, body)
			}
			if response.Header.Get("Cache-Control") != "private, no-store" {
				t.Fatal("private response cacheable")
			}
			if strings.Contains(string(body), "provider.invalid") || strings.Contains(string(body), "fake-test-only") || strings.Contains(string(body), "persisted-opaque-q7Kx") || strings.Contains(string(body), "global-opaque-z8J") || strings.Contains(string(body), "pipeline-opaque-t9Z") {
				t.Fatal("provider config leaked")
			}
		})
	}
}
