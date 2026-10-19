// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/polling"
	"github.com/arcentrix/arcentra/pkg/scm/pollsnapshot"
	"github.com/stretchr/testify/require"
)

func TestPollingProviderScopeBindsCurrentConfigurationAndEvidence(t *testing.T) {
	for _, tc := range []struct {
		provider, repository, apiOrigin, stream, resultProvider string
		version                                                 int
	}{
		{"gitlab", "https://gitlab.example/group/deep/repo", "https://gitlab.example", "merge_requests", "", 1},
		{"github", "https://github.com/owner/repo", "https://api.github.com", "pull_requests", "github", 2},
		{"gitea", "https://gitea.example/owner/repo", "https://gitea.example", "pull_requests", "gitea", 2},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			p := &model.Project{ProjectID: "project", RepoType: tc.provider, RepoURL: tc.repository + ".git", IsEnabled: 1, Status: model.ProjectStatusActive, TriggerMode: model.TriggerModeMR, AuthType: model.AuthTypeNone}
			path := strings.SplitN(strings.TrimPrefix(tc.repository, "https://"), "/", 2)[1]
			result := pollsnapshot.Result{Version: tc.version, Provider: tc.resultProvider, APIOrigin: tc.apiOrigin, Repository: path, ProviderProjectID: 42}
			scope, err := snapshotScope(p, result)
			require.NoError(t, err)
			require.Equal(t, polling.Scope{Version: tc.version, ProjectID: "project", Provider: tc.provider, APIOrigin: tc.apiOrigin, Repository: tc.repository, ProviderProjectID: "42", Stream: tc.stream}, scope)
			for name, mutate := range map[string]func(*pollsnapshot.Result){
				"wrong-provider":    func(r *pollsnapshot.Result) { r.Provider = "other" },
				"wrong-version":     func(r *pollsnapshot.Result) { r.Version = 99 },
				"wrong-api-origin":  func(r *pollsnapshot.Result) { r.APIOrigin = "https://other.example" },
				"wrong-repository":  func(r *pollsnapshot.Result) { r.Repository = "other/repo" },
				"missing-stable-id": func(r *pollsnapshot.Result) { r.ProviderProjectID = 0 },
			} {
				t.Run(name, func(t *testing.T) {
					bad := result
					mutate(&bad)
					_, err := snapshotScope(p, bad)
					require.ErrorIs(t, err, polling.ErrBlocked)
				})
			}
			for name, mutate := range map[string]func(*model.Project){
				"disabled":          func(p *model.Project) { p.IsEnabled = 0 },
				"archived":          func(p *model.Project) { p.Status = model.ProjectStatusArchived },
				"unsupported-auth":  func(p *model.Project) { p.AuthType = model.AuthTypeSSHKey },
				"trigger-disabled":  func(p *model.Project) { p.TriggerMode = 0 },
				"provider-change":   func(p *model.Project) { p.RepoType = "bitbucket" },
				"repository-change": func(p *model.Project) { p.RepoURL = "https://other.example/owner/repo" },
			} {
				t.Run(name, func(t *testing.T) {
					bad := *p
					mutate(&bad)
					_, err := snapshotScope(&bad, result)
					require.Error(t, err)
				})
			}
		})
	}
}

func TestPollingUnsupportedProviderCannotReadExternalSource(t *testing.T) {
	for _, p := range []*model.Project{
		nil,
		{ProjectID: "p", RepoType: "github", RepoURL: "https://github.enterprise.example/owner/repo", IsEnabled: 1, Status: model.ProjectStatusActive, TriggerMode: model.TriggerModeMR},
		{ProjectID: "p", RepoType: "gitee", RepoURL: "https://gitee.com/owner/repo", IsEnabled: 1, Status: model.ProjectStatusActive, TriggerMode: model.TriggerModeMR},
		{ProjectID: "p", RepoType: "github", RepoURL: "https://user:synthetic@github.com/owner/repo", IsEnabled: 1, Status: model.ProjectStatusActive, TriggerMode: model.TriggerModeMR},
	} {
		require.False(t, pollingProjectEligible(p))
		_, err := (productionPollingSource{}).Snapshot(context.Background(), p, pollsnapshot.Request{})
		require.ErrorIs(t, err, polling.ErrBlocked)
	}
}

func TestPollingGiteaMergeEligibilityUsesObservedUpdateNotAuthorTime(t *testing.T) {
	cutoff := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	observation := polling.Observation{State: "merged", UpdatedAt: cutoff.Add(time.Hour).Format(time.RFC3339Nano), MergedAt: cutoff.Add(-24 * time.Hour).Format(time.RFC3339Nano)}
	observed, err := pollingObservationTime(model.RepoTypeGitea, observation)
	require.NoError(t, err)
	require.True(t, observed.After(cutoff))
	require.Equal(t, cutoff.Add(time.Hour), observed)
	for _, provider := range []string{model.RepoTypeGitHub, model.RepoTypeGitLab} {
		observed, err = pollingObservationTime(provider, observation)
		require.NoError(t, err)
		require.True(t, observed.Before(cutoff), "legacy/GitHub merge eligibility remains unchanged")
	}
	observation.UpdatedAt = "invalid"
	_, err = pollingObservationTime(model.RepoTypeGitea, observation)
	require.Error(t, err)
}
