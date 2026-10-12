package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
)

func artifactRouterFixture(t *testing.T) (*fiber.App, map[string]*model.ProjectArtifact, string) {
	t.Helper()
	app, rt := collectionRouterFixture(t)
	db := rt.Services.ProjectRepo.(*repo.ProjectRepo).Database()
	r := repo.NewArtifactRepo(collectionTestDB{db})
	root := t.TempDir()
	rt.Services.Artifact = service.NewArtifactService(r, nil, root)
	records := make(map[string]*model.ProjectArtifact)
	for _, scope := range []string{"a", "b"} {
		for _, value := range []any{&model.PipelineRun{RunID: "run-" + scope, PipelineID: "pipe-" + scope}, &model.JobRun{JobRunID: "job-" + scope, PipelineRunID: "run-" + scope, PipelineID: "pipe-" + scope}, &model.StepRun{StepRunID: "step-" + scope, JobRunID: "job-" + scope, PipelineRunID: "run-" + scope, PipelineID: "pipe-" + scope}} {
			if err := db.Create(value).Error; err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"dist/result.json", "empty.txt", "expired.txt", "missing.txt"} {
			payload := []byte(`{"code":401,"actual":"JSON artifact"}`)
			if name == "empty.txt" {
				payload = nil
			}
			relative, _ := artifactutil.LocalPath(scope, "run-"+scope, "job-"+scope, "step-"+scope, name)
			if name != "missing.txt" {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(root, relative)), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, relative), payload, 0600); err != nil {
					t.Fatal(err)
				}
			}
			hash := sha256.Sum256(payload)
			value := &model.ProjectArtifact{ProjectID: scope, PipelineID: "pipe-" + scope, PipelineRunID: "run-" + scope, JobRunID: "job-" + scope, StepRunID: "step-" + scope, Name: name, StorageType: "local", StoragePath: relative, Size: int64(len(payload)), SHA256: hex.EncodeToString(hash[:]), MediaType: "application/json"}
			if name == "expired.txt" {
				expired := time.Now().Add(-time.Hour)
				value.ExpiresAt = &expired
			}
			if err := r.Upsert(context.Background(), value); err != nil {
				t.Fatal(err)
			}
			records[scope+"/"+name] = value
		}
	}
	authMW := func(c *fiber.Ctx) error {
		token := strings.TrimPrefix(c.Get("Authorization"), "Bearer ")
		claims, err := jwt.ParseToken(token, "test-secret")
		if err != nil {
			return apphttp.Err(c, apphttp.Unauthorized.Code, apphttp.Unauthorized.Msg)
		}
		c.Locals("claims", claims)
		return c.Next()
	}
	app.Use(middleware.CorsMiddleware())
	rt.artifactRouter(app, authMW, middleware.SubjectMiddleware(nil))
	return app, records, root
}
func artifactRequest(t *testing.T, app *fiber.App, user, path string) *http.Response {
	t.Helper()
	request := httptest.NewRequest("GET", path, nil)
	request.Header.Set("Origin", "http://localhost:5173")
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
	return response
}
func TestProjectArtifactHTTPAuthorizationAndBinaryResponses(t *testing.T) {
	app, records, root := artifactRouterFixture(t)
	for _, test := range []struct {
		user, project, key string
		status             int
	}{
		{"", "a", "a/dist/result.json", 401}, {"outsider", "a", "a/dist/result.json", 403}, {"project-user", "b", "b/dist/result.json", 403},
		{"project-user", "b", "a/dist/result.json", 404}, {"project-user", "a", "a/expired.txt", 410}, {"project-user", "a", "a/missing.txt", 409},
		{"org-user", "b", "b/dist/result.json", 200}, {"project-user", "a", "a/dist/result.json", 200}, {"project-user", "a", "a/empty.txt", 200},
	} {
		t.Run(test.user+test.project+test.key, func(t *testing.T) {
			response := artifactRequest(t, app, test.user, "/projects/"+test.project+"/artifacts/"+records[test.key].ArtifactID+"/download")
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != test.status {
				t.Fatalf("status %d wanted %d body %s", response.StatusCode, test.status, body)
			}
			if strings.Contains(string(body), root) || strings.Contains(string(body), "storage_path") {
				t.Fatalf("private location leaked %s", body)
			}
			if test.status == 200 {
				if !strings.HasPrefix(response.Header.Get("Content-Disposition"), "attachment;") {
					t.Fatalf("missing attachment header %+v", response.Header)
				}
				if response.Header.Get("X-Content-Type-Options") != "nosniff" || response.Header.Get("Cache-Control") != "private, no-store" {
					t.Fatalf("unsafe headers %+v", response.Header)
				}
				if !strings.Contains(response.Header.Get("Access-Control-Expose-Headers"), "Content-Disposition") {
					t.Fatalf("filename inaccessible to CORS %+v", response.Header)
				}
				if strings.HasSuffix(test.key, "empty.txt") {
					if len(body) != 0 {
						t.Fatalf("empty file replaced with envelope: %s", body)
					}
				} else if string(body) != `{"code":401,"actual":"JSON artifact"}` {
					t.Fatalf("binary altered: %s", body)
				}
			} else if response.Header.Get("Content-Disposition") != "" {
				t.Fatalf("error advertised as attachment")
			}
		})
	}
}
func TestProjectArtifactHTTPFiltersRefreshAndOwnership(t *testing.T) {
	app, records, root := artifactRouterFixture(t)
	for _, test := range []struct {
		user, query string
		total       int
	}{
		{"project-user", "?page=2&pageSize=1", 4}, {"project-user", "?status=available", 2}, {"project-user", "?status=expired", 1}, {"project-user", "?status=unavailable", 1}, {"project-user", "?name=result", 1}, {"project-user", "?runId=run-b", 0}, {"outsider", "", 0},
	} {
		response := artifactRequest(t, app, test.user, "/projects/a/artifacts"+test.query)
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("list failure: %s", body)
		}
		var result struct {
			Detail service.ArtifactList `json:"detail"`
		}
		if err := sonic.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		if result.Detail.Total != int64(test.total) {
			t.Fatalf("query %s total %d wanted %d body %s", test.query, result.Detail.Total, test.total, body)
		}
	}
	if err := os.Remove(filepath.Join(root, records["a/dist/result.json"].StoragePath)); err != nil {
		t.Fatal(err)
	}
	response := artifactRequest(t, app, "project-user", "/projects/a/artifacts?status=unavailable")
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	var result struct {
		Detail service.ArtifactList `json:"detail"`
	}
	if err := sonic.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Detail.Total != 2 {
		t.Fatalf("refresh didn't reflect missing file: %s", body)
	}
	for _, query := range []string{"?page=0", "?pageSize=101", "?status=published", "?page=999999999999999999999999999999999999999"} {
		response := artifactRequest(t, app, "project-user", "/projects/a/artifacts"+query)
		_ = response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatalf("invalid pagination/filter accepted %s status %d", query, response.StatusCode)
		}
	}
}
