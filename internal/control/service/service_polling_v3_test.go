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
	"github.com/arcentrix/arcentra/pkg/scm"
	"github.com/arcentrix/arcentra/pkg/scm/pollsnapshot"
	"github.com/stretchr/testify/require"
)

func TestPollingV3ScopeUsesCanonicalStringIdentityOnly(t *testing.T) {
	for _, tc := range []struct{ provider, repository, origin, key string }{
		{"gitee", "https://gitee.com/owner/repo", "https://gitee.com", "42"},
		{"bitbucket", "https://bitbucket.org/owner/repo", "https://api.bitbucket.org", "12345678-1234-1234-1234-123456789abc"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			p := &model.Project{ProjectID: "project", RepoType: tc.provider, RepoURL: tc.repository, IsEnabled: 1, Status: model.ProjectStatusActive, TriggerMode: model.TriggerModeMR}
			result := pollsnapshot.Result{Version: 3, Provider: tc.provider, APIOrigin: tc.origin, Repository: "owner/repo", ProviderProjectKey: tc.key}
			scope, err := snapshotScope(p, result)
			require.NoError(t, err)
			require.Equal(t, tc.key, scope.ProviderProjectID)
			require.Equal(t, 3, scope.Version)
			for name, mutate := range map[string]func(*pollsnapshot.Result){
				"numeric-also-present": func(r *pollsnapshot.Result) { r.ProviderProjectID = 42 },
				"key-missing":          func(r *pollsnapshot.Result) { r.ProviderProjectKey = "" },
				"key-malformed":        func(r *pollsnapshot.Result) { r.ProviderProjectKey = "{NOT-A-KEY}" },
				"old-version":          func(r *pollsnapshot.Result) { r.Version = 2 },
				"other-origin":         func(r *pollsnapshot.Result) { r.APIOrigin = "https://other.example" },
				"other-provider":       func(r *pollsnapshot.Result) { r.Provider = "github" },
			} {
				t.Run(name, func(t *testing.T) {
					bad := result
					mutate(&bad)
					_, err := snapshotScope(p, bad)
					require.Error(t, err)
				})
			}
			source := &v3RequestSource{}
			svc := &PollingService{source: source}
			_, err = svc.readSnapshot(context.Background(), p, &scope, nil)
			require.NoError(t, err)
			require.Equal(t, tc.key, source.request.ProviderProjectKey)
			require.Zero(t, source.request.ProviderProjectID)
		})
	}
	// New optional fields cannot silently amend an old provider's evidence.
	p := &model.Project{ProjectID: "p", RepoType: "github", RepoURL: "https://github.com/owner/repo", IsEnabled: 1, Status: model.ProjectStatusActive, TriggerMode: model.TriggerModeMR}
	_, err := snapshotScope(p, pollsnapshot.Result{Version: 2, Provider: "github", APIOrigin: "https://api.github.com", Repository: "owner/repo", ProviderProjectID: 42, ProviderProjectKey: "42"})
	require.Error(t, err)
}

type v3RequestSource struct{ request pollsnapshot.Request }

func (s *v3RequestSource) Snapshot(_ context.Context, _ *model.Project, r pollsnapshot.Request) (pollsnapshot.Result, error) {
	s.request = r
	return pollsnapshot.Result{}, nil
}
func (*v3RequestSource) Definition(context.Context, *model.Pipeline, *model.Project, string) (string, string, error) {
	panic("definition not expected")
}

func TestPollingV3ObservationCutoffDoesNotInventMergeTime(t *testing.T) {
	updated := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, provider := range []string{"gitee", "bitbucket"} {
		got, err := pollingObservationTime(provider, polling.Observation{State: "merged", UpdatedAt: updated.Format(time.RFC3339Nano), MergedAt: ""})
		require.NoError(t, err)
		require.Equal(t, updated, got)
	}
}

func TestPollingBitbucketDefinitionUsesDocumentedTokenUsername(t *testing.T) {
	pipeline, project := pinnedDefinitionModels()
	project.RepoType = model.RepoTypeBitbucket
	project.RepoURL = "https://bitbucket.org/workspace/repo.git"
	pipeline.RepoURL = project.RepoURL
	sha := strings.Repeat("a", 40)
	for _, pinned := range []bool{false, true} {
		requested := ""
		if pinned {
			requested = sha
		}
		_, actual, err := loadPollingDefinition(context.Background(), pipeline, project, requested, pinned, func(_ context.Context, r scm.GitDefinitionRequest) (string, string, error) {
			require.Equal(t, "x-token-auth", r.Auth.Username)
			require.Equal(t, "synthetic-token", r.Auth.Token)
			require.Equal(t, project.RepoURL, r.RepoURL)
			require.NotContains(t, r.RepoURL, r.Auth.Token)
			require.Equal(t, requested, r.CommitSHA)
			return "name: bounded\n", sha, nil
		})
		require.NoError(t, err)
		require.Equal(t, sha, actual)
	}
}

func TestPollingV3CursorValidatesEveryObservationBeforeBaselineOrDedup(t *testing.T) {
	scope := polling.Scope{Version: 3, ProjectID: "p", Provider: "gitee", APIOrigin: "https://gitee.com", Repository: "https://gitee.com/owner/repo", ProviderProjectID: "42", Stream: "pull_requests"}
	stamp := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	o := pollsnapshot.Observation{ID: 7, IID: 3, EvidenceVersion: 3, ProviderProjectKey: "42", SourceProjectKey: "43", MergeEvidence: polling.MergeEvidenceStateTimestamp, State: "opened", SourceRepository: "fork/repo", SourceBranch: "feature", TargetBranch: "main", SourceSHA: strings.Repeat("a", 40), TargetSHA: strings.Repeat("b", 40), UpdatedAt: stamp, ObservationID: "observed"}
	result := pollsnapshot.Result{UpperBound: stamp, Observations: []pollsnapshot.Observation{o}}
	_, err := snapshotCursor(scope, result, nil)
	require.NoError(t, err)
	for _, previous := range []*polling.Cursor{nil, {Version: 1, Boundary: stamp.Format(time.RFC3339Nano), Complete: true, Observed: map[string]string{"7": "observed"}}} {
		for name, mutate := range map[string]func(*pollsnapshot.Observation){
			"future-observation": func(o *pollsnapshot.Observation) { o.UpdatedAt = stamp.Add(time.Second) },
			"different-scope":    func(o *pollsnapshot.Observation) { o.ProviderProjectKey = "99" },
			"missing-version":    func(o *pollsnapshot.Observation) { o.EvidenceVersion = 0 },
			"numeric-identity":   func(o *pollsnapshot.Observation) { o.SourceProjectID = 43 },
			"unsupported-state":  func(o *pollsnapshot.Observation) { o.State = "unknown" },
			"missing-hash":       func(o *pollsnapshot.Observation) { o.SourceSHA = "" },
		} {
			t.Run(name, func(t *testing.T) {
				bad := o
				mutate(&bad)
				got, err := snapshotCursor(scope, pollsnapshot.Result{UpperBound: stamp, Observations: []pollsnapshot.Observation{bad}}, previous)
				require.Error(t, err)
				require.Equal(t, polling.Cursor{}, got)
			})
		}
	}
	_, err = snapshotCursor(scope, pollsnapshot.Result{UpperBound: stamp, Observations: []pollsnapshot.Observation{o, o}}, nil)
	require.Error(t, err)
}

func TestPollingV3HardEvidenceErrorsDoNotSilentlyResume(t *testing.T) {
	hard := []pollsnapshot.Reason{pollsnapshot.ReasonConfiguration, pollsnapshot.ReasonRequest, pollsnapshot.ReasonMalformed, pollsnapshot.ReasonIdentity, pollsnapshot.ReasonPagination, pollsnapshot.ReasonUnsupportedEvidence, pollsnapshot.ReasonAuthentication, pollsnapshot.ReasonForbidden}
	transient := []pollsnapshot.Reason{pollsnapshot.ReasonMovement, pollsnapshot.ReasonTransport, pollsnapshot.ReasonLimit, pollsnapshot.ReasonRateLimit, pollsnapshot.ReasonStatus}
	for _, reason := range hard {
		err := &pollsnapshot.Error{Reason: reason}
		require.True(t, pollingV3HardEvidenceFailure(polling.Scope{Version: 3}, err))
		for _, version := range []int{1, 2} {
			require.False(t, pollingV3HardEvidenceFailure(polling.Scope{Version: version}, err))
		}
	}
	for _, reason := range transient {
		require.False(t, pollingV3HardEvidenceFailure(polling.Scope{Version: 3}, &pollsnapshot.Error{Reason: reason}))
	}
	require.False(t, pollingV3HardEvidenceFailure(polling.Scope{Version: 3}, context.DeadlineExceeded))
}
