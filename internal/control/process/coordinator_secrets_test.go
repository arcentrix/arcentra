// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package process

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/pipeline"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/stretchr/testify/require"
)

type runtimeSecretResolver struct {
	resolved    *service.ProjectSecretResolution
	resolveErr  error
	validateErr error
	project     string
	versions    map[string]string
	calls       int
}

func (r *runtimeSecretResolver) ResolveProjectSecrets(_ context.Context, project string) (*service.ProjectSecretResolution, error) {
	r.calls++
	r.project = project
	return r.resolved, r.resolveErr
}
func (r *runtimeSecretResolver) ValidateProjectSecretResolution(_ context.Context, project string, versions map[string]string) error {
	r.project = project
	r.versions = maps.Clone(versions)
	versions["caller-mutation"] = "must-not-escape"
	return r.validateErr
}

func runtimeSecretFixture(t *testing.T) (*Coordinator, *pipeline.ExecutionContext, *runtimeSecretResolver, *repo.Repositories) {
	t.Helper()
	engine, repositories := newTestProcess(t)
	run := pendingManualArchiveRun(t, repositories)
	claimed, err := repositories.Pipeline.(*repo.PipelineRepo).StartManualRunIfPending(context.Background(), run)
	require.NoError(t, err)
	require.True(t, claimed)
	resolver := &runtimeSecretResolver{resolved: &service.ProjectSecretResolution{Values: map[string]string{}, Versions: map[string]string{}}}
	engine.secretSvc = resolver
	definition := &spec.Pipeline{Namespace: "test", Variables: map[string]string{"PUBLIC": "unchanged"}}
	execution := pipeline.NewExecutionContext(definition, nil, t.TempDir(), *engine.logger)
	return NewCoordinator(run, definition, engine), execution, resolver, repositories
}

func TestCoordinatorSecretsUseOriginalProjectAndCompleteRuntimeOnlySnapshot(t *testing.T) {
	rc, execution, resolver, repositories := runtimeSecretFixture(t)
	for i := 0; i < 151; i++ {
		name := fmt.Sprintf("TOKEN_%03d", i)
		resolver.resolved.Values[name] = "synthetic-canary-" + name
		resolver.resolved.Versions[fmt.Sprintf("id-%03d", i)] = "1"
	}
	require.NoError(t, repositories.Pipeline.Update(context.Background(), rc.run.PipelineID, map[string]any{"project_id": "foreign-now"}))
	require.NoError(t, rc.loadSecrets(context.Background(), execution))
	require.Equal(t, "admission-project", resolver.project)
	require.Len(t, execution.Env, 152)
	require.Equal(t, "synthetic-canary-TOKEN_150", execution.Env["secrets.TOKEN_150"])
	require.Equal(t, map[string]string{"PUBLIC": "unchanged"}, execution.Pipeline.Variables)
	stored, err := repositories.Pipeline.GetRun(context.Background(), rc.run.RunID)
	require.NoError(t, err)
	require.Equal(t, "{}", stored.Env)
	require.NoError(t, execution.ValidateSecrets(context.Background()))
	require.Len(t, resolver.versions, 151)
	resolver.resolved.Versions["forged-after-load"] = "new"
	require.NoError(t, execution.ValidateSecrets(context.Background()))
	require.Len(t, resolver.versions, 151, "snapshot and validator argument must not alias")
	resolver.validateErr = errors.New("backend synthetic-canary-TOKEN_150")
	err = execution.ValidateSecrets(context.Background())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "synthetic-canary")
	require.Equal(t, "synthetic-canary-TOKEN_150", execution.Env["secrets.TOKEN_150"], "rotation never substitutes a new value into immutable inputs")
}

func TestCoordinatorSecretsFailClosedWithoutPartialEnvironment(t *testing.T) {
	for _, state := range []string{"resolution-error", "nil-result", "legacy-unpinned", "missing-pin-schema"} {
		t.Run(state, func(t *testing.T) {
			rc, execution, resolver, repositories := runtimeSecretFixture(t)
			before := maps.Clone(execution.Env)
			resolver.resolved.Values["PARTIAL"] = "private-canary"
			db := repositories.Pipeline.(*repo.PipelineRepo).Database()
			switch state {
			case "resolution-error":
				resolver.resolveErr = errors.New("decrypt private-canary")
			case "nil-result":
				resolver.resolved = nil
			case "legacy-unpinned":
				require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", rc.run.RunID).Update("project_id", "").Error)
			case "missing-pin-schema":
				require.NoError(t, db.Migrator().DropColumn(&model.PipelineRun{}, "project_id"))
			}
			err := rc.loadSecrets(context.Background(), execution)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "private-canary")
			require.Equal(t, before, execution.Env)
			require.Nil(t, execution.ValidateSecrets)
			if state == "legacy-unpinned" || state == "missing-pin-schema" {
				require.Zero(t, resolver.calls)
			}
		})
	}
}

func TestCoordinatorSecretFailureStopsDAGBeforeDispatch(t *testing.T) {
	engine, repositories := newTestProcess(t)
	engine.secretSvc = &runtimeSecretResolver{resolveErr: errors.New("private-canary")}
	marker := filepath.Join(t.TempDir(), "must-not-execute")
	run := submitTestPipeline(t, engine, repositories, &spec.Job{Name: "build", Steps: []*spec.Step{shellStep("run", "touch '"+marker+"'")}})
	waitRun(t, repositories, run.RunID, model.PipelineStatusFailed)
	jobs, err := repositories.JobRun.ListByPipelineRunID(context.Background(), run.RunID)
	require.NoError(t, err)
	require.Empty(t, jobs)
	_, err = os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestCoordinatorMissingSecretServiceCannotExecuteSecretReferences(t *testing.T) {
	rc, execution, _, _ := runtimeSecretFixture(t)
	rc.engine.secretSvc = nil
	require.NoError(t, rc.loadSecrets(context.Background(), execution))
	rc.spec.Variables["TOKEN"] = "${{ secrets.token }}"
	require.Error(t, rc.loadSecrets(context.Background(), execution))
}

func TestCoordinatorMissingOrDisabledReferenceFailsBeforeAnyInjection(t *testing.T) {
	for _, reference := range []string{"${{ secrets.MISSING }}", "${secrets.MISSING}", "${{\tsecrets.MISSING\n}}"} {
		t.Run(reference, func(t *testing.T) {
			rc, execution, resolver, _ := runtimeSecretFixture(t)
			resolver.resolved.Values["AVAILABLE"] = "private-canary"
			resolver.resolved.Versions["disabled-id"] = "1:0:MISSING"
			rc.spec.Jobs = []*spec.Job{{Name: "build", Steps: []*spec.Step{{Name: "run", Uses: "shell", Args: spec.MapAsStruct(map[string]any{"nested": []any{map[string]any{"script": reference}}})}}}}
			before := maps.Clone(execution.Env)
			err := rc.loadSecrets(context.Background(), execution)
			require.Error(t, err)
			require.Equal(t, before, execution.Env)
			require.Nil(t, execution.ValidateSecrets)
			require.NotContains(t, err.Error(), "private-canary")
		})
	}
}

func TestCoordinatorSecretReferencesInRunEnvironmentAndDefinitionScopes(t *testing.T) {
	rc, execution, resolver, _ := runtimeSecretFixture(t)
	rc.run.Env = `{"TOKEN":"${{\tsecrets.RUN_ENV\t}}","GENERAL":"${{UNKNOWN}}"}`
	rc.spec.Runtime = &spec.Runtime{Env: map[string]string{"RUNTIME": "${secrets.RUNTIME}"}}
	rc.spec.Variables["PIPELINE"] = "${{secrets.PIPELINE}}"
	rc.spec.Jobs = []*spec.Job{{Name: "build", Env: map[string]string{"JOB": "${secrets.JOB}"}, Source: &spec.Source{Auth: &spec.SourceAuth{Token: "${secrets.SOURCE}"}}, Steps: []*spec.Step{{Name: "step", Env: map[string]string{"STEP": "${secrets.STEP}"}}}}}
	require.Equal(t, map[string]struct{}{"RUN_ENV": {}, "RUNTIME": {}, "PIPELINE": {}, "JOB": {}, "SOURCE": {}, "STEP": {}}, runSecretReferences(rc.spec, map[string]string{"TOKEN": "${{\tsecrets.RUN_ENV\t}}"}))
	for _, name := range []string{"RUN_ENV", "RUNTIME", "PIPELINE", "JOB", "SOURCE", "STEP"} {
		resolver.resolved.Values[name] = "synthetic-" + name
		resolver.resolved.Versions[name] = "1"
	}
	require.NoError(t, rc.loadSecrets(context.Background(), execution))
	require.Equal(t, "${{UNKNOWN}}", execution.ResolveVariable("${{UNKNOWN}}"))
}
