// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/arcentrix/arcentra/pkg/polling"
	"github.com/arcentrix/arcentra/pkg/scm/gitlab"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

const pollingTestPath = "/project/polling-project/polling"
const pollingTestRepository = "https://gitlab.example.invalid/group/repository"

type pollingHTTPSource struct {
	snapshots   atomic.Int64
	definitions atomic.Int64
	onSnapshot  func(context.Context) error
}

func (s *pollingHTTPSource) Snapshot(ctx context.Context, project *model.Project, request gitlab.SnapshotRequest) (gitlab.SnapshotResult, error) {
	s.snapshots.Add(1)
	if project.ProjectID != "polling-project" || request.Repository != "group/repository" || request.UpperBound.IsZero() {
		return gitlab.SnapshotResult{}, errors.New("unexpected synthetic polling scope")
	}
	if s.onSnapshot != nil {
		if err := s.onSnapshot(ctx); err != nil {
			return gitlab.SnapshotResult{}, err
		}
	}
	return gitlab.SnapshotResult{
		Version: gitlab.SnapshotVersion, APIOrigin: "https://gitlab.example.invalid", Repository: request.Repository,
		ProviderProjectID: 42, UpperBound: request.UpperBound,
		Observations: []gitlab.MRSnapshot{{ID: 7, IID: 1, ProviderProjectID: 42, State: "opened",
			ObservationID: "private-observation-canary", Title: "private-title-canary", ActorName: "private-author-canary",
			SourceBranch: "feature", TargetBranch: "main", SourceSHA: strings.Repeat("a", 40), UpdatedAt: request.UpperBound}},
	}, nil
}

func (s *pollingHTTPSource) Definition(context.Context, *model.Pipeline, *model.Project, string) (string, string, error) {
	s.definitions.Add(1)
	return "", "", errors.New("HTTP configuration must not execute or load a pipeline definition")
}

type pollingHTTPFixture struct {
	adminFixture
	source *pollingHTTPSource
}

func newPollingHTTPFixture(t *testing.T) pollingHTTPFixture {
	t.Helper()
	f := pollingHTTPFixture{adminFixture: newAdminFixture(t), source: &pollingHTTPSource{}}
	repositories := f.rt.Services.Repos()
	require.NoError(t, f.db.WithContext(context.Background()).AutoMigrate(&model.SCMPollingStream{}, &model.PipelineRun{}))
	require.NoError(t, f.rt.Services.ConfigurePollingAuthorization())
	t.Cleanup(func() { require.NoError(t, repo.SetPollingAuthorizer(repositories, nil)) })
	f.rt.Services.Polling = service.NewPollingService(repositories, f.source, nil, nil)
	sessions := &adminSessions{values: map[string]string{}}
	for user, token := range f.tokens {
		raw, err := sonic.Marshal(apphttp.TokenInfo{AccessToken: token})
		require.NoError(t, err)
		sessions.values[consts.UserTokenKey+user] = string(raw)
	}
	f.rt.pollingRouter(f.app, middleware.AuthorizationMiddleware("admin-test-secret", sessions))
	require.NoError(t, f.db.WithContext(context.Background()).Create(&model.Project{
		ProjectID: "polling-project", OrgID: "polling-org", Name: "Polling project", RepoType: model.RepoTypeGitLab,
		RepoURL: pollingTestRepository + ".git", DefaultBranch: "main", AuthType: model.AuthTypeToken,
		Credential: "provider-token-canary", WebhookSecret: "webhook-secret-canary", Settings: []byte(`{"secret":"settings-secret-canary"}`),
		EnvVars: []byte(`{"SECRET":"environment-secret-canary"}`), TriggerMode: model.TriggerModeMR, IsEnabled: 1, Status: model.ProjectStatusActive,
	}).Error)
	for _, item := range []struct{ id, project, name string }{
		{"pipeline-a", "polling-project", "Visible pipeline"}, {"pipeline-b", "polling-project", "hidden-pipeline-name-canary"}, {"foreign-pipeline-canary", "foreign", "foreign-pipeline-name-canary"},
	} {
		require.NoError(t, f.db.WithContext(context.Background()).Create(&model.Pipeline{PipelineID: item.id, ProjectID: item.project, Name: item.name,
			RepoURL: pollingTestRepository, DefaultBranch: "main", PipelineFilePath: "pipeline.yaml", IsEnabled: 1,
			Metadata: `{"secret":"pipeline-metadata-canary"}`}).Error)
	}
	return f
}

func pollingHTTPGrant(t *testing.T, f pollingHTTPFixture, id, subjectType, subjectID, role, scopeType, scopeID string) {
	t.Helper()
	require.NoError(t, f.db.WithContext(context.Background()).Create(&model.RoleGrant{
		GrantID: id, SubjectType: subjectType, SubjectID: subjectID, RoleID: role, ScopeType: scopeType, ScopeID: scopeID, IsEnabled: 1,
	}).Error)
}

func pollingHTTPRole(t *testing.T, f pollingHTTPFixture, role string, permissions ...string) {
	t.Helper()
	require.NoError(t, f.db.WithContext(context.Background()).Create(&model.Role{RoleID: role, Name: role, IsEnabled: 1}).Error)
	for _, permission := range permissions {
		require.NoError(t, f.db.WithContext(context.Background()).Create(&model.RolePermissionBinding{RoleID: role, PermissionID: permission}).Error)
	}
}

func pollingHTTPStatus(t *testing.T, response map[string]any) service.PollingStatus {
	t.Helper()
	raw, err := sonic.Marshal(response["detail"])
	require.NoError(t, err)
	var result service.PollingStatus
	require.NoError(t, sonic.Unmarshal(raw, &result))
	return result
}

func (f pollingHTTPFixture) baseline(t *testing.T, user string) service.PollingStatus {
	t.Helper()
	status, body, raw := f.request(t, user, "POST", pollingTestPath+"/baseline", `{"expectedRevision":0}`)
	require.Equal(t, 200, status, raw)
	result := pollingHTTPStatus(t, body)
	require.Equal(t, polling.StateBaselined, result.State)
	require.NotEmpty(t, result.BaselineID)
	return result
}

func (f pollingHTTPFixture) stream(t *testing.T) *model.SCMPollingStream {
	t.Helper()
	row, err := f.rt.Services.Repos().SCMPolling.GetByProject(context.Background(), "polling-project")
	require.NoError(t, err)
	return row
}

func (f pollingHTTPFixture) noRuns(t *testing.T) {
	t.Helper()
	var count int64
	require.NoError(t, f.db.WithContext(context.Background()).Model(&model.PipelineRun{}).Count(&count).Error)
	require.Zero(t, count)
	require.Zero(t, f.source.definitions.Load())
}

func pollingHTTPEnableBody(t *testing.T, state service.PollingStatus, ids ...string) string {
	t.Helper()
	raw, err := sonic.Marshal(map[string]any{"expectedRevision": state.Revision, "baselineId": state.BaselineID, "pipelineIds": ids})
	require.NoError(t, err)
	return string(raw)
}

func TestPollingHTTPRejectsAnonymousOrdinaryAndForeignScopes(t *testing.T) {
	f := newPollingHTTPFixture(t)
	state := f.baseline(t, "admin")
	before := f.stream(t)
	for _, route := range []struct{ method, suffix, body string }{
		{"GET", "/", ""}, {"GET", "/candidates", ""},
		{"POST", "/baseline", fmt.Sprintf(`{"expectedRevision":%d}`, state.Revision)},
		{"POST", "/enable", pollingHTTPEnableBody(t, state, "pipeline-a")},
		{"POST", "/disable", fmt.Sprintf(`{"expectedRevision":%d}`, state.Revision)},
	} {
		for _, user := range []string{"", "ordinary", "project-owner", "org-owner"} {
			status, _, raw := f.request(t, user, route.method, pollingTestPath+route.suffix+"?userId=admin&actorId=admin&teamId=admin&projectId=foreign", route.body)
			want := 403
			if user == "" {
				want = 401
			}
			require.Equal(t, want, status, user+" "+route.method+route.suffix+" "+raw)
			require.NotContains(t, raw, "canary")
		}
	}
	require.Equal(t, before, f.stream(t))
	require.EqualValues(t, 1, f.source.snapshots.Load(), "denied requests must not read the provider")
	f.noRuns(t)
}

func TestPollingHTTPActorComesOnlyFromAuthenticatedClaims(t *testing.T) {
	f := newPollingHTTPFixture(t)
	forged := `"actorId":"admin","userId":"admin","enabledBy":"admin","baselineBy":"admin","disabledBy":"admin","projectId":"foreign","teamId":"admin"`
	status, _, raw := f.request(t, "ordinary", "POST", pollingTestPath+"/baseline?actorId=admin&userId=admin", `{"expectedRevision":0,`+forged+`}`)
	require.Equal(t, 403, status, raw)
	require.Nil(t, f.stream(t))
	require.Zero(t, f.source.snapshots.Load())
	pollingHTTPGrant(t, f, "current-owner", "user", "ordinary", consts.RoleProjectMaintainer, "project", "polling-project")
	status, body, raw := f.request(t, "ordinary", "POST", pollingTestPath+"/baseline?actorId=admin", `{"expectedRevision":0,`+forged+`}`)
	require.Equal(t, 200, status, raw)
	state := pollingHTTPStatus(t, body)
	require.Equal(t, "ordinary", f.stream(t).BaselineBy)
	enable := strings.TrimSuffix(pollingHTTPEnableBody(t, state, "pipeline-a"), "}") + "," + forged + "}"
	status, body, raw = f.request(t, "ordinary", "POST", pollingTestPath+"/enable?actorId=admin", enable)
	require.Equal(t, 200, status, raw)
	state = pollingHTTPStatus(t, body)
	require.Equal(t, "ordinary", state.AuthorizerID)
	require.Equal(t, "ordinary", f.stream(t).EnabledBy)
	status, _, raw = f.request(t, "ordinary", "POST", pollingTestPath+"/disable?actorId=admin", fmt.Sprintf(`{"expectedRevision":%d,%s}`, state.Revision, forged))
	require.Equal(t, 200, status, raw)
	require.Equal(t, "ordinary", f.stream(t).DisabledBy)
	require.Equal(t, polling.StateDisabled, f.stream(t).State)
	f.noRuns(t)
}

func TestPollingHTTPBaselineAndSelectionAreExplicitAndNeverRun(t *testing.T) {
	f := newPollingHTTPFixture(t)
	for _, path := range []string{pollingTestPath + "/", pollingTestPath + "/candidates"} {
		status, body, raw := f.request(t, "admin", "GET", path, "")
		require.Equal(t, 200, status, raw)
		if strings.HasSuffix(path, "/") {
			state := pollingHTTPStatus(t, body)
			require.Equal(t, polling.StateDisabled, state.State)
			require.Zero(t, state.Revision)
		}
	}
	require.Nil(t, f.stream(t))
	require.Zero(t, f.source.snapshots.Load())
	status, _, raw := f.request(t, "admin", "POST", pollingTestPath+"/enable", `{"expectedRevision":0,"baselineId":"not-baselined","pipelineIds":["pipeline-a"]}`)
	require.Equal(t, 409, status, raw)
	state := f.baseline(t, "admin")
	require.Equal(t, 1, *state.BaselineObservationCount)
	require.Empty(t, f.stream(t).EnabledBy)
	require.Empty(t, f.stream(t).SelectionJSON)
	f.noRuns(t)
	for _, body := range []string{`{}`, `{"expectedRevision":1,"baselineId":"` + state.BaselineID + `"}`, `{"expectedRevision":1,"baselineId":"` + state.BaselineID + `","pipelineIds":[]}`} {
		status, _, raw = f.request(t, "admin", "POST", pollingTestPath+"/enable", body)
		require.Equal(t, 400, status, raw)
	}
	status, _, raw = f.request(t, "admin", "POST", pollingTestPath+"/enable", pollingHTTPEnableBody(t, state, "foreign-pipeline-canary"))
	require.Equal(t, 422, status, raw)
	require.Equal(t, polling.StateBaselined, f.stream(t).State)
	status, body, raw := f.request(t, "admin", "POST", pollingTestPath+"/enable", pollingHTTPEnableBody(t, state, "pipeline-a"))
	require.Equal(t, 200, status, raw)
	enabled := pollingHTTPStatus(t, body)
	require.Equal(t, polling.StateEnabled, enabled.State)
	require.Equal(t, []service.PollingPipelineView{{PipelineID: "pipeline-a", Name: "Visible pipeline"}}, enabled.SelectedPipelines)
	selection, err := polling.ParseSelection(f.stream(t).SelectionJSON)
	require.NoError(t, err)
	require.Len(t, selection.Pipelines, 1)
	require.Equal(t, "pipeline-a", selection.Pipelines[0].PipelineID)
	status, _, raw = f.request(t, "admin", "POST", pollingTestPath+"/disable", fmt.Sprintf(`{"expectedRevision":%d}`, state.Revision))
	require.Equal(t, 409, status, raw)
	require.Equal(t, polling.StateEnabled, f.stream(t).State)
	require.EqualValues(t, 1, f.source.snapshots.Load(), "enable must not fetch another snapshot")
	f.noRuns(t)
}

func TestPollingHTTPReadsAreCredentialSafeAndPermissionFiltered(t *testing.T) {
	f := newPollingHTTPFixture(t)
	state := f.baseline(t, "admin")
	status, _, raw := f.request(t, "admin", "POST", pollingTestPath+"/enable", pollingHTTPEnableBody(t, state, "pipeline-a", "pipeline-b"))
	require.Equal(t, 200, status, raw)
	pollingHTTPRole(t, f, "project-reader", consts.PermProjectRead)
	pollingHTTPRole(t, f, "pipeline-reader", consts.PermPipelineRead)
	pollingHTTPGrant(t, f, "project-read", "user", "ordinary", "project-reader", "project", "polling-project")
	pollingHTTPGrant(t, f, "pipeline-read", "user", "ordinary", "pipeline-reader", "pipeline", "pipeline-a")
	before := f.stream(t)
	for _, user := range []string{"admin", "ordinary"} {
		for _, path := range []string{pollingTestPath + "/", pollingTestPath + "/candidates"} {
			status, body, raw := f.request(t, user, "GET", path, "")
			require.Equal(t, 200, status, raw)
			for _, forbidden := range []string{"provider-token-canary", "webhook-secret-canary", "settings-secret-canary", "environment-secret-canary", "pipeline-metadata-canary", "private-observation-canary", "private-title-canary", "private-author-canary", "foreign-pipeline", "cursorJson", "scopeJson", "selectionJson", "pendingBatchJson", "credential"} {
				require.NotContains(t, raw, forbidden)
			}
			if user == "ordinary" {
				require.NotContains(t, raw, "pipeline-b")
				require.NotContains(t, raw, "hidden-pipeline-name-canary")
				if strings.HasSuffix(path, "/") {
					view := pollingHTTPStatus(t, body)
					require.Empty(t, view.BaselineID)
					require.Empty(t, view.AuthorizerID)
					require.Nil(t, view.BaselineObservationCount)
					require.Equal(t, service.PollingCapabilities{}, view.Capabilities)
					require.Equal(t, []service.PollingPipelineView{{PipelineID: "pipeline-a", Name: "Visible pipeline"}}, view.SelectedPipelines)
				} else {
					detail := body["detail"].(map[string]any)
					require.EqualValues(t, 1, detail["total"])
					require.Len(t, detail["items"].([]any), 1)
					require.Equal(t, false, detail["items"].([]any)[0].(map[string]any)["canEnable"])
				}
			}
		}
	}
	request := httptest.NewRequest("GET", pollingTestPath+"/", nil)
	request.Header.Set("Authorization", "Bearer "+f.tokens["ordinary"])
	response, err := f.app.Test(request, routerTestTimeoutMillis)
	require.NoError(t, err)
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.Equal(t, "private, no-store", response.Header.Get("Cache-Control"))
	require.Equal(t, before, f.stream(t), "read routes must not change the polling stream")
	require.EqualValues(t, 1, f.source.snapshots.Load())
	f.noRuns(t)
}

func TestPollingHTTPRevocationUsesCurrentAuthority(t *testing.T) {
	for _, mutation := range []string{"grant-revoked", "grant-expired", "role-disabled", "user-disabled", "user-deleted", "team-removed", "team-disabled"} {
		t.Run(mutation, func(t *testing.T) {
			f := newPollingHTTPFixture(t)
			subject := authz.Subject{UserID: "ordinary"}
			if strings.HasPrefix(mutation, "team-") {
				require.NoError(t, f.db.WithContext(context.Background()).Create(&model.Team{TeamID: "operators", Name: "Operators", IsEnabled: 1}).Error)
				require.NoError(t, f.db.WithContext(context.Background()).Create(&model.TeamMember{TeamID: "operators", UserID: "ordinary"}).Error)
				pollingHTTPGrant(t, f, "current", "team", "operators", consts.RoleProjectMaintainer, "project", "polling-project")
				subject.TeamIDs = []string{"operators"}
			} else {
				pollingHTTPGrant(t, f, "current", "user", "ordinary", consts.RoleProjectMaintainer, "project", "polling-project")
			}
			state := f.baseline(t, "ordinary")
			before := f.stream(t)
			require.NoError(t, f.rt.Services.Authorizer.Reload(context.Background()))
			resource := authz.ResourceRef{Type: "project", ID: "polling-project", ProjectID: "polling-project", OrgID: "polling-org"}
			allowed, err := f.rt.Services.Authorizer.Check(context.Background(), subject, consts.PermProjectUpdate, resource)
			require.NoError(t, err)
			require.True(t, allowed)
			switch mutation {
			case "grant-revoked":
				require.NoError(t, f.db.WithContext(context.Background()).Model(&model.RoleGrant{}).Where("grant_id = ?", "current").Update("is_enabled", 0).Error)
			case "grant-expired":
				require.NoError(t, f.db.WithContext(context.Background()).Model(&model.RoleGrant{}).Where("grant_id = ?", "current").Update("expires_at", time.Now().Add(-time.Second)).Error)
			case "role-disabled":
				require.NoError(t, f.db.WithContext(context.Background()).Model(&model.Role{}).Where("role_id = ?", consts.RoleProjectMaintainer).Update("is_enabled", 0).Error)
			case "user-disabled":
				require.NoError(t, f.db.WithContext(context.Background()).Model(&model.User{}).Where("user_id = ?", "ordinary").Update("is_enabled", 0).Error)
			case "user-deleted":
				require.NoError(t, f.db.WithContext(context.Background()).Where("user_id = ?", "ordinary").Delete(&model.User{}).Error)
			case "team-removed":
				require.NoError(t, f.db.WithContext(context.Background()).Where("team_id = ?", "operators").Delete(&model.TeamMember{}).Error)
			case "team-disabled":
				require.NoError(t, f.db.WithContext(context.Background()).Model(&model.Team{}).Where("team_id = ?", "operators").Update("is_enabled", 0).Error)
			}
			allowed, err = f.rt.Services.Authorizer.Check(context.Background(), subject, consts.PermProjectUpdate, resource)
			require.NoError(t, err)
			require.True(t, allowed, "cached authorizer must still hold stale authority")
			for _, route := range []struct{ method, suffix, body string }{
				{"GET", "/", ""}, {"GET", "/candidates", ""},
				{"POST", "/baseline", fmt.Sprintf(`{"expectedRevision":%d}`, state.Revision)},
				{"POST", "/enable", pollingHTTPEnableBody(t, state, "pipeline-a")},
				{"POST", "/disable", fmt.Sprintf(`{"expectedRevision":%d}`, state.Revision)},
			} {
				status, _, raw := f.request(t, "ordinary", route.method, pollingTestPath+route.suffix, route.body)
				require.Equal(t, 403, status, route.method+route.suffix+" "+raw)
			}
			require.Equal(t, before, f.stream(t))
			require.EqualValues(t, 1, f.source.snapshots.Load(), "revocation must be checked before provider access")
			f.noRuns(t)
		})
	}
}

func TestPollingHTTPBaselineRechecksAuthorityAfterSnapshot(t *testing.T) {
	f := newPollingHTTPFixture(t)
	pollingHTTPGrant(t, f, "current", "user", "ordinary", consts.RoleProjectMaintainer, "project", "polling-project")
	f.source.onSnapshot = func(ctx context.Context) error {
		return f.db.WithContext(ctx).Model(&model.RoleGrant{}).Where("grant_id = ?", "current").Update("is_enabled", 0).Error
	}
	status, _, raw := f.request(t, "ordinary", "POST", pollingTestPath+"/baseline", `{"expectedRevision":0}`)
	require.Equal(t, 403, status, raw)
	require.EqualValues(t, 1, f.source.snapshots.Load())
	require.Nil(t, f.stream(t), "a snapshot cannot preserve revoked write authority")
	f.noRuns(t)
}

func TestPollingHTTPTriggerRevocationCannotEnableReadableCandidate(t *testing.T) {
	f := newPollingHTTPFixture(t)
	pollingHTTPRole(t, f, "polling-reader-editor", consts.PermProjectRead, consts.PermProjectUpdate, consts.PermPipelineRead)
	pollingHTTPRole(t, f, "polling-trigger", consts.PermPipelineTrigger)
	pollingHTTPGrant(t, f, "read-update", "user", "ordinary", "polling-reader-editor", "project", "polling-project")
	pollingHTTPGrant(t, f, "trigger", "user", "ordinary", "polling-trigger", "pipeline", "pipeline-a")
	state := f.baseline(t, "ordinary")
	status, _, raw := f.request(t, "ordinary", "GET", pollingTestPath+"/candidates", "")
	require.Equal(t, 200, status, raw)
	require.Contains(t, raw, `"canEnable":true`)
	require.NoError(t, f.db.WithContext(context.Background()).Model(&model.RoleGrant{}).Where("grant_id = ?", "trigger").Update("is_enabled", 0).Error)
	status, _, raw = f.request(t, "ordinary", "GET", pollingTestPath+"/candidates", "")
	require.Equal(t, 200, status, raw)
	require.NotContains(t, raw, `"canEnable":true`)
	status, _, raw = f.request(t, "ordinary", "POST", pollingTestPath+"/enable", pollingHTTPEnableBody(t, state, "pipeline-a"))
	require.Equal(t, 403, status, raw)
	require.Equal(t, polling.StateBaselined, f.stream(t).State)
	f.noRuns(t)
}

func TestPollingHTTPProviderAndDatabaseErrorsAreSanitized(t *testing.T) {
	for _, failure := range []string{"provider", "database"} {
		t.Run(failure, func(t *testing.T) {
			f := newPollingHTTPFixture(t)
			if failure == "provider" {
				f.source.onSnapshot = func(context.Context) error { return errors.New("provider-token-canary upstream private response") }
			} else {
				require.NoError(t, f.db.Callback().Query().Before("gorm:query").Register("polling-http-query-failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "role_grant" {
						tx.AddError(errors.New("database-credential-canary SQL endpoint"))
					}
				}))
			}
			status, _, raw := f.request(t, "admin", "POST", pollingTestPath+"/baseline", `{"expectedRevision":0}`)
			require.Equal(t, 503, status, raw)
			require.NotContains(t, raw, "canary")
			require.NotContains(t, raw, "SQL")
			require.Nil(t, f.stream(t))
			f.noRuns(t)
		})
	}
}

func TestPollingHTTPUnavailablePrimaryCannotUseReadableReplica(t *testing.T) {
	f := newPollingHTTPFixture(t)
	replica := newPollingHTTPFixture(t)
	primary, err := f.db.DB()
	require.NoError(t, err)
	require.NoError(t, f.db.Use(dbresolver.Register(dbresolver.Config{
		Replicas: []gorm.Dialector{sqlite.Open(replica.db.Dialector.(*sqlite.Dialector).DSN)},
	})))
	// Warm a successful request before losing primary storage. Neither a prior
	// success marker nor a replica that still authorizes this actor may survive
	// as authority for the next polling request.
	status, _, raw := f.request(t, "admin", "GET", pollingTestPath+"/", "")
	require.Equal(t, 200, status, raw)
	require.NoError(t, primary.Close())
	project, err := f.rt.Services.Repos().Project.Get(context.Background(), "polling-project")
	require.NoError(t, err, "fixture must retain a readable replica")
	require.Equal(t, "polling-project", project.ProjectID)
	checker, err := authz.NewLiveAuthorizer(context.Background(), f.rt.Services.Repos())
	require.NoError(t, err)
	allowed, err := checker.Check(context.Background(), authz.Subject{UserID: "admin"}, consts.PermProjectRead,
		authz.ResourceRef{Type: "project", ID: project.ProjectID, ProjectID: project.ProjectID, OrgID: project.OrgID})
	require.NoError(t, err)
	require.True(t, allowed, "replica must still permit the authenticated actor")
	for _, route := range []struct{ method, suffix, body string }{
		{"GET", "/", ""}, {"GET", "/candidates", ""},
		{"POST", "/baseline", `{"expectedRevision":0}`},
		{"POST", "/enable", `{"expectedRevision":1,"baselineId":"baseline","pipelineIds":["pipeline-a"]}`},
		{"POST", "/disable", `{"expectedRevision":1}`},
	} {
		status, body, raw := f.request(t, "admin", route.method, pollingTestPath+route.suffix, route.body)
		require.Equal(t, 503, status, route.method+route.suffix+" "+raw)
		require.Nil(t, body["detail"], "failed requests must not return a success payload")
		for _, forbidden := range []string{"database is closed", "canary", `"state"`, `"capabilities"`, `"items"`} {
			require.NotContains(t, raw, forbidden)
		}
	}
	require.Zero(t, f.source.snapshots.Load())
	require.Zero(t, f.source.definitions.Load())
}

func TestPollingHTTPActorReadFailureNeverReturnsSuccess(t *testing.T) {
	f := newPollingHTTPFixture(t)
	state := f.baseline(t, "admin")
	before := f.stream(t)
	status, _, raw := f.request(t, "admin", "GET", pollingTestPath+"/", "")
	require.Equal(t, 200, status, raw)
	require.NoError(t, f.db.Callback().Query().Before("gorm:query").Register("polling-http-actor-read-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "user" {
			tx.AddError(errors.New("primary-actor-database-secret-canary unavailable"))
		}
	}))
	for _, route := range []struct{ method, suffix, body string }{
		{"GET", "/", ""}, {"GET", "/candidates", ""},
		{"POST", "/baseline", fmt.Sprintf(`{"expectedRevision":%d}`, state.Revision)},
		{"POST", "/enable", pollingHTTPEnableBody(t, state, "pipeline-a")},
		{"POST", "/disable", fmt.Sprintf(`{"expectedRevision":%d}`, state.Revision)},
	} {
		status, body, raw := f.request(t, "admin", route.method, pollingTestPath+route.suffix+"?actorId=admin&userId=admin", route.body)
		require.Equal(t, 503, status, route.method+route.suffix+" "+raw)
		require.Nil(t, body["detail"])
		require.NotContains(t, raw, "canary")
		require.NotContains(t, raw, `"state"`)
	}
	require.Equal(t, before, f.stream(t))
	require.EqualValues(t, 1, f.source.snapshots.Load(), "failed actor reads must not access the provider")
	f.noRuns(t)
}

func TestPollingHTTPMissingOrMovedSelectionRetainsSafePausedControls(t *testing.T) {
	for _, mutation := range []string{"soft-deleted", "deleted", "moved"} {
		t.Run(mutation, func(t *testing.T) {
			f := newPollingHTTPFixture(t)
			pollingHTTPGrant(t, f, "current", "user", "ordinary", consts.RoleProjectMaintainer, "project", "polling-project")
			require.NoError(t, f.db.WithContext(context.Background()).Create(&model.Project{ProjectID: "foreign", OrgID: "foreign", Name: "Other owner", IsEnabled: 1}).Error)
			state := f.baseline(t, "ordinary")
			status, body, raw := f.request(t, "ordinary", "POST", pollingTestPath+"/enable", pollingHTTPEnableBody(t, state, "pipeline-a"))
			require.Equal(t, 200, status, raw)
			state = pollingHTTPStatus(t, body)
			updates := map[string]any{"is_deleted": 1, "name": "new-owner-selection-name-canary"}
			if mutation == "moved" {
				updates = map[string]any{"project_id": "foreign", "name": "new-owner-selection-name-canary"}
			}
			require.NoError(t, f.rt.Services.Repos().Pipeline.Update(context.Background(), "pipeline-a", updates))
			if mutation == "deleted" {
				require.NoError(t, f.db.WithContext(context.Background()).Where("pipeline_id = ?", "pipeline-a").Delete(&model.Pipeline{}).Error)
			}
			status, body, raw = f.request(t, "ordinary", "GET", pollingTestPath+"/", "")
			require.Equal(t, 200, status, raw)
			paused := pollingHTTPStatus(t, body)
			require.Equal(t, polling.StatePaused, paused.State)
			require.Equal(t, state.Revision+1, paused.Revision)
			require.Equal(t, "configuration_changed", paused.Blocker)
			require.True(t, paused.Capabilities.CanBaseline)
			require.True(t, paused.Capabilities.CanDisable)
			require.False(t, paused.Capabilities.CanEnable)
			require.Empty(t, paused.SelectedPipelines)
			for _, forbidden := range []string{"new-owner-selection-name-canary", "Visible pipeline", "pipeline-a"} {
				require.NotContains(t, raw, forbidden)
			}
			status, body, raw = f.request(t, "ordinary", "POST", pollingTestPath+"/disable", fmt.Sprintf(`{"expectedRevision":%d}`, paused.Revision))
			require.Equal(t, 200, status, raw)
			require.Equal(t, polling.StateDisabled, pollingHTTPStatus(t, body).State)
			require.NotContains(t, raw, "new-owner-selection-name-canary")
			f.noRuns(t)
		})
	}
}
