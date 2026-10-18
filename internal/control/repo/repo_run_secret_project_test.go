// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

func TestRunSecretProjectPinsAtActualAdmissionAndNeverFollowsReassignment(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "manual"}[manual], func(t *testing.T) {
			r, run := automaticAdmissionRepository(t)
			admit := r.StartAutomaticRunIfPending
			if manual {
				run, admit = manualAdmissionRun(t, r), r.StartManualRunIfPending
			}
			ctx := context.Background()
			stored, err := r.GetRun(ctx, run.RunID)
			require.NoError(t, err)
			require.Empty(t, stored.ProjectID)
			_, err = r.GetRunSecretProject(ctx, run.RunID, run.PipelineID)
			require.Error(t, err, "creation alone does not grant runtime secrets")
			for _, project := range []string{"at-admission", "after-admission"} {
				require.NoError(t, r.Database().Create(&model.Project{ProjectID: project, IsEnabled: 1, Status: model.ProjectStatusActive}).Error)
			}
			require.NoError(t, r.Update(ctx, run.PipelineID, map[string]any{"project_id": "at-admission"}))
			claimed, err := admit(ctx, run)
			require.NoError(t, err)
			require.True(t, claimed)
			require.Empty(t, run.ProjectID, "admission must not mutate concurrently shared caller input")
			require.NoError(t, r.Update(ctx, run.PipelineID, map[string]any{"project_id": "after-admission"}))
			project, err := r.GetRunSecretProject(ctx, run.RunID, run.PipelineID)
			require.NoError(t, err)
			require.Equal(t, "at-admission", project)
			stored, err = r.GetRun(ctx, run.RunID)
			require.NoError(t, err)
			require.Equal(t, project, stored.ProjectID)
			byRequest, err := r.GetRunByRequestID(ctx, run.PipelineID, run.RequestID)
			require.NoError(t, err)
			require.Equal(t, project, byRequest.ProjectID)
			require.Equal(t, "{}", stored.Env, "plaintext never enters durable run inputs")
		})
	}
}

func TestRunSecretProjectRejectsCallerPinsAndGenericIdentityUpdates(t *testing.T) {
	r, run := automaticAdmissionRepository(t)
	ctx := context.Background()
	for _, field := range []string{"project_id", "ProjectID", "projectId", "pipeline_id", "PipelineID", "run_id", "RunID"} {
		require.Error(t, r.UpdateRun(ctx, run.RunID, map[string]any{field: "foreign"}))
		require.Error(t, r.UpdateRunIfStatus(ctx, run.RunID, model.PipelineStatusPending, map[string]any{field: "foreign"}))
	}
	for _, storedPin := range []bool{false, true} {
		candidate := *run
		candidate.ProjectID = "foreign"
		if storedPin {
			require.NoError(t, r.Database().Model(&model.PipelineRun{}).Where("run_id = ?", run.RunID).Update("project_id", "foreign").Error)
			candidate.ProjectID = ""
		}
		claimed, err := r.StartAutomaticRunIfPending(ctx, &candidate)
		require.NoError(t, err)
		require.False(t, claimed)
	}
	forged := &model.PipelineRun{RunID: "forged", PipelineID: run.PipelineID, ProjectID: "foreign"}
	require.Error(t, r.CreateRun(ctx, forged))
	require.Error(t, r.CreateRetryApprovalRun(ctx, forged))
}

func TestRunSecretProjectPrimaryOnlyAndLegacyFailClosed(t *testing.T) {
	r, run := automaticAdmissionRepository(t)
	ctx := context.Background()
	claimed, err := r.StartAutomaticRunIfPending(ctx, run)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, r.Database().Use(dbresolver.Register(dbresolver.Config{Replicas: []gorm.Dialector{sqlite.Open(filepath.Join(t.TempDir(), "empty-replica.db"))}})))
	project, err := r.GetRunSecretProject(ctx, run.RunID, run.PipelineID)
	require.NoError(t, err)
	require.Equal(t, "project", project)
	for _, identity := range [][2]string{{run.RunID, "foreign"}, {"missing", run.PipelineID}, {"RUN", run.PipelineID}} {
		_, err := r.GetRunSecretProject(ctx, identity[0], identity[1])
		require.Error(t, err)
	}
	require.NoError(t, r.Database().Model(&model.PipelineRun{}).Where("run_id = ?", run.RunID).Updates(map[string]any{"project_id": "", "start_time": time.Now()}).Error)
	_, err = r.GetRunSecretProject(ctx, run.RunID, run.PipelineID)
	require.Error(t, err, "legacy started runs must never infer a project from today's definition")
}

func TestPollingRunSecretProjectPinsOnlyOnSuccessfulAdmission(t *testing.T) {
	for _, start := range []bool{true, false} {
		t.Run(map[bool]string{true: "admit", false: "reject"}[start], func(t *testing.T) {
			f := newPollingFixture(t)
			_, run := f.capture(t, f.enable(t), false)
			forged := *run
			forged.ProjectID = "foreign"
			_, err := f.runs.CreatePollingRun(f.ctx, &forged)
			require.Error(t, err)
			f.create(t, run)
			stored, err := f.runs.GetRun(f.ctx, run.RunID)
			require.NoError(t, err)
			require.Empty(t, stored.ProjectID)
			if start {
				claimed, err := f.runs.StartPollingRunIfPending(f.ctx, run)
				require.NoError(t, err)
				require.True(t, claimed.ClaimedNow)
			} else {
				_, err := f.runs.RejectPendingPollingRun(f.ctx, run)
				require.NoError(t, err)
			}
			stored, err = f.runs.GetRun(f.ctx, run.RunID)
			require.NoError(t, err)
			if start {
				require.Equal(t, "project", stored.ProjectID)
			} else {
				require.Empty(t, stored.ProjectID)
			}
		})
	}
}

func TestRunSecretProjectCannotExpandCapturedRetryApprovalScope(t *testing.T) {
	r, _ := automaticAdmissionRepository(t)
	ctx := context.Background()
	policy := retryapproval.Policy{ID: "policy", Candidates: []string{"reviewer"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 60}
	policy.Fingerprint, _ = policy.Digest()
	captured := retryapproval.RunContext{Version: 1, ProjectID: "project", PipelineID: "pipeline", PipelineRunID: "gated", ActorID: "actor", DefinitionCommitSHA: strings.Repeat("a", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: "v1:" + strings.Repeat("b", 64), GatedJobs: []string{"build"}, Policy: policy}
	body, err := captured.Canonical()
	require.NoError(t, err)
	digest, err := captured.Digest()
	require.NoError(t, err)
	run := &model.PipelineRun{RunID: captured.PipelineRunID, PipelineID: captured.PipelineID, TriggeredBy: captured.ActorID, TriggerType: 1, DefinitionCommitSha: captured.DefinitionCommitSHA, DefinitionPath: captured.DefinitionPath, Status: model.PipelineStatusPending, RetryApprovalContext: body, RetryApprovalContextDigest: digest}
	require.NoError(t, r.CreateRetryApprovalRun(ctx, run))
	require.NoError(t, r.Database().Create(&model.Project{ProjectID: "foreign", IsEnabled: 1, Status: model.ProjectStatusActive}).Error)
	require.NoError(t, r.Update(ctx, run.PipelineID, map[string]any{"project_id": "foreign"}))
	claimed, err := r.StartManualRunIfPending(ctx, run)
	require.NoError(t, err)
	require.False(t, claimed)
	stored, err := r.GetRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Empty(t, stored.ProjectID)
	require.Nil(t, stored.StartTime)
}
