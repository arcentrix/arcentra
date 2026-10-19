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
	"github.com/stretchr/testify/require"
)

func TestCopilotExecutionProductionRouterValidationAndLiveAuthority(t *testing.T) {
	_, rt := collectionRouterFixture(t)
	db := rt.Services.ProjectRepo.(*repo.ProjectRepo).Database()
	require.NoError(t, db.AutoMigrate(&model.CopilotConversation{}, &model.CopilotTurn{}, &model.CopilotActionPlan{}, &model.CopilotExecutionIntent{}))
	repos := repo.NewRepositories(collectionTestDB{db}, nil)
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")))
	rt.Services = service.NewServices(collectionTestDB{db}, nil, repos)
	rt.AppConf = &config.AppConfig{Copilot: copilot.Config{History: copilot.HistoryConfig{Enabled: true}, Execution: copilot.ExecutionConfig{RetryApprovalEnabled: true}}}
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
	base := "/projects/a/copilot/execution-intents"
	prepare := "/projects/a/copilot/conversations/conversation/plans/plan/execution-intents"
	call("", "GET", base, "", 401)
	call("outsider", "GET", base, "", 403)
	call("project-user", "GET", "/projects/b/copilot/execution-intents", "", 403)
	call("project-user", "GET", base, "", 200)
	for _, query := range []string{"?limit=21", "?limit=01", "?limit=-1", "?limit=0", "?after=01", "?after=-1"} {
		call("project-user", "GET", base+query, "", 400)
	}
	for _, path := range []string{prepare, base + "/intent/confirm", base + "/intent/cancel"} {
		for _, body := range []string{`{"ownerUserId":"outsider"}`, `{"actorId":"outsider"}`, `{"execute":true}`, `{} {}`, strings.Repeat("x", 2049)} {
			call("project-user", "POST", path, body, 400)
		}
	}
	call("project-user", "POST", prepare, `{"requestId":"request-1234567890","expectedVersion":1,"planDigest":"digest"}`, 400)
	call("project-user", "POST", base+"/intent/confirm", `{"expectedVersion":1,"intentDigest":"digest"}`, 400)
	call("project-user", "GET", base+"/foreign-intent", "", 404)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "project-grant").Update("is_enabled", 0).Error)
	call("project-user", "GET", base, "", 403)
	call("project-user", "POST", base+"/intent/confirm", `{"expectedVersion":1,"intentDigest":"digest","acknowledgePermanentApproval":true}`, 404)
}
