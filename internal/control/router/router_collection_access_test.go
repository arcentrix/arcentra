package router

import (
	"context"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type collectionTestDB struct{ db *gorm.DB }

func (d collectionTestDB) Database() *gorm.DB { return d.db }

func collectionRouterFixture(t *testing.T) (*fiber.App, *Router) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "permissions.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := repo.MigrateSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	repos := repo.NewRepositories(collectionTestDB{db}, nil)
	for i, item := range []struct{ id, org string }{{"a", "org-one"}, {"b", "org-one"}, {"c", "org-two"}} {
		created := time.Now().Add(time.Duration(i) * time.Second)
		if err := repos.Project.Create(context.Background(), &model.Project{BaseModel: model.BaseModel{CreatedAt: created}, ProjectID: item.id, OrgID: item.org, Name: item.id, RepoURL: "https://example.test/" + item.id, Status: 1}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Pipeline.Create(context.Background(), &model.Pipeline{BaseModel: model.BaseModel{CreatedAt: created}, PipelineID: "pipe-" + item.id, ProjectID: item.id, Name: item.id, IsEnabled: 1}); err != nil {
			t.Fatal(err)
		}
	}
	for _, permission := range []string{consts.PermProjectRead, consts.PermPipelineRead, consts.PermPipelineCreate} {
		if err := db.Create(&model.RolePermissionBinding{RoleID: "scoped-test", PermissionID: permission}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, grant := range []model.RoleGrant{
		{GrantID: "project-grant", SubjectType: "user", SubjectID: "project-user", RoleID: "scoped-test", ScopeType: "project", ScopeID: "a", IsEnabled: 1},
		{GrantID: "org-grant", SubjectType: "user", SubjectID: "org-user", RoleID: "scoped-test", ScopeType: "org", ScopeID: "org-one", IsEnabled: 1},
		{GrantID: "platform-grant", SubjectType: "user", SubjectID: "admin", RoleID: "scoped-test", ScopeType: "platform", IsEnabled: 1},
	} {
		if err := db.Create(&grant).Error; err != nil {
			t.Fatal(err)
		}
	}
	authorizer, err := authz.NewAuthorizer(repos, nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := &Router{HTTP: &http.HTTP{Auth: http.Auth{SecretKey: "test-secret"}}, Services: &service.Services{PipelineRepo: repos.Pipeline, ProjectRepo: repos.Project, Project: service.NewProjectService(repos.Project), Authorizer: authorizer}}
	app := fiber.New()
	app.Use(middleware.UnifiedResponseMiddleware())
	authMW := func(c *fiber.Ctx) error {
		token := strings.TrimPrefix(c.Get("Authorization"), "Bearer ")
		claims, err := jwt.ParseToken(token, "test-secret")
		if err != nil {
			return http.Err(c, http.Unauthorized.Code, http.Unauthorized.Msg)
		}
		c.Locals("claims", claims)
		return c.Next()
	}
	rt.pipelineRouter(app, authMW, middleware.SubjectMiddleware(nil))
	rt.projectRouter(app, authMW, middleware.SubjectMiddleware(nil))
	return app, rt
}

func collectionRequest(t *testing.T, app *fiber.App, user, method, path, body string) map[string]any {
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
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := sonic.Unmarshal(raw, &result); err != nil {
		t.Fatalf("invalid response %s: %v", raw, err)
	}
	return result
}

func TestCollectionsUseProjectAndOrganizationGrants(t *testing.T) {
	app, _ := collectionRouterFixture(t)
	for _, test := range []struct {
		user, path string
		want       int
	}{
		{"project-user", "/pipelines/?projectId=a", 1}, {"project-user", "/pipelines/", 1},
		{"project-user", "/project/", 1}, {"project-user", "/project/user/my-projects?userId=admin", 1},
		{"org-user", "/pipelines/", 2}, {"org-user", "/pipelines/?projectId=b", 1},
		{"org-user", "/project/", 2}, {"org-user", "/project/user/my-projects", 2},
		{"admin", "/pipelines/", 3}, {"admin", "/project/", 3}, {"outsider", "/pipelines/", 0}, {"outsider", "/project/", 0},
	} {
		t.Run(test.user+test.path, func(t *testing.T) {
			result := collectionRequest(t, app, test.user, "GET", test.path, "")
			if result["code"] != float64(http.Success.Code) {
				t.Fatalf("scoped request denied: %+v", result)
			}
			detail := result["detail"].(map[string]any)
			list := detail["list"].([]any)
			if len(list) != test.want || detail["total"] != float64(test.want) {
				t.Fatalf("scope leaked/dropped rows: %+v", result)
			}
		})
	}
	result := collectionRequest(t, app, "org-user", "GET", "/project/?pageNum=2&pageSize=1", "")
	detail := result["detail"].(map[string]any)
	if detail["total"] != float64(2) || len(detail["list"].([]any)) != 1 {
		t.Fatalf("authorization must precede paging: %+v", result)
	}
	result = collectionRequest(t, app, "project-user", "GET", "/pipelines/?projectId=c", "")
	if result["detail"].(map[string]any)["total"] != float64(0) {
		t.Fatalf("foreign scope leaked: %+v", result)
	}
}

func TestPipelineCreateUsesBodyProjectScope(t *testing.T) {
	app, _ := collectionRouterFixture(t)
	for _, test := range []struct {
		user, project string
		allowed       bool
	}{{"project-user", "a", true}, {"org-user", "b", true}, {"project-user", "c", false}, {"outsider", "a", false}} {
		result := collectionRequest(t, app, test.user, "POST", "/pipelines/", `{"projectId":"`+test.project+`","orgId":"org-one","name":"new","pipelineFilePath":".arcentra.yml"}`)
		if (result["code"] == float64(http.Success.Code)) != test.allowed {
			t.Fatalf("%s/%s unexpected create: %+v", test.user, test.project, result)
		}
	}
}

func TestCollectionsFailClosedWithoutAuthorizerOrSubject(t *testing.T) {
	app, rt := collectionRouterFixture(t)
	result := collectionRequest(t, app, "", "GET", "/pipelines/", "")
	if result["code"] == float64(http.Success.Code) {
		t.Fatal("anonymous list allowed")
	}
	rt.Services.Authorizer = nil
	for _, path := range []string{"/pipelines/", "/project/", "/project/user/my-projects"} {
		result := collectionRequest(t, app, "admin", "GET", path, "")
		if result["code"] == float64(http.Success.Code) {
			t.Fatalf("missing authorizer failed open at %s", path)
		}
	}
}

func TestEffectiveActionsInheritProjectOrganization(t *testing.T) {
	_, rt := collectionRouterFixture(t)
	for _, scope := range []struct{ kind, id string }{{"project", "a"}, {"pipeline", "pipe-a"}} {
		actions, err := rt.Services.Authorizer.EffectiveActions(context.Background(), authz.Subject{UserID: "org-user"}, scope.kind, scope.id)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, action := range actions {
			if action == consts.PermPipelineCreate {
				found = true
			}
		}
		if !found {
			t.Fatalf("organization action missing from %s scope: %+v", scope.kind, actions)
		}
	}
	actions, err := rt.Services.Authorizer.EffectiveActions(context.Background(), authz.Subject{UserID: "org-user"}, "project", "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 0 {
		t.Fatalf("foreign organization actions exposed: %v", actions)
	}
}
