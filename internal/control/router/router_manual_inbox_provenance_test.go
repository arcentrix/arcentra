// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
)

type manualInboxHTTPEngine struct {
	calls int
	run   *model.PipelineRun
}

func (e *manualInboxHTTPEngine) Submit(run *model.PipelineRun, _ *pipelinev1.Spec) error {
	e.calls++
	copy := *run
	e.run = &copy
	return nil
}
func (e *manualInboxHTTPEngine) CancelRun(string) error { return nil }
func (e *manualInboxHTTPEngine) PauseRun(string) error  { return nil }
func (e *manualInboxHTTPEngine) ResumeRun(string) error { return nil }

func TestManualInboxHTTPVerifiedActorCaptureAndRequestReplay(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	sessions := &adminSessions{values: map[string]string{}}
	for user, token := range f.tokens {
		raw, err := sonic.Marshal(apphttp.TokenInfo{AccessToken: token})
		require.NoError(t, err)
		sessions.values[consts.UserTokenKey+user] = string(raw)
	}
	f.rt.pipelineRouter(f.app, middleware.AuthorizationMiddleware("admin-test-secret", sessions), middleware.SubjectMiddleware(f.rt.Services.Repos().TeamMember))
	engine := &manualInboxHTTPEngine{}
	require.NoError(t, f.rt.Services.BindPipelineEngine(engine))
	require.NoError(t, f.rt.Services.Repos().Project.Create(ctx, &model.Project{ProjectID: "http-inbox-project", IsEnabled: 1, Status: model.ProjectStatusActive, RepoURL: "https://fixture.invalid/only-fake-git.git", DefaultBranch: "main"}))
	require.NoError(t, f.rt.Services.Repos().Pipeline.Create(ctx, &model.Pipeline{PipelineID: "http-inbox-pipeline", ProjectID: "http-inbox-project", IsEnabled: 1, RepoURL: "https://fixture.invalid/only-fake-git.git", DefaultBranch: "main", PipelineFilePath: "pipeline.json"}))
	require.NoError(t, f.db.Create(&model.RoleGrant{GrantID: "inbox-other-trigger", SubjectType: "user", SubjectID: "other", RoleID: consts.RolePlatformSuperAdmin, ScopeType: "platform", IsEnabled: 1}).Error)
	directory := t.TempDir()
	content, err := spec.MarshalProtoByFormat(&pipelinev1.Spec{Namespace: "inbox", Jobs: []*pipelinev1.Job{{Name: "build", Steps: []*pipelinev1.Step{{Name: "shell", Uses: "shell", Action: "run", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"run": "echo fixture"})}}}}}, pipelinev1.SpecFormat_SPEC_FORMAT_JSON, "pipeline.json")
	require.NoError(t, err)
	source := filepath.Join(directory, "source.json")
	require.NoError(t, os.WriteFile(source, []byte(content), 0600))
	// This fixture cannot contact any repository: every git command is replaced.
	script := "#!/bin/sh\nset -eu\ncase \"$1\" in\nclone) cp \"$ARCENTRA_INBOX_HTTP_SOURCE\" pipeline.json ;;\nrev-parse) printf '%s\\n' aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa ;;\n*) exit 1 ;;\nesac\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "git"), []byte(script), 0700))
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ARCENTRA_INBOX_HTTP_SOURCE", source)
	path := "/pipelines/http-inbox-pipeline/trigger"
	for _, body := range []string{`{"triggeredBy":"other","requestId":"forged"}`, `{"triggeredBy":" admin ","requestId":"alias"}`} {
		_, denied, raw := f.request(t, "admin", "POST", path, body)
		require.EqualValues(t, apphttp.BadRequest.Code, denied["code"], raw)
	}
	_, anonymous, raw := f.request(t, "", "POST", path, `{"requestId":"anonymous"}`)
	require.NotEqualValues(t, apphttp.Success.Code, anonymous["code"], raw)
	require.Nil(t, anonymous["detail"])
	require.Zero(t, engine.calls)
	status, body, raw := f.request(t, "admin", "POST", path, `{"requestId":"trusted","inboxActorId":"other","inboxOrigin":1}`)
	require.Equal(t, 200, status, raw)
	runID := body["detail"].(map[string]any)["runId"].(string)
	run, err := f.rt.Services.PipelineRepo.GetRun(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, "admin", run.InboxActorID)
	require.Equal(t, model.RunInboxOriginVerifiedManual, run.InboxOrigin)
	require.Equal(t, "admin", engine.run.InboxActorID)
	require.Empty(t, run.ProjectID)
	require.NotContains(t, raw, "inboxActor")
	require.NotContains(t, raw, "InboxActor")
	require.NotContains(t, raw, "inbox_origin")
	status, replay, raw := f.request(t, "admin", "POST", path, `{"requestId":"trusted"}`)
	require.Equal(t, 200, status, raw)
	require.Equal(t, runID, replay["detail"].(map[string]any)["runId"])
	require.Equal(t, 1, engine.calls)
	_, conflict, raw := f.request(t, "other", "POST", path, `{"requestId":"trusted"}`)
	require.EqualValues(t, 409, conflict["code"], raw)
	require.Equal(t, 1, engine.calls)
	var count int64
	require.NoError(t, f.db.Model(&model.PipelineRun{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, f.db.Model(&model.InboxWork{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
