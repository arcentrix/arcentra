package router

import (
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

func TestCopilotHistoryProductionRouterIdentityAndPrivateRead(t *testing.T) {
	_, rt := collectionRouterFixture(t)
	db := rt.Services.ProjectRepo.(*repo.ProjectRepo).Database()
	if err := db.AutoMigrate(&model.CopilotConversation{}, &model.CopilotTurn{}, &model.CopilotActionPlan{}); err != nil {
		t.Fatal(err)
	}
	repos := repo.NewRepositories(collectionTestDB{db}, nil)
	if err := db.Create(&model.PipelineRun{RunID: "history-run", PipelineID: "pipe-a", ProjectID: "a", Env: "{}"}).Error; err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")))
	rt.Services = service.NewServices(collectionTestDB{db}, nil, repos)
	rt.AppConf = &config.AppConfig{Copilot: copilot.Config{History: copilot.HistoryConfig{Enabled: true}}}
	sessions := &adminSessions{values: map[string]string{}}
	rt.Cache = sessions
	app := rt.Router()
	call := func(user, method, path, body string, status int) map[string]any {
		t.Helper()
		request := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if user != "" {
			token, _, err := jwt.GenToken(user, []byte("test-secret"), time.Hour, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := sonic.Marshal(apphttp.TokenInfo{AccessToken: token})
			if err != nil {
				t.Fatal(err)
			}
			sessions.values[consts.UserTokenKey+user] = string(raw)
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := app.Test(request, routerTestTimeoutMillis)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != status {
			t.Fatalf("%s %s: status %d want %d: %s", method, path, response.StatusCode, status, raw)
		}
		if response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("history response is cacheable")
		}
		var envelope map[string]any
		if err := sonic.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if (status == 200) != (envelope["code"] == float64(200)) {
			t.Fatalf("wrong response envelope: %s", raw)
		}
		for _, forbidden := range []string{"payloadCiphertext", "ownerUserId", "encryptKey"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatal("private internal field exposed")
			}
		}
		return envelope
	}
	base := "/projects/a/copilot/conversations"
	create := `{"runId":"history-run","requestId":"create-history-request-123","acknowledgeRetention":true}`
	call("", "POST", base, create, 401)
	call("outsider", "POST", base, create, 403)
	call("project-user", "POST", "/projects/b/copilot/conversations", create, 403)
	call("project-user", "POST", base, strings.TrimSuffix(create, "}")+`,"ownerUserId":"outsider"}`, 400)
	call("project-user", "POST", base, create+`{}`, 400)
	result := call("project-user", "POST", base, create, 200)
	detail, ok := result["detail"].(map[string]any)
	if !ok {
		t.Fatalf("missing conversation data: %#v", result)
	}
	conversation, ok := detail["conversationId"].(string)
	if !ok || conversation == "" {
		t.Fatalf("missing conversation ID: %#v", detail)
	}
	call("project-user", "GET", base+"/"+conversation, "", 200)
	call("outsider", "GET", base+"/"+conversation, "", 404)
	call("project-user", "GET", base+"?limit=01", "", 400)
	call("project-user", "GET", base+"/"+conversation+"?after=01", "", 400)
	call("project-user", "POST", base+"/"+conversation+"/plans", `{"requestId":"plan-request-123456","turnId":"turn","kind":"new_run_review","execute":true}`, 400)
	// The same still-valid session must lose history access immediately after
	// its live grant is revoked, without refreshing cached effective actions.
	if err := db.Model(&model.RoleGrant{}).Where("grant_id = ?", "project-grant").Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	call("project-user", "GET", base+"/"+conversation, "", 403)
	call("project-user", "DELETE", base+"/"+conversation, `{"expectedVersion":1}`, 403)

}
