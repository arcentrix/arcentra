// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
)

func settingsFixture(t *testing.T) adminFixture {
	f := newFirstBuildFixture(t)
	require.NoError(t, f.db.Create(&model.Project{ProjectID: "settings-project", OrgID: "legacy-scope", Name: "sample", DisplayName: "Sample", DefaultBranch: "main", Status: model.ProjectStatusActive, IsEnabled: 1, Credential: "private-credential-canary", WebhookSecret: "webhook-canary", EnvVars: []byte(`{"TOKEN":"env-canary"}`), Settings: []byte(`{"opaque":"settings-canary"}`)}).Error)
	return f
}
func settingsSnapshot(t *testing.T, f adminFixture, user string) map[string]any {
	status, body, raw := f.request(t, user, "GET", "/project/settings-project/settings", "")
	require.Equal(t, 200, status, raw)
	for _, secret := range []string{"private-credential-canary", "webhook-canary", "env-canary", "settings-canary", "credential", "webhookSecret", "envVars"} {
		require.NotContains(t, raw, secret)
	}
	return body["detail"].(map[string]any)
}
func settingsBody(t *testing.T, v map[string]any) string {
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

func TestProjectSettingsAuthenticatedCASAndLifecycle(t *testing.T) {
	f := settingsFixture(t)
	for _, u := range []string{"", "ordinary", "project-owner"} {
		status, _, _ := f.request(t, u, "GET", "/project/settings-project/settings", "")
		if u == "" {
			require.Equal(t, 401, status)
		} else {
			require.Equal(t, 403, status)
		}
	}
	first := settingsSnapshot(t, f, "admin")
	require.Equal(t, true, first["canUpdate"])
	status, body, raw := f.request(t, "admin", "PATCH", "/project/settings-project/settings", settingsBody(t, map[string]any{"version": first["version"], "displayName": "Updated", "description": "", "visibility": 0, "tags": ""}))
	require.Equal(t, 200, status, raw)
	second := body["detail"].(map[string]any)
	require.Equal(t, "Updated", second["displayName"])
	require.NotEqual(t, first["version"], second["version"])
	status, _, _ = f.request(t, "admin", "PATCH", "/project/settings-project/settings", settingsBody(t, map[string]any{"version": first["version"], "displayName": "Stale"}))
	require.Equal(t, 409, status)
	status, body, raw = f.request(t, "admin", "POST", "/project/settings-project/settings/archive", settingsBody(t, map[string]any{"version": second["version"]}))
	require.Equal(t, 200, status, raw)
	archived := body["detail"].(map[string]any)
	require.Equal(t, float64(2), archived["status"])
	require.Equal(t, false, archived["canUpdate"])
	require.Equal(t, true, archived["canRestore"])
	status, _, _ = f.request(t, "admin", "PATCH", "/project/settings-project/settings", settingsBody(t, map[string]any{"version": archived["version"], "description": "forbidden"}))
	require.Equal(t, 409, status)
	status, body, raw = f.request(t, "admin", "POST", "/project/settings-project/settings/restore", settingsBody(t, map[string]any{"version": archived["version"]}))
	require.Equal(t, 200, status, raw)
	require.Equal(t, float64(1), body["detail"].(map[string]any)["status"])
	var p model.Project
	require.NoError(t, f.db.Where("project_id = ?", "settings-project").Take(&p).Error)
	require.Equal(t, "private-credential-canary", p.Credential)
	var audits []model.AuditLog
	require.NoError(t, f.db.Where("resource_id = ?", "settings-project").Find(&audits).Error)
	require.Len(t, audits, 3)
	for _, a := range audits {
		require.Equal(t, "admin", a.ActorUserID)
		require.Equal(t, "{}", string(a.Payload))
	}
}

func TestProjectSettingsLivePermissionAndStrictInput(t *testing.T) {
	f := settingsFixture(t)
	require.NoError(t, f.db.Create(&model.RoleGrant{GrantID: "settings-read", SubjectType: "user", SubjectID: "ordinary", RoleID: consts.RoleProjectReporter, ScopeType: "project", ScopeID: "settings-project", IsEnabled: 1}).Error)
	read := settingsSnapshot(t, f, "ordinary")
	require.Equal(t, false, read["canUpdate"])
	status, _, _ := f.request(t, "ordinary", "PATCH", "/project/settings-project/settings", settingsBody(t, map[string]any{"version": read["version"], "displayName": "Denied"}))
	require.Equal(t, 403, status)
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "settings-read").Update("role_id", consts.RoleProjectOwner).Error)
	owner := settingsSnapshot(t, f, "ordinary")
	require.Equal(t, true, owner["canUpdate"])
	for _, extra := range []map[string]any{{"orgId": "foreign"}, {"createdBy": "admin"}, {"credential": "new-secret"}, {"status": 1}, {"isEnabled": 1}, {"defaultBranch": "refs/../bad"}, {"defaultBranch": "bad.lock"}, {"defaultBranch": "HEAD"}, {"displayName": ""}, {"visibility": 3}} {
		extra["version"] = owner["version"]
		status, _, raw := f.request(t, "ordinary", "PATCH", "/project/settings-project/settings", settingsBody(t, extra))
		require.Equal(t, 400, status, raw)
	}
	past := time.Now().Add(-time.Second)
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "settings-read").Update("expires_at", past).Error)
	status, _, _ = f.request(t, "ordinary", "PATCH", "/project/settings-project/settings", settingsBody(t, map[string]any{"version": owner["version"], "displayName": "After revoke"}))
	require.Equal(t, 403, status)
	status, _, _ = f.request(t, "ordinary", "GET", "/project/settings-project/settings?userId=admin", "")
	require.Equal(t, 403, status)
}

func TestProjectSettingsInactiveOrganizationAndDisabledProject(t *testing.T) {
	f := settingsFixture(t)
	require.NoError(t, f.db.Create(&model.Organization{OrgID: "legacy-scope", Name: "Real organization", Status: model.OrgStatusFrozen, IsEnabled: 0}).Error)
	snapshot := settingsSnapshot(t, f, "admin")
	require.Equal(t, false, snapshot["canUpdate"])
	status, _, _ := f.request(t, "admin", "POST", "/project/settings-project/settings/archive", settingsBody(t, map[string]any{"version": snapshot["version"]}))
	require.Equal(t, 409, status)
	require.NoError(t, f.db.Model(&model.Organization{}).Where("org_id = ?", "legacy-scope").Updates(map[string]any{"status": model.OrgStatusActive, "is_enabled": 1}).Error)
	require.NoError(t, f.db.Model(&model.Project{}).Where("project_id = ?", "settings-project").Updates(map[string]any{"status": model.ProjectStatusDisabled, "is_enabled": 0}).Error)
	snapshot = settingsSnapshot(t, f, "admin")
	require.Equal(t, false, snapshot["canRestore"])
	status, _, _ = f.request(t, "admin", "POST", "/project/settings-project/settings/restore", settingsBody(t, map[string]any{"version": snapshot["version"]}))
	require.Equal(t, 409, status)
}

func TestProjectSettingsRetiredUnversionedMutationRoutes(t *testing.T) {
	f := settingsFixture(t)
	initial := settingsSnapshot(t, f, "admin")
	for _, request := range []struct{ method, path, body string }{
		{"PUT", "/project/settings-project", `{"displayName":"bypass","status":2,"isEnabled":0}`},
		{"DELETE", "/project/settings-project", ""},
		{"POST", "/project/settings-project/enable", ""},
		{"POST", "/project/settings-project/disable", ""},
		{"POST", "/project/settings-project/statistics", `{"totalBuilds":999}`},
	} {
		status, _, raw := f.request(t, "admin", request.method, request.path, request.body)
		require.Equal(t, 409, status, raw)
		require.Contains(t, raw, "versioned project settings")
	}
	current := settingsSnapshot(t, f, "admin")
	require.Equal(t, initial["version"], current["version"])
	var audits int64
	require.NoError(t, f.db.Model(&model.AuditLog{}).Where("resource_id = ?", "settings-project").Count(&audits).Error)
	require.Zero(t, audits)
}

func TestProjectSettingsRejectsAmbiguousHistoricalIDs(t *testing.T) {
	f := settingsFixture(t)
	// Reproduce a historical database before the explicit unique-ID migration.
	if f.db.Migrator().HasIndex(&model.Project{}, "idx_project_project_id") {
		require.NoError(t, f.db.Migrator().DropIndex(&model.Project{}, "idx_project_project_id"))
	}
	require.NoError(t, f.db.Create(&model.Project{ProjectID: "settings-project", OrgID: "other-scope", Name: "collision", DisplayName: "Collision", Status: 1, IsEnabled: 1}).Error)
	status, _, raw := f.request(t, "admin", "GET", "/project/settings-project/settings", "")
	require.Equal(t, 404, status, raw)
	require.NotContains(t, raw, "Collision")
	require.NotContains(t, raw, "other-scope")
}

func TestProjectSettingsStrictObjectSyntax(t *testing.T) {
	f := settingsFixture(t)
	current := settingsSnapshot(t, f, "admin")
	version := current["version"].(string)
	for _, body := range []string{
		`{"version":"` + version + `","displayName":"one","displayName":"two"}`,
		`{"version":"` + version + `","DisplayName":"case alias"}`,
		`{"version":"` + version + `","displayName":null}`,
		`{"version":"` + version + `","visibility":null}`,
		`null`, `[]`,
	} {
		status, _, raw := f.request(t, "admin", "PATCH", "/project/settings-project/settings", body)
		require.Equal(t, 400, status, raw)
	}
	require.Equal(t, current["version"], settingsSnapshot(t, f, "admin")["version"])
}
