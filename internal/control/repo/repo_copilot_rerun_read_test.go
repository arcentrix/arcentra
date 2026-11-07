// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func rerunReadFixture(t *testing.T) (*Repositories, CopilotRerunPrepareInput) {
	t.Helper()
	r, in := rerunIntentFixture(t)
	require.NoError(t, r.database.Database().AutoMigrate(&model.Organization{}, &model.Secret{}))
	return r, in
}

func TestCopilotRerunPreviewHasNoWriteAndAuthenticatesBeforeEligibility(t *testing.T) {
	r, in := rerunReadFixture(t)
	ctx := context.Background()
	db := r.database.Database()
	writes := 0
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("rerun-preview-create", func(*gorm.DB) { writes++ }))
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("rerun-preview-update", func(*gorm.DB) { writes++ }))
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("rerun-preview-delete", func(*gorm.DB) { writes++ }))
	t.Cleanup(func() {
		_ = db.Callback().Create().Remove("rerun-preview-create")
		_ = db.Callback().Update().Remove("rerun-preview-update")
		_ = db.Callback().Delete().Remove("rerun-preview-delete")
	})
	called := false
	err := r.CopilotRerunPreview(ctx, in.OwnerUserID, in.ProjectID, in.OriginalJobRunID, permitRerun, func(_ context.Context, view *Repositories, scope *CopilotRerunScope, snapshot *model.CopilotDefinitionSnapshot, evidence *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, intent *model.CopilotRerunIntent) error {
		called = true
		require.NotNil(t, view.Secret)
		require.NotNil(t, scope)
		require.NotNil(t, snapshot)
		require.NotNil(t, evidence)
		require.NotNil(t, ids)
		require.Nil(t, intent)
		return nil
	})
	require.NoError(t, err)
	require.True(t, called)
	require.Zero(t, writes)
	called = false
	deny := func(context.Context, *Repositories, *CopilotRerunScope, string, string) error {
		called = true
		return ErrCopilotRerunForbidden
	}
	err = r.CopilotRerunPreview(ctx, in.OwnerUserID, in.ProjectID, in.OriginalJobRunID, deny, func(context.Context, *Repositories, *CopilotRerunScope, *model.CopilotDefinitionSnapshot, *CopilotRerunOriginalEvidence, *CopilotRerunExecutionIdentities, *model.CopilotRerunIntent) error {
		t.Fatal("projection after denied authorization")
		return nil
	})
	require.ErrorIs(t, err, ErrCopilotRerunForbidden)
	require.True(t, called)
	require.Zero(t, writes)
}

func TestCopilotRerunRequestRecoveryKeepsOwnerAndProjectScope(t *testing.T) {
	r, in := rerunReadFixture(t)
	ctx := context.Background()
	allow := func(context.Context, *Repositories, string, *model.Project, string) error { return nil }
	_, err := r.CopilotRerunLookupRequest(ctx, in.OwnerUserID, in.ProjectID, in.RequestID, allow)
	require.ErrorIs(t, err, ErrCopilotRerunNotFound)
	prepared, err := testRerunPrepare(r, ctx, in, permitRerun)
	require.NoError(t, err)
	recovered, err := r.CopilotRerunLookupRequest(ctx, in.OwnerUserID, in.ProjectID, in.RequestID, allow)
	require.NoError(t, err)
	require.Equal(t, prepared, recovered)
	_, err = r.CopilotRerunLookupRequest(ctx, "different-owner", in.ProjectID, in.RequestID, allow)
	require.ErrorIs(t, err, ErrCopilotRerunNotFound)
	_, err = r.CopilotRerunLookupRequest(ctx, in.OwnerUserID, "different-project", in.RequestID, allow)
	require.ErrorIs(t, err, ErrCopilotRerunForbidden)
	_, err = r.CopilotRerunLookupRequest(ctx, in.OwnerUserID, in.ProjectID, in.RequestID, func(context.Context, *Repositories, string, *model.Project, string) error {
		return ErrCopilotRerunForbidden
	})
	require.ErrorIs(t, err, ErrCopilotRerunForbidden)
}

func TestCopilotRerunPreviewWrongProjectNeverLocksForeignDomain(t *testing.T) {
	r, in := rerunReadFixture(t)
	db := r.database.Database()
	ctx := context.Background()
	const foreignProject, foreignPipeline, foreignRun, foreignJob = "foreign-project", "foreign-pipeline", "foreign-run", "foreign-job"
	for _, row := range []any{
		&model.Project{ProjectID: foreignProject, IsEnabled: 1, Status: model.ProjectStatusActive},
		&model.Pipeline{PipelineID: foreignPipeline, ProjectID: foreignProject, IsEnabled: 1},
		&model.PipelineRun{RunID: foreignRun, PipelineID: foreignPipeline, ProjectID: foreignProject},
		&model.JobRun{JobRunID: foreignJob, PipelineID: foreignPipeline, PipelineRunID: foreignRun},
	} {
		require.NoError(t, db.Create(row).Error)
	}
	var lockedProjects []string
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("rerun-cross-project-lock-plan", func(q *gorm.DB) {
		_, locked := q.Statement.Clauses["FOR"]
		if q.Statement.Table != "project" || !locked {
			return
		}
		for _, value := range q.Statement.Vars {
			if id, ok := value.(string); ok && (id == in.ProjectID || id == foreignProject) {
				lockedProjects = append(lockedProjects, id)
			}
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove("rerun-cross-project-lock-plan") })
	for _, pair := range []struct{ actual, requested, job string }{
		{in.ProjectID, foreignProject, in.OriginalJobRunID},
		{foreignProject, in.ProjectID, foreignJob},
	} {
		lockedProjects = nil
		err := r.CopilotRerunPreview(ctx, in.OwnerUserID, pair.requested, pair.job, func(context.Context, *Repositories, *CopilotRerunScope, string, string) error {
			t.Fatal("foreign project must be rejected before authorization")
			return nil
		}, func(context.Context, *Repositories, *CopilotRerunScope, *model.CopilotDefinitionSnapshot, *CopilotRerunOriginalEvidence, *CopilotRerunExecutionIdentities, *model.CopilotRerunIntent) error {
			t.Fatal("foreign project must be rejected before projection")
			return nil
		})
		require.ErrorIs(t, err, ErrCopilotRerunForbidden)
		require.NotEmpty(t, lockedProjects)
		for _, locked := range lockedProjects {
			require.Equal(t, pair.actual, locked, "must never acquire the other domain's Project lock")
		}
	}
	// Both directions are witnessed without relying on a timeout or SQLite's
	// locking behavior; live MySQL concurrency still requires hosted acceptance.
}

func TestCopilotRerunProjectReadLockPlanUsesOrganizationBeforeProject(t *testing.T) {
	r, in := rerunReadFixture(t)
	db := r.database.Database()
	ctx := context.Background()
	require.NoError(t, db.Create(&model.Organization{OrgID: "read-org", IsEnabled: 1, Status: model.OrgStatusActive}).Error)
	require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", in.ProjectID).Update("org_id", "read-org").Error)
	var locked []string
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("rerun-project-lock-plan", func(q *gorm.DB) {
		if _, ok := q.Statement.Clauses["FOR"]; ok {
			locked = append(locked, q.Statement.Table)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove("rerun-project-lock-plan") })
	allow := func(ctx context.Context, view *Repositories, owner string, _ *model.Project, _ string) error {
		_, err := view.User.(IExecutionActorReader).GetExecutionActor(ctx, owner)
		return err
	}
	require.NoError(t, r.CopilotRerunReadProject(ctx, in.OwnerUserID, in.ProjectID, allow))
	require.Equal(t, []string{"organization", "project", "user"}, locked)
	locked = nil
	_, err := r.CopilotRerunLookupRequest(ctx, in.OwnerUserID, in.ProjectID, in.RequestID, allow)
	require.ErrorIs(t, err, ErrCopilotRerunNotFound)
	require.Equal(t, []string{"organization", "project", "user", "copilot_rerun_intent"}, locked)
	// This asserts the emitted locking clauses and ordering. It is not a claim
	// that SQLite proves MySQL lock compatibility or production concurrency.
}
func TestCopilotRerunMetadataProjectionDoesNotAddLateStepLocks(t *testing.T) {
	r, in := rerunReadFixture(t)
	db := r.database.Database()
	ctx := context.Background()
	steps, secretLocks := 0, 0
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("rerun-metadata-lock-plan", func(q *gorm.DB) {
		_, locked := q.Statement.Clauses["FOR"]
		if q.Statement.Table == "step_run" {
			steps++
			require.False(t, locked)
		}
		if q.Statement.Table == "secret" && locked {
			secretLocks++
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove("rerun-metadata-lock-plan") })
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		view, finish := NewScopedAuthorizationView(tx)
		metadata, err := CopilotRerunReviewRepositories(view)
		require.NoError(t, err)
		_, _, err = metadata.StepRun.List(ctx, StepRunFilter{PipelineRunID: "original-run", Page: 1, PageSize: 200})
		require.NoError(t, err)
		_, _, err = metadata.Secret.List(ctx, 1, 1000, "", "project", in.ProjectID, "")
		require.NoError(t, err)
		return finish(ctx)
	}))
	require.Positive(t, steps)
	require.Positive(t, secretLocks)
	_, err := CopilotRerunReviewRepositories(r)
	require.ErrorIs(t, err, ErrCopilotRerunInvalid)
}
