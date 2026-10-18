// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func projectSecretsFixture(t *testing.T) adminFixture {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key))
	f := settingsFixture(t)
	sessions := &adminSessions{values: map[string]string{}}
	for user, token := range f.tokens {
		raw, err := sonic.Marshal(apphttp.TokenInfo{AccessToken: token})
		require.NoError(t, err)
		sessions.values[consts.UserTokenKey+user] = string(raw)
	}
	auth := middleware.AuthorizationMiddleware("admin-test-secret", sessions)
	f.rt.secretRouter(f.app, auth, middleware.SubjectMiddleware(f.rt.Services.Repos().TeamMember))
	return f
}
func secretCreate(t *testing.T, f adminFixture, name string) map[string]any {
	t.Helper()
	status, body, raw := f.request(t, "admin", "POST", "/project/settings-project/secrets", settingsBody(t, map[string]any{"name": name, "secretType": "token", "description": "safe metadata", "value": "synthetic-private-canary"}))
	require.Equal(t, 200, status, raw)
	require.NotContains(t, raw, "synthetic-private-canary")
	require.NotContains(t, raw, "secretValue")
	require.NotContains(t, raw, "MASKED")
	return body["detail"].(map[string]any)
}
func secretInventory(t *testing.T, f adminFixture, user string) map[string]any {
	t.Helper()
	status, body, raw := f.request(t, user, "GET", "/project/settings-project/secrets", "")
	require.Equal(t, 200, status, raw)
	require.NotContains(t, raw, "synthetic-private-canary")
	return body["detail"].(map[string]any)
}
func TestProjectSecretsWriteOnlyCASRotateDisable(t *testing.T) {
	f := projectSecretsFixture(t)
	first := secretCreate(t, f, "DEPLOY_TOKEN")
	id := first["secretId"].(string)
	path := "/project/settings-project/secrets/" + id
	require.Equal(t, "1", first["version"])
	require.Equal(t, true, first["configured"])
	var stored model.Secret
	require.NoError(t, f.db.Where("secret_id = ?", id).Take(&stored).Error)
	ciphertext := stored.SecretValue
	require.NotContains(t, ciphertext, "synthetic-private-canary")
	status, body, raw := f.request(t, "admin", "PATCH", path, settingsBody(t, map[string]any{"version": first["version"], "description": ""}))
	require.Equal(t, 200, status, raw)
	second := body["detail"].(map[string]any)
	require.Equal(t, "2", second["version"])
	require.NoError(t, f.db.Where("secret_id = ?", id).Take(&stored).Error)
	require.Equal(t, ciphertext, stored.SecretValue)
	status, _, _ = f.request(t, "admin", "POST", path+"/rotate", `{"version":"1","value":"stale-canary"}`)
	require.Equal(t, 409, status)
	status, body, raw = f.request(t, "admin", "POST", path+"/rotate", `{"version":"2","value":"rotated-canary"}`)
	require.Equal(t, 200, status, raw)
	require.NotContains(t, raw, "rotated-canary")
	require.Equal(t, "3", body["detail"].(map[string]any)["version"])
	resolved, err := f.rt.Services.Secret.ResolveProjectSecrets(context.Background(), "settings-project")
	require.NoError(t, err)
	require.Equal(t, "rotated-canary", resolved.Values["DEPLOY_TOKEN"])
	status, body, raw = f.request(t, "admin", "POST", path+"/disable", `{"version":"3"}`)
	require.Equal(t, 200, status, raw)
	require.Equal(t, false, body["detail"].(map[string]any)["canRotate"])
	require.Error(t, f.rt.Services.Secret.ValidateProjectSecretResolution(context.Background(), "settings-project", resolved.Versions))
	disabled, err := f.rt.Services.Secret.ResolveProjectSecrets(context.Background(), "settings-project")
	require.NoError(t, err)
	require.Empty(t, disabled.Values)
	status, _, _ = f.request(t, "admin", "POST", path+"/rotate", `{"version":"4","value":"cannot-enable"}`)
	require.Equal(t, 409, status)
	var audits []model.AuditLog
	require.NoError(t, f.db.Where("resource_id = ?", id).Find(&audits).Error)
	require.Len(t, audits, 4)
	for _, audit := range audits {
		require.Equal(t, "{}", string(audit.Payload))
		require.Equal(t, "admin", audit.ActorUserID)
	}
	inventory := secretInventory(t, f, "admin")
	require.Equal(t, float64(1), inventory["total"])
}
func TestProjectSecretsAuthorizationRevocationAndScopeIsolation(t *testing.T) {
	f := projectSecretsFixture(t)
	for _, user := range []string{"", "ordinary", "project-owner"} {
		status, _, _ := f.request(t, user, "GET", "/project/settings-project/secrets", "")
		if user == "" {
			require.Equal(t, 401, status)
		} else {
			require.Equal(t, 403, status)
		}
	}
	first := secretCreate(t, f, "ONLY_THIS_PROJECT")
	id := first["secretId"].(string)
	require.NoError(t, f.db.Create(&model.Project{ProjectID: "another", OrgID: "legacy-scope", Name: "another", Status: 1, IsEnabled: 1}).Error)
	status, _, _ := f.request(t, "admin", "POST", "/project/another/secrets/"+id+"/rotate", `{"version":"1","value":"wrong-project"}`)
	require.Equal(t, 404, status)
	status, _, _ = f.request(t, "admin", "POST", "/project/settings-project/secrets/"+strings.ToLower(id)+"/disable", `{"version":"1"}`)
	if strings.ToLower(id) != id {
		require.Equal(t, 404, status)
	}
	require.NoError(t, f.db.Create(&model.RoleGrant{GrantID: "project-secret-writer", SubjectType: "user", SubjectID: "ordinary", RoleID: consts.RoleProjectOwner, ScopeType: "project", ScopeID: "settings-project", IsEnabled: 1}).Error)
	initial := secretInventory(t, f, "ordinary")
	require.Equal(t, true, initial["canWrite"])
	past := time.Now().Add(-time.Second)
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "project-secret-writer").Update("expires_at", past).Error)
	status, _, _ = f.request(t, "ordinary", "POST", "/project/settings-project/secrets/"+id+"/rotate", `{"version":"1","value":"after-revocation"}`)
	require.Equal(t, 403, status)
	require.NoError(t, f.db.Model(&model.User{}).Where("user_id = ?", "admin").Update("is_enabled", 0).Error)
	status, _, _ = f.request(t, "admin", "GET", "/project/settings-project/secrets", "")
	require.Equal(t, 403, status)
}
func TestProjectSecretsStrictInputsMissingKeyAndRollback(t *testing.T) {
	f := projectSecretsFixture(t)
	first := secretCreate(t, f, "UPPER_TOKEN")
	path := "/project/settings-project/secrets/" + first["secretId"].(string)
	for _, body := range []string{`{"version":"1","value":null}`, `{"version":"1","value":"bad\u0000value"}`, `{"version":"1","value":""}`, `{"version":"1","value":"***MASKED***"}`, `{"version":"1","value":"configured"}`, `{"version":"1","value":"[configured]"}`, `{"version":"1","Value":"alias"}`, `{"version":"1","value":"one","value":"two"}`, `{"version":"01","value":"alias-version"}`, `{"version":"1","value":"safe","scopeId":"other"}`, `null`, `[]`} {
		status, _, raw := f.request(t, "admin", "POST", path+"/rotate", body)
		require.Equal(t, 400, status, raw)
	}
	status, _, _ := f.request(t, "admin", "PATCH", path, `{"version":"1","description":"change","value":"forbidden"}`)
	require.Equal(t, 400, status)
	status, _, _ = f.request(t, "admin", "POST", "/project/settings-project/secrets", `{"name":"upper_token","secretType":"token","value":"alias"}`)
	require.Equal(t, 409, status)
	hook := "project_secret_audit_failure"
	require.NoError(t, f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_log" {
			tx.AddError(fmt.Errorf("synthetic audit failure"))
		}
	}))
	status, _, _ = f.request(t, "admin", "POST", path+"/rotate", `{"version":"1","value":"must-rollback"}`)
	require.Equal(t, 503, status)
	require.NoError(t, f.db.Callback().Create().Remove(hook))
	resolved, err := f.rt.Services.Secret.ResolveProjectSecrets(context.Background(), "settings-project")
	require.NoError(t, err)
	require.Equal(t, "synthetic-private-canary", resolved.Values["UPPER_TOKEN"])
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", "")
	f.rt.Services.Secret = service.NewSecretService(f.rt.Services.Repos().Secret)
	inventory := secretInventory(t, f, "admin")
	require.Equal(t, false, inventory["encryptionConfigured"])
	require.Equal(t, false, inventory["canCreate"])
	status, _, _ = f.request(t, "admin", "POST", path+"/rotate", `{"version":"1","value":"cannot-encrypt"}`)
	require.Equal(t, 503, status)
	resolved, err = f.rt.Services.Secret.ResolveProjectSecrets(context.Background(), "settings-project")
	require.Error(t, err)
	require.Nil(t, resolved)
	status, _, raw := f.request(t, "admin", "PATCH", path, `{"version":"1","description":"key independent"}`)
	require.Equal(t, 200, status, raw)
}
func TestProjectSecretsLegacyPlatformBypassBlocked(t *testing.T) {
	f := projectSecretsFixture(t)
	first := secretCreate(t, f, "LEGACY_CANARY")
	var legacySQL string
	hook := "project_secret_legacy_query_scope"
	require.NoError(t, f.db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "secret" {
			legacySQL = tx.Statement.SQL.String()
		}
	}))
	defer func() { require.NoError(t, f.db.Callback().Query().Remove(hook)) }()
	id := first["secretId"].(string)
	for _, request := range []struct{ method, path, body string }{
		{"GET", "/secrets/" + id, ""}, {"GET", "/secrets/" + id + "/value", ""}, {"PUT", "/secrets/" + id, `{"scope":"global","secretValue":"bypass"}`}, {"DELETE", "/secrets/" + id, ""}, {"GET", "/secrets/scope/project/settings-project", ""}, {"POST", "/secrets/", `{"name":"BYPASS","secretType":"token","scope":"project","scopeId":"settings-project","secretValue":"bypass"}`}, {"GET", "/secrets/?scope=project&scopeID=settings-project", ""},
	} {
		_, body, raw := f.request(t, "admin", request.method, request.path, request.body)
		require.NotContains(t, raw, "synthetic-private-canary")
		require.NotEqual(t, float64(0), body["code"], raw)
		if legacySQL != "" {
			require.Contains(t, legacySQL, "LOWER(TRIM(scope)) <>")
		}
	}
	_, _, raw := f.request(t, "admin", "GET", "/secrets/", "")
	require.NotContains(t, raw, "LEGACY_CANARY")
	require.NotContains(t, raw, id)
	// Explicitly retain legacy non-project creation/value compatibility.
	status, body, raw := f.request(t, "admin", "POST", "/secrets/", `{"name":"GLOBAL_TOKEN","secretType":"token","scope":"global","secretValue":"global-canary"}`)
	require.Equal(t, 200, status, raw)
	global := body["detail"].(map[string]any)["secretId"].(string)
	_, _, raw = f.request(t, "admin", "GET", "/secrets/"+global+"/value", "")
	require.Contains(t, raw, "global-canary")
}
func TestProjectSecretsCompleteSnapshotAndCorruptionFailClosed(t *testing.T) {
	f := projectSecretsFixture(t)
	// More than the historical100-row page must resolve in one complete snapshot.
	for i := range 105 {
		secretCreate(t, f, fmt.Sprintf("TOKEN_%03d", i))
	}
	resolved, err := f.rt.Services.Secret.ResolveProjectSecrets(context.Background(), "settings-project")
	require.NoError(t, err)
	require.Len(t, resolved.Values, 105)
	require.NoError(t, f.db.Model(&model.Secret{}).Where("name = ?", "TOKEN_104").Update("secret_value", "corrupt-ciphertext").Error)
	resolved, err = f.rt.Services.Secret.ResolveProjectSecrets(context.Background(), "settings-project")
	require.Error(t, err)
	require.Nil(t, resolved)
	for i := 105; i <= 1000; i++ {
		require.NoError(t, f.db.Create(&model.Secret{SecretID: fmt.Sprintf("overflow-%d", i), Name: fmt.Sprintf("TOKEN_%03d", i), Scope: "project", ScopeID: "settings-project", SecretType: "token", SecretValue: "synthetic-invalid"}).Error)
	}
	status, _, raw := f.request(t, "admin", "GET", "/project/settings-project/secrets", "")
	require.Equal(t, 503, status, raw)
	require.NotContains(t, raw, "TOKEN_")
}

func TestProjectSecretsPlaintextBudgetAndNoKeyEmptySnapshot(t *testing.T) {
	f := projectSecretsFixture(t)
	for index := range 17 {
		status, _, raw := f.request(t, "admin", "POST", "/project/settings-project/secrets", settingsBody(t, map[string]any{"name": fmt.Sprintf("LARGE_%02d", index), "secretType": "token", "value": strings.Repeat("x", 65536)}))
		require.Equal(t, 200, status, raw)
	}
	result, err := f.rt.Services.Secret.ResolveProjectSecrets(context.Background(), "settings-project")
	require.Error(t, err)
	require.Nil(t, result)
	require.NoError(t, f.db.Create(&model.Project{ProjectID: "empty-project", OrgID: "legacy-scope", Name: "empty", Status: 1, IsEnabled: 1}).Error)
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", "")
	emptyService := service.NewSecretService(f.rt.Services.Repos().Secret)
	result, err = emptyService.ResolveProjectSecrets(context.Background(), "empty-project")
	require.NoError(t, err)
	require.Empty(t, result.Values)
	require.Empty(t, result.Versions)
}

func TestProjectSecretsCiphertextBudgetBeforeMaterialization(t *testing.T) {
	f := projectSecretsFixture(t)
	first := secretCreate(t, f, "OVERSIZED")
	require.NoError(t, f.db.Model(&model.Secret{}).Where("secret_id = ?", first["secretId"]).Update("secret_value", strings.Repeat("x", 3*1024*1024)).Error)
	materialized := false
	hook := "project_secret_size_query"
	require.NoError(t, f.db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "CASE WHEN is_enabled = 1 THEN secret_value") {
			materialized = true
		}
	}))
	defer func() { require.NoError(t, f.db.Callback().Query().Remove(hook)) }()
	resolved, err := f.rt.Services.Secret.ResolveProjectSecrets(context.Background(), "settings-project")
	require.Error(t, err)
	require.Nil(t, resolved)
	require.False(t, materialized)
	status, _, raw := f.request(t, "admin", "POST", "/project/settings-project/secrets/"+first["secretId"].(string)+"/disable", `{"version":"1"}`)
	require.Equal(t, 200, status, raw)
	resolved, err = f.rt.Services.Secret.ResolveProjectSecrets(context.Background(), "settings-project")
	require.NoError(t, err)
	require.Empty(t, resolved.Values)
}
