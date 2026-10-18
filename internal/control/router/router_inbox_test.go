// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func inboxHTTPFixture(t *testing.T) adminFixture {
	t.Helper()
	f := newAdminFixture(t)
	sessions := &adminSessions{values: map[string]string{}}
	for user, token := range f.tokens {
		raw, err := sonic.Marshal(apphttp.TokenInfo{AccessToken: token})
		require.NoError(t, err)
		sessions.values[consts.UserTokenKey+user] = string(raw)
	}
	f.rt.inboxRouter(f.app, middleware.AuthorizationMiddleware("admin-test-secret", sessions))
	require.NoError(t, f.db.Create(&model.Project{ProjectID: "inbox-project", IsEnabled: 1, Status: model.ProjectStatusActive}).Error)
	require.NoError(t, f.db.Create(&model.Pipeline{PipelineID: "inbox-pipeline", ProjectID: "inbox-project", IsEnabled: 1}).Error)
	run := &model.PipelineRun{RunID: "inbox-run", PipelineID: "inbox-pipeline", RequestID: "inbox-http", TriggeredBy: "admin", TriggerType: 1, Status: model.PipelineStatusPending}
	writer := f.rt.Services.Repos().Pipeline.(repo.IVerifiedManualTriggerCreator)
	require.NoError(t, writer.CreateVerifiedManualTriggerRun(context.Background(), "admin", run, func(context.Context, *repo.Repositories, *model.Project, *model.Pipeline, string) error { return nil }))
	admitted, err := f.rt.Services.Repos().Pipeline.(*repo.PipelineRepo).StartManualRunIfPending(context.Background(), run)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NoError(t, f.rt.Services.Repos().Pipeline.UpdateRun(context.Background(), run.RunID, map[string]any{"status": model.PipelineStatusSuccess, "end_time": time.Now()}))
	_, err = f.rt.Services.Inbox.ReconcileOnce(context.Background())
	require.NoError(t, err)
	return f
}
func TestInboxHTTPExplicitReadOwnershipCurrentGrantAndNoStore(t *testing.T) {
	f := inboxHTTPFixture(t)
	status, body, raw := f.request(t, "admin", "GET", "/inbox", "")
	require.Equal(t, 200, status, raw)
	detail := body["detail"].(map[string]any)
	items := detail["list"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	id := item["id"].(string)
	require.Nil(t, item["readAt"])
	status, body, raw = f.request(t, "admin", "GET", "/inbox/unread-count", "")
	require.Equal(t, 200, status, raw)
	require.EqualValues(t, 1, body["detail"].(map[string]any)["count"])
	status, body, raw = f.request(t, "other", "GET", "/inbox", "")
	require.Equal(t, 200, status, raw)
	require.Empty(t, body["detail"].(map[string]any)["list"])
	for _, guessed := range []string{id, "999999"} {
		status, _, raw = f.request(t, "other", "POST", "/inbox/"+guessed+"/read", "{}")
		require.Equal(t, 404, status, raw)
	}
	status, body, raw = f.request(t, "admin", "POST", "/inbox/"+id+"/read", "{}")
	require.Equal(t, 200, status, raw)
	readAt := body["detail"].(map[string]any)["readAt"]
	status, body, raw = f.request(t, "admin", "POST", "/inbox/"+id+"/read", "")
	require.Equal(t, 200, status, raw)
	require.Equal(t, readAt, body["detail"].(map[string]any)["readAt"])
	for _, invalid := range []string{`{"recipientId":"other"}`, `{"readAt":null}`, `null`, `[]`, `{} {}`, `{"x":1,"x":2}`} {
		status, _, raw = f.request(t, "admin", "POST", "/inbox/"+id+"/read", invalid)
		require.Equal(t, 400, status, raw)
	}
	for _, path := range []string{"/inbox?recipientId=other", "/inbox?filter=unknown", "/inbox?limit=51", "/inbox?cursor=wrong", "/inbox/unread-count?recipientId=other"} {
		status, _, raw = f.request(t, "admin", "GET", path, "")
		require.Equal(t, 400, status, raw)
	}
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("subject_id = ?", "admin").Update("is_enabled", 0).Error)
	status, body, raw = f.request(t, "admin", "GET", "/inbox", "")
	require.Equal(t, 200, status, raw)
	require.Empty(t, body["detail"].(map[string]any)["list"])
	status, _, raw = f.request(t, "admin", "POST", "/inbox/"+id+"/read", "{}")
	require.Equal(t, 404, status, raw)
	require.NoError(t, f.db.Model(&model.User{}).Where("user_id = ?", "admin").Update("is_enabled", 0).Error)
	status, _, raw = f.request(t, "admin", "GET", "/inbox", "")
	require.Equal(t, 403, status, raw)
	status, _, raw = f.request(t, "", "GET", "/inbox", "")
	require.Equal(t, 401, status, raw)
}
func TestInboxHTTPReadBodyAllowlist(t *testing.T) {
	for _, body := range []string{"", "{}", " { \n } "} {
		require.True(t, emptyInboxReadBody([]byte(body)))
	}
	for _, body := range []string{"null", "[]", "{", "{}{}", "{\"id\":1}"} {
		require.False(t, emptyInboxReadBody([]byte(body)))
	}
}
