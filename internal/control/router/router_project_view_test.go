// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestProjectMetadataResponsesNeverExportConfiguration(t *testing.T) {
	f := settingsFixture(t)
	require.NoError(t, f.db.Model(&model.Project{}).Where("project_id = ?", "settings-project").Updates(map[string]any{
		"repo_url":     "https://user:repo-password-canary@example.test/repository?access_token=query-canary#fragment-canary",
		"homepage":     "https://user:homepage-password-canary@example.test/home?key=home-query-canary",
		"build_config": `{"private":"build-canary"}`,
	}).Error)
	require.NoError(t, f.db.Create(&model.RoleGrant{GrantID: "settings-public-read", SubjectType: "user", SubjectID: "ordinary", RoleID: consts.RoleProjectReporter, ScopeType: "project", ScopeID: "settings-project", IsEnabled: 1}).Error)
	for _, path := range []string{"/project/settings-project", "/project/", "/project/user/my-projects"} {
		status, _, raw := f.request(t, "ordinary", "GET", path, "")
		require.Equal(t, 200, status, raw)
		for _, sensitive := range []string{"private-credential-canary", "webhook-canary", "env-canary", "settings-canary", "build-canary", "repo-password-canary", "query-canary", "fragment-canary", "homepage-password-canary", "home-query-canary", `"envVars"`, `"settings"`, `"buildConfig"`} {
			require.NotContains(t, raw, sensitive)
		}
		require.Contains(t, raw, "https://example.test/repository")
	}
	var persisted model.Project
	require.NoError(t, f.db.Where("project_id = ?", "settings-project").Take(&persisted).Error)
	require.Contains(t, persisted.RepoURL, "repo-password-canary")
	require.Contains(t, string(persisted.EnvVars), "env-canary")
}
func TestProjectDisplayURLWithholdsOpaqueOrUnsafeValues(t *testing.T) {
	for _, value := range []string{"secret", "git@example.test:repository", "javascript:alert(1)", "https://example.test/\nsecret", "file:///private/key", "https://[invalid"} {
		require.Empty(t, publicProjectURL(value, true))
	}
	require.Equal(t, "ssh://example.test/repository", publicProjectURL("ssh://user:secret@example.test/repository?token=secret#secret", true))
	require.Empty(t, publicProjectURL("ssh://example.test/repository", false))
	require.Equal(t, "https://example.test/path", publicProjectURL("https://user:secret@example.test/path?token=secret#fragment", false))
}

func TestOrganizationProjectListStillRequiresProjectRead(t *testing.T) {
	f := settingsFixture(t)
	require.NoError(t, f.db.Create(&model.Role{RoleID: "only-org-read", Name: "Only org read", ScopeType: "org", IsEnabled: 1}).Error)
	require.NoError(t, f.db.Create(&model.RolePermissionBinding{RoleID: "only-org-read", PermissionID: consts.PermOrgRead}).Error)
	require.NoError(t, f.db.Create(&model.RoleGrant{GrantID: "only-org-read", SubjectType: "user", SubjectID: "ordinary", RoleID: "only-org-read", ScopeType: "org", ScopeID: "legacy-scope", IsEnabled: 1}).Error)
	status, body, raw := f.request(t, "ordinary", "GET", "/project/org/legacy-scope", "")
	require.Equal(t, 200, status, raw)
	detail := body["detail"].(map[string]any)
	require.Equal(t, float64(0), detail["total"])
	require.Empty(t, detail["list"])
	require.NoError(t, f.db.Create(&model.RoleGrant{GrantID: "one-project-read", SubjectType: "user", SubjectID: "ordinary", RoleID: consts.RoleProjectReporter, ScopeType: "project", ScopeID: "settings-project", IsEnabled: 1}).Error)
	status, body, raw = f.request(t, "ordinary", "GET", "/project/org/legacy-scope", "")
	require.Equal(t, 200, status, raw)
	detail = body["detail"].(map[string]any)
	require.Equal(t, float64(1), detail["total"])
	require.Len(t, detail["list"], 1)
	require.Equal(t, "settings-project", detail["list"].([]any)[0].(map[string]any)["projectId"])
}
