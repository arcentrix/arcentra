package router

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
)

func deploymentRouterFixture(t *testing.T) (*fiber.App, string, string) {
	t.Helper()
	app, rt := collectionRouterFixture(t)
	db := rt.Services.ProjectRepo.(*repo.ProjectRepo).Database()
	database := collectionTestDB{db}
	records := repo.NewDeploymentRepo(database)
	rt.Services.Deployment = service.NewDeploymentService(records, rt.Services.PipelineRepo, nil, nil, nil, nil)
	for _, user := range []string{"deploy-user", "trigger-test", "dev-release-test"} {
		if err := db.Create(&model.User{UserID: user, Username: user, IsEnabled: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, role := range []string{"deploy-test", "trigger-test", "dev-release-test"} {
		if err := db.Create(&model.Role{RoleID: role, Name: role, IsEnabled: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}

	for _, action := range []string{"project:read", "project:update", "pipeline:read", "pipeline:trigger", "release:deploy_prod"} {
		if err := db.Create(&model.RolePermissionBinding{RoleID: "deploy-test", PermissionID: action}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.RoleGrant{GrantID: "deployment-grant", SubjectType: "user", SubjectID: "deploy-user", RoleID: "deploy-test", ScopeType: "project", ScopeID: "a", IsEnabled: 1}).Error; err != nil {
		t.Fatal(err)
	}

	for _, role := range []string{"trigger-test", "dev-release-test"} {
		for _, action := range []string{"project:read", "project:update", "pipeline:read", "pipeline:trigger"} {
			if err := db.Create(&model.RolePermissionBinding{RoleID: role, PermissionID: action}).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Create(&model.RoleGrant{GrantID: role, SubjectType: "user", SubjectID: role, RoleID: role, ScopeType: "project", ScopeID: "a", IsEnabled: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.RolePermissionBinding{RoleID: "dev-release-test", PermissionID: "release:deploy_dev"}).Error; err != nil {
		t.Fatal(err)
	}
	reloaded, err := authz.NewAuthorizer(repo.NewRepositories(database, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	rt.Services.Authorizer = reloaded
	environment, err := rt.Services.Deployment.CreateEnvironment(context.Background(), "a", service.DeploymentEnvironmentInput{Name: "Staging", TargetRef: "staging-web", Namespace: "staging", Deployment: "web", Container: "app"})
	if err != nil {
		t.Fatal(err)
	}
	release, err := records.CreateRelease(context.Background(), &model.DeploymentRelease{ReleaseID: "release-a", Version: 1, ProjectID: "a", EnvironmentID: environment.EnvironmentID, RequestID: "request-a", PipelineID: "pipe-a", PipelineRunID: "run-a", ArtifactID: "artifact-a", Status: "queued", Image: "registry.example/app@sha256:" + strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	authMW := func(c *fiber.Ctx) error {
		claims, err := jwt.ParseToken(strings.TrimPrefix(c.Get("Authorization"), "Bearer "), "test-secret")
		if err != nil {
			return apphttp.Err(c, 401, "authentication required")
		}
		c.Locals("claims", claims)
		return c.Next()
	}
	rt.deploymentRouter(app, authMW, middleware.SubjectMiddleware(nil))
	return app, environment.EnvironmentID, release.ReleaseID
}
func deploymentRequest(t *testing.T, app *fiber.App, user, method, path, body string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if user != "" {
		token, _, err := jwt.GenToken(user, []byte("test-secret"), time.Hour, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := app.Test(request, routerTestTimeoutMillis)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var result map[string]any
	if err := sonic.Unmarshal(raw, &result); err != nil {
		t.Fatalf("invalid JSON %s", raw)
	}
	if response.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("missing private cache policy")
	}
	return response.StatusCode, result
}
func TestDeploymentHTTPAuthorizationScopesAndDisabledExecution(t *testing.T) {
	app, _, release := deploymentRouterFixture(t)
	for _, tc := range []struct {
		user, method, path, body string
		want                     int
	}{
		{"", "GET", "/projects/a/environments", "", 401},
		{"outsider", "GET", "/projects/a/environments", "", 403},
		{"project-user", "GET", "/projects/b/environments", "", 403},
		{"project-user", "GET", "/projects/a/environments", "", 200},
		{"project-user", "GET", "/projects/a/releases", "", 200},
		{"project-user", "GET", "/projects/a/releases/" + release, "", 200},
		{"project-user", "POST", "/projects/a/releases/" + release + "/advance", "{}", 403},
		{"deploy-user", "POST", "/projects/a/releases/" + release + "/advance", "{}", 503},
		{"trigger-test", "POST", "/projects/a/releases/" + release + "/advance", "{}", 403},
		{"dev-release-test", "POST", "/projects/a/releases/" + release + "/advance", `{"tier":"dev"}`, 403},
		{"deploy-user", "POST", "/projects/a/releases", `{"pipelineId":"pipe-b"}`, 404},
		{"project-user", "POST", "/projects/a/environments", `{"name":"x"}`, 403},
		{"project-user", "GET", "/projects/a/releases?pageSize=1000", "", 400},
		{"project-user", "GET", "/projects/a/releases/missing", "", 404},
	} {
		t.Run(tc.user+tc.method+tc.path, func(t *testing.T) {
			status, result := deploymentRequest(t, app, tc.user, tc.method, tc.path, tc.body)
			if status != tc.want {
				t.Fatalf("status %d want %d: %+v", status, tc.want, result)
			}
		})
	}
	_, result := deploymentRequest(t, app, "project-user", "GET", "/projects/a/environments", "")
	detail := result["detail"].(map[string]any)
	list := detail["list"].([]any)
	if list[0].(map[string]any)["configured"] != false {
		t.Fatal("unconfigured target advertised available")
	}
	status, result := deploymentRequest(t, app, "deploy-user", "POST", "/projects/a/releases/"+release+"/cancel", "{}")
	if status != 200 || result["detail"].(map[string]any)["status"] != "cancelled" {
		t.Fatalf("cancel %+v", result)
	}
}
