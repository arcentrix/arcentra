// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"strings"
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func rerunCaptureFixture(t *testing.T) (*PipelineRepo, *model.PipelineRun, *model.CopilotDefinitionSnapshot) {
	t.Helper()
	repository, _ := manualInboxFixture(t)
	run, input, now := rerunValueFixture(t)
	run.ProjectID = ""
	run.Branch = "main"
	run.TriggerType = int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL)
	run.RequestID = "capture-request"
	run.TotalJobs = 1
	db := repository.Database()
	require.NoError(t, db.AutoMigrate(&model.CopilotDefinitionSnapshot{}))
	require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipeline").Updates(map[string]any{"repo_url": input.DefinitionRepository, "default_branch": "main", "pipeline_file_path": input.DefinitionPath}).Error)
	require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", "project").Updates(map[string]any{"repo_url": input.DefinitionRepository, "repo_type": "git"}).Error)
	var pipeline model.Pipeline
	var project model.Project
	require.NoError(t, db.Where("pipeline_id = ?", "pipeline").First(&pipeline).Error)
	require.NoError(t, db.Where("project_id = ?", "project").First(&project).Error)
	var err error
	input.RepositoryConfigDigest, err = CopilotDefinitionRepositoryConfigDigest(&pipeline, &project)
	require.NoError(t, err)
	snapshot, err := newTestCopilotDefinitionSnapshot(run, input, now)
	require.NoError(t, err)
	return repository, run, snapshot
}

func TestCopilotRerunSnapshotAtomicVerifiedCreation(t *testing.T) {
	repository, run, snapshot := rerunCaptureFixture(t)
	require.NoError(t, repository.CreateVerifiedManualTriggerRunWithDefinition(context.Background(), "actor", run, snapshot, permitManualInbox))
	require.Empty(t, run.ProjectID, "ordinary admission still owns project pinning")
	require.Equal(t, model.RunInboxOriginVerifiedManual, run.InboxOrigin)
	var stored model.CopilotDefinitionSnapshot
	require.NoError(t, repository.Database().Where("run_id = ?", run.RunID).First(&stored).Error)
	require.Equal(t, "project", stored.ProjectID)
	require.Equal(t, snapshot.BindingDigest, stored.BindingDigest)
	require.Equal(t, snapshot.PayloadCiphertext, stored.PayloadCiphertext)
	require.NoError(t, ValidateCopilotDefinitionSnapshot(&stored))
	for _, entity := range []any{&model.PipelineRun{}, &model.InboxWork{}, &model.CopilotDefinitionSnapshot{}} {
		var count int64
		require.NoError(t, repository.Database().Model(entity).Count(&count).Error)
		require.EqualValues(t, 1, count)
	}
	// No delayed terminal-history capture API: repeating creation conflicts.
	again := *run
	again.ID = 0
	again.InboxActorID = ""
	again.InboxOrigin = 0
	require.ErrorIs(t, repository.CreateVerifiedManualTriggerRunWithDefinition(context.Background(), "actor", &again, snapshot, permitManualInbox), ErrManualTriggerConflict)
}

func TestCopilotRerunSnapshotCaptureRejectsDriftAndRollsBack(t *testing.T) {
	for _, scenario := range []string{"pipeline-repository", "pipeline-branch", "pipeline-path", "project-repository", "project-type", "project-scope", "trigger-variables", "context", "wrong-owner", "authorization", "ciphertext", "multiple-jobs", "snapshot-write-failure"} {
		t.Run(scenario, func(t *testing.T) {
			r, run, snapshot := rerunCaptureFixture(t)
			db := r.Database()
			actor := "actor"
			authorize := VerifiedManualTriggerAuthorize(permitManualInbox)
			switch scenario {
			case "pipeline-repository":
				require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipeline").Update("repo_url", "https://example.invalid/other.git").Error)
			case "pipeline-branch":
				require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipeline").Update("default_branch", "other").Error)
			case "pipeline-path":
				require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipeline").Update("pipeline_file_path", "other.yaml").Error)
			case "project-repository":
				require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", "project").Update("repo_url", "https://example.invalid/other.git").Error)
			case "project-type":
				require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", "project").Update("repo_type", "github").Error)
			case "project-scope":
				require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", "project").Update("org_id", "other-org").Error)
			case "trigger-variables":
				run.Env = `{"parameter":"changed"}`
			case "context":
				run.RetryApprovalContextDigest = "v1:" + strings.Repeat("0", 64)
			case "wrong-owner":
				actor = "other"
			case "authorization":
				authorize = func(context.Context, *Repositories, *model.Project, *model.Pipeline, string) error {
					return errors.New("denied")
				}
			case "ciphertext":
				snapshot.PayloadCiphertext[0] ^= 1
			case "multiple-jobs":
				run.TotalJobs = 2
			case "snapshot-write-failure":
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("reject_snapshot", func(tx *gorm.DB) {
					if tx.Statement.Table == "copilot_definition_snapshot" {
						tx.AddError(errors.New("synthetic storage failure"))
					}
				}))
			}
			require.Error(t, r.CreateVerifiedManualTriggerRunWithDefinition(context.Background(), actor, run, snapshot, authorize))
			require.Zero(t, run.ID)
			require.Empty(t, run.InboxActorID)
			for _, entity := range []any{&model.PipelineRun{}, &model.InboxWork{}, &model.CopilotDefinitionSnapshot{}} {
				var count int64
				require.NoError(t, db.Model(entity).Count(&count).Error)
				require.Zero(t, count, "failed capture may not leave any partial authority/provenance")
			}
		})
	}
}

func TestCopilotRerunSnapshotDoesNotBackfillOrdinaryRuns(t *testing.T) {
	r, run, snapshot := rerunCaptureFixture(t)
	// Old interface still succeeds without a snapshot table, and never creates one.
	require.NoError(t, r.Database().Migrator().DropTable(&model.CopilotDefinitionSnapshot{}))
	require.NoError(t, r.CreateVerifiedManualTriggerRun(context.Background(), "actor", run, permitManualInbox))
	require.False(t, r.Database().Migrator().HasTable(&model.CopilotDefinitionSnapshot{}))
	require.NoError(t, r.Database().AutoMigrate(&model.CopilotDefinitionSnapshot{}))
	again := *run
	again.ID = 0
	again.InboxActorID = ""
	again.InboxOrigin = 0
	require.ErrorIs(t, r.CreateVerifiedManualTriggerRunWithDefinition(context.Background(), "actor", &again, snapshot, permitManualInbox), ErrManualTriggerConflict)
	var count int64
	require.NoError(t, r.Database().Model(&model.CopilotDefinitionSnapshot{}).Count(&count).Error)
	require.Zero(t, count)
}
