// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/admission"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/validation"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
)

func pendingManualArchiveRun(t *testing.T, repositories *repo.Repositories) *model.PipelineRun {
	t.Helper()
	if err := repositories.Pipeline.Create(context.Background(), &model.Pipeline{PipelineID: "manual-pipeline", ProjectID: "admission-project", IsEnabled: 1}); err != nil {
		t.Fatal(err)
	}
	run := &model.PipelineRun{RunID: "manual-run", PipelineID: "manual-pipeline", TriggerType: 1, TriggeredBy: "actor", Status: model.PipelineStatusPending}
	if err := repositories.Pipeline.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestProcessProjectArchiveBlocksManualAndAutomaticAdmission(t *testing.T) {
	for _, source := range []string{"manual", "automatic"} {
		t.Run(source, func(t *testing.T) {
			engine, repositories := newTestProcess(t)
			var run *model.PipelineRun
			if source == "manual" {
				run = pendingManualArchiveRun(t, repositories)
			} else {
				run = pendingAutomaticRun(t, repositories.Pipeline, "archive")
			}
			db := repositories.Pipeline.(*repo.PipelineRepo).Database()
			if err := repo.NewProjectRepo(processTestDB{db}).Update(context.Background(), "admission-project", map[string]any{"status": model.ProjectStatusArchived}); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "executed")
			definition := &spec.Pipeline{Namespace: "test", Jobs: []*spec.Job{{Name: "build", Steps: []*spec.Step{shellStep("build", "touch '"+marker+"'")}}}}
			if err := engine.Submit(run, definition); err == nil {
				t.Fatal("archived scope admitted")
			}
			if _, exists := engine.runs.Load(run.RunID); exists {
				t.Fatal("denial created coordinator")
			}
			stored, err := repositories.Pipeline.GetRun(context.Background(), run.RunID)
			if err != nil || stored.StartTime != nil || stored.Status != model.PipelineStatusPending {
				t.Fatal("denial claimed run", err)
			}
			jobs, err := repositories.JobRun.ListByPipelineRunID(context.Background(), run.RunID)
			if err != nil || len(jobs) != 0 {
				t.Fatal("denial created jobs", err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("denial executed command")
			}
		})
	}
}

func TestProcessArchiveDoesNotCancelAdmittedManualRun(t *testing.T) {
	engine, repositories := newTestProcess(t)
	engine.sem = make(chan struct{}, 1)
	engine.sem <- struct{}{}
	run := pendingManualArchiveRun(t, repositories)
	if err := engine.Submit(run, validAutomaticDefinition()); err != nil {
		t.Fatal(err)
	}
	db := repositories.Pipeline.(*repo.PipelineRepo).Database()
	if err := repo.NewProjectRepo(processTestDB{db}).Update(context.Background(), "admission-project", map[string]any{"status": model.ProjectStatusArchived}); err != nil {
		t.Fatal(err)
	}
	<-engine.sem
	waitRun(t, repositories, run.RunID, model.PipelineStatusSuccess)
}

type manualUnknownAdmissionRepo struct {
	repo.IPipelineRepository
	failure error
}

func (r *manualUnknownAdmissionRepo) StartManualRunIfPending(context.Context, *model.PipelineRun) (bool, error) {
	return true, r.failure
}
func TestManualAdmissionUncertainCommitDoesNotScheduleOrRewrite(t *testing.T) {
	engine, repositories := newTestProcess(t)
	run := pendingManualArchiveRun(t, repositories)
	original := repositories.Pipeline
	failure := errors.New("synthetic commit outcome unknown")
	repositories.Pipeline = &manualUnknownAdmissionRepo{IPipelineRepository: original, failure: failure}
	if err := engine.Submit(run, validAutomaticDefinition()); err == nil || admission.IsRejected(err) || !errors.Is(err, failure) {
		t.Fatalf("unknown admission changed classification: %v", err)
	}
	if _, exists := engine.runs.Load(run.RunID); exists {
		t.Fatal("unknown admission created coordinator")
	}
	stored, err := original.GetRun(context.Background(), run.RunID)
	if err != nil || stored.Status != model.PipelineStatusPending || stored.StartTime != nil || stored.EndTime != nil {
		t.Fatal("unknown outcome rewrote run", err)
	}
}

func TestRetryApprovalProcessUsesSemanticEnvironmentIdentity(t *testing.T) {
	for _, test := range []struct {
		name     string
		env      string
		admitted bool
	}{
		{"equivalent-json", `{"SECOND":"two","FIRST":"one"}`, true},
		{"changed-value", `{"SECOND":"changed","FIRST":"one"}`, false},
		{"null-value", `{"SECOND":null,"FIRST":"one"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine, repositories := newTestProcess(t)
			// Hold execution capacity so this tests only verified admission and
			// never attempts to dispatch a synthetic offline Agent workload.
			engine.sem = make(chan struct{}, 1)
			engine.sem <- struct{}{}
			image := "registry.example/build@sha256:" + strings.Repeat("a", 64)
			parsed := &spec.Pipeline{Namespace: "test", Runtime: &spec.Runtime{Type: taskqueue.RuntimeContainerdOfflineV1, Image: image, Resources: &spec.Resources{CpuLimit: "1", MemoryLimit: "64Mi"}}, Jobs: []*spec.Job{{Name: "build", Retry: &spec.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}, Approval: &spec.Approval{Required: true, Type: "manual", Plugin: retryapproval.Plugin}, Steps: []*spec.Step{{Name: "shell", Uses: "shell", Args: spec.MapAsStruct(map[string]any{"run": "exit 9"}), RunOnAgent: true}}}}}
			policy := retryapproval.Policy{ID: "retry", Candidates: []string{"reviewer"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 60}
			var err error
			policy.Fingerprint, err = policy.Digest()
			require.NoError(t, err)
			definition, err := validation.ResolvedDefinitionDigest(parsed)
			require.NoError(t, err)
			captured := retryapproval.RunContext{Version: 1, ProjectID: "admission-project", PipelineID: "pipeline", PipelineRunID: "run", ActorID: "actor", DefinitionCommitSHA: strings.Repeat("c", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: definition, GatedJobs: []string{"build"}, Policy: policy}
			canonical, err := captured.Canonical()
			require.NoError(t, err)
			digest, err := captured.Digest()
			require.NoError(t, err)
			engine.appConf = &config.AppConfig{Pipeline: config.PipelineConfig{RetryApproval: &policy}}
			engine.offlineRuntimePolicy = taskqueue.OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowWholeJobRetryApproval: true, AllowedImages: []string{image}, MaxCPU: "1", MaxMemory: "64Mi"}
			ctx := service.WithVerifiedPipelineActor(context.Background(), "actor")
			require.NoError(t, repositories.Pipeline.Create(ctx, &model.Pipeline{PipelineID: "pipeline", ProjectID: "admission-project", IsEnabled: 1}))
			run := &model.PipelineRun{RunID: "run", PipelineID: "pipeline", TriggeredBy: "actor", TriggerType: 1, Status: model.PipelineStatusPending, Env: `{"FIRST": "one", "SECOND": "two"}`, DefinitionCommitSha: captured.DefinitionCommitSHA, DefinitionPath: captured.DefinitionPath, RetryApprovalContext: canonical, RetryApprovalContextDigest: digest}
			require.NoError(t, repositories.Pipeline.(*repo.PipelineRepo).CreateRetryApprovalRun(ctx, run))
			submitted := *run
			submitted.Env = test.env
			stored, err := repositories.Pipeline.GetRun(ctx, run.RunID)
			require.NoError(t, err)
			require.NotEqual(t, stored.Env, submitted.Env, "fixture must differ at the byte level")
			err = engine.SubmitVerified(ctx, &submitted, parsed)
			if test.admitted {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			_, registered := engine.runs.Load(run.RunID)
			require.Equal(t, test.admitted, registered)
			stored, err = repositories.Pipeline.GetRun(ctx, run.RunID)
			require.NoError(t, err)
			if test.admitted {
				require.Equal(t, model.PipelineStatusRunning, stored.Status)
				require.NotNil(t, stored.StartTime)
			} else {
				require.Equal(t, model.PipelineStatusPending, stored.Status)
				require.Nil(t, stored.StartTime)
			}
			jobs, err := repositories.JobRun.ListByPipelineRunID(ctx, run.RunID)
			require.NoError(t, err)
			require.Empty(t, jobs)
		})
	}
}
