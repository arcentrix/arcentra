// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/dbresolver"
)

func manualInboxFixture(t *testing.T) (*PipelineRepo, *model.PipelineRun) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "manual-inbox.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.Pipeline{}, &model.Project{}, &model.PipelineRun{}, &model.JobRun{}, &model.User{}, &model.RoleGrant{}, &model.InboxWork{}))
	for _, row := range []any{
		&model.Pipeline{PipelineID: "pipeline", ProjectID: "project", IsEnabled: 1, Metadata: "{}"},
		&model.Project{ProjectID: "project", IsEnabled: 1, Status: model.ProjectStatusActive},
		&model.User{UserID: "actor", IsEnabled: 1},
	} {
		require.NoError(t, db.Create(row).Error)
	}
	return &PipelineRepo{IDatabase: statusTestDB{db}}, &model.PipelineRun{RunID: "manual", PipelineID: "pipeline", RequestID: "request", TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL), TriggeredBy: "actor", Status: model.PipelineStatusPending, Env: "{}"}
}

func permitManualInbox(context.Context, *Repositories, *model.Project, *model.Pipeline, string) error {
	return nil
}

func TestManualInboxProvenanceCreationAdmissionAndPrivateProjection(t *testing.T) {
	r, run := manualInboxFixture(t)
	ctx := context.Background()
	require.NoError(t, r.CreateVerifiedManualTriggerRun(ctx, "actor", run, permitManualInbox))
	require.Equal(t, "actor", run.InboxActorID)
	require.Equal(t, model.RunInboxOriginVerifiedManual, run.InboxOrigin)
	require.Empty(t, run.ProjectID)
	for _, read := range []func() (*model.PipelineRun, error){func() (*model.PipelineRun, error) { return r.GetRun(ctx, run.RunID) }, func() (*model.PipelineRun, error) { return r.GetRunByRequestID(ctx, run.PipelineID, run.RequestID) }} {
		stored, err := read()
		require.NoError(t, err)
		require.Equal(t, run.InboxActorID, stored.InboxActorID)
		require.Equal(t, run.InboxOrigin, stored.InboxOrigin)
		b, err := json.Marshal(stored)
		require.NoError(t, err)
		require.NotContains(t, string(b), "InboxActor")
		require.NotContains(t, string(b), "inboxActor")
		require.NotContains(t, string(b), "inbox_origin")
	}
	var work []model.InboxWork
	require.NoError(t, r.Database().Find(&work).Error)
	require.Len(t, work, 1)
	require.Equal(t, "run", work[0].SourceKind)
	require.Equal(t, run.RunID, work[0].SourceID)
	wrong := *run
	wrong.InboxActorID = "other"
	ok, err := r.StartManualRunIfPending(ctx, &wrong)
	require.NoError(t, err)
	require.False(t, ok)
	wrong = *run
	wrong.InboxOrigin = 0
	ok, err = r.StartManualRunIfPending(ctx, &wrong)
	require.NoError(t, err)
	require.False(t, ok)
	wrong = *run
	wrong.InboxActorID = ""
	wrong.InboxOrigin = 0
	ok, err = r.StartManualRunIfPending(ctx, &wrong)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = r.StartManualRunIfPending(ctx, run)
	require.NoError(t, err)
	require.True(t, ok)
	stored, err := r.GetRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, "project", stored.ProjectID)
	require.Equal(t, "actor", stored.InboxActorID)
	require.NotNil(t, stored.StartTime)
}

func TestManualInboxProvenanceRejectsUntrustedCreationAndAliases(t *testing.T) {
	r, fresh := manualInboxFixture(t)
	ctx := context.Background()
	now := time.Now()
	invalid := map[string]func(*model.PipelineRun){
		"actor": func(r *model.PipelineRun) { r.InboxActorID = "actor" }, "origin": func(r *model.PipelineRun) { r.InboxOrigin = 1 },
		"automatic": func(r *model.PipelineRun) { r.TriggerType = 2 }, "running": func(r *model.PipelineRun) { r.Status = model.PipelineStatusRunning },
		"project": func(r *model.PipelineRun) { r.ProjectID = "project" }, "start": func(r *model.PipelineRun) { r.StartTime = &now }, "end": func(r *model.PipelineRun) { r.EndTime = &now },
		"polling": func(r *model.PipelineRun) { r.PollingContext = "{}" }, "polling-digest": func(r *model.PipelineRun) { r.PollingContextDigest = "digest" },
		"forged-actor": func(r *model.PipelineRun) { r.TriggeredBy = "other" }, "empty-run": func(r *model.PipelineRun) { r.RunID = "" },
		"request-alias": func(r *model.PipelineRun) { r.RequestID = " request" }, "pipeline-alias": func(r *model.PipelineRun) { r.PipelineID = "PIPELINE" }, "row-id": func(r *model.PipelineRun) { r.ID = 9 },
		"retry-forgery": func(r *model.PipelineRun) { r.RetryApprovalContext = "{}" }, "deployment-forgery": func(r *model.PipelineRun) { r.DeploymentActorID = "actor" },
	}
	for name, alter := range invalid {
		t.Run(name, func(t *testing.T) {
			run := *fresh
			alter(&run)
			require.Error(t, r.CreateVerifiedManualTriggerRun(ctx, "actor", &run, permitManualInbox))
			require.Empty(t, fresh.InboxActorID)
		})
	}
	for _, actor := range []string{"", " actor", "Actor", "missing"} {
		require.Error(t, r.CreateVerifiedManualTriggerRun(ctx, actor, fresh, permitManualInbox))
	}
	require.Error(t, r.CreateVerifiedManualTriggerRun(ctx, "actor", fresh, nil))
	for _, bind := range []func(*model.PipelineRun){func(r *model.PipelineRun) { r.InboxActorID = "actor" }, func(r *model.PipelineRun) { r.InboxOrigin = 1 }} {
		run := *fresh
		bind(&run)
		require.Error(t, r.CreateRun(ctx, &run))
		require.Error(t, r.CreateRetryApprovalRun(ctx, &run))
		_, err := r.CreatePollingRun(ctx, &run)
		require.Error(t, err)
	}
	for _, field := range []string{"inbox_actor_id", "InboxActorID", "inboxActorId", "INBOX_ACTOR_ID", "InBoXaCtOrId", "inbox_origin", "InboxOrigin", "INBOXORIGIN"} {
		require.Error(t, r.UpdateRun(ctx, fresh.RunID, map[string]any{field: "other"}))
		require.Error(t, r.UpdateRunIfStatus(ctx, fresh.RunID, model.PipelineStatusPending, map[string]any{field: "other"}))
	}
	var count int64
	require.NoError(t, r.Database().Model(&model.PipelineRun{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestManualInboxProvenanceExactEnabledActorAndActiveScope(t *testing.T) {
	for _, kind := range []string{"disabled-user", "duplicate-user", "case-user", "case-pipeline", "case-project", "disabled-pipeline", "deleted-pipeline", "archived-project", "disabled-project", "denied"} {
		t.Run(kind, func(t *testing.T) {
			r, run := manualInboxFixture(t)
			db := r.Database()
			authorize := VerifiedManualTriggerAuthorize(permitManualInbox)
			switch kind {
			case "disabled-user":
				require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", "actor").Update("is_enabled", 0).Error)
			case "duplicate-user":
				require.NoError(t, db.Create(&model.User{UserID: "actor", IsEnabled: 1}).Error)
			case "case-user":
				require.NoError(t, db.Create(&model.User{UserID: "Actor", IsEnabled: 1}).Error)
			case "case-pipeline":
				require.NoError(t, db.Create(&model.Pipeline{PipelineID: "Pipeline", ProjectID: "project", IsEnabled: 1, Metadata: "{}"}).Error)
			case "case-project":
				require.NoError(t, db.Create(&model.Project{ProjectID: "Project", IsEnabled: 1, Status: model.ProjectStatusActive}).Error)
			case "disabled-pipeline":
				require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipeline").Update("is_enabled", 0).Error)
			case "deleted-pipeline":
				require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipeline").Update("is_deleted", 1).Error)
			case "archived-project":
				require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", "project").Update("status", model.ProjectStatusArchived).Error)
			case "disabled-project":
				require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", "project").Update("is_enabled", 0).Error)
			case "denied":
				authorize = func(context.Context, *Repositories, *model.Project, *model.Pipeline, string) error {
					return errors.New("denied")
				}
			}
			require.ErrorIs(t, r.CreateVerifiedManualTriggerRun(context.Background(), "actor", run, authorize), ErrManualTriggerForbidden)
			var count int64
			require.NoError(t, db.Model(&model.InboxWork{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestManualInboxProvenanceAtomicWorkRollbackAndExpiry(t *testing.T) {
	for _, failure := range []string{"work", "expiry"} {
		t.Run(failure, func(t *testing.T) {
			r, run := manualInboxFixture(t)
			db := r.Database()
			authorize := VerifiedManualTriggerAuthorize(permitManualInbox)
			if failure == "work" {
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("inbox:reject_work", func(tx *gorm.DB) {
					if tx.Statement.Table == "inbox_work" {
						tx.AddError(errors.New("injected work failure"))
					}
				}))
			} else {
				expires := time.Now().Add(time.Second)
				require.NoError(t, db.Create(&model.RoleGrant{GrantID: "grant", ExpiresAt: &expires, IsEnabled: 1}).Error)
				authorize = func(ctx context.Context, view *Repositories, _ *model.Project, _ *model.Pipeline, _ string) error {
					_, err := view.RoleGrant.ListAllEnabled(ctx)
					return err
				}
				require.NoError(t, db.Callback().Create().After("gorm:create").Register("inbox:expire", func(tx *gorm.DB) {
					if tx.Statement.Table == "inbox_work" {
						time.Sleep(time.Until(expires) + 10*time.Millisecond)
					}
				}))
			}
			require.Error(t, r.CreateVerifiedManualTriggerRun(context.Background(), "actor", run, authorize))
			require.Empty(t, run.InboxActorID)
			for _, row := range []any{&model.PipelineRun{}, &model.InboxWork{}} {
				var count int64
				require.NoError(t, db.Model(row).Count(&count).Error)
				require.Zero(t, count)
			}
		})
	}
}

func TestManualInboxProvenanceUnknownCommitAndConcurrentRequest(t *testing.T) {
	r, run := manualInboxFixture(t)
	pool, err := r.Database().DB()
	require.NoError(t, err)
	db, err := gorm.Open(sqlite.New(sqlite.Config{Conn: &pollingUnknownCommitPool{DB: pool}}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	faulty := &PipelineRepo{IDatabase: statusTestDB{db}}
	require.Error(t, faulty.CreateVerifiedManualTriggerRun(context.Background(), "actor", run, permitManualInbox))
	require.Empty(t, run.InboxActorID)
	stored, err := r.GetRunByRequestID(context.Background(), run.PipelineID, run.RequestID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, "actor", stored.InboxActorID)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			attempt := *run
			_ = r.CreateVerifiedManualTriggerRun(context.Background(), "actor", &attempt, permitManualInbox)
		}()
	}
	wg.Wait()
	for _, row := range []any{&model.PipelineRun{}, &model.InboxWork{}} {
		var count int64
		require.NoError(t, r.Database().Model(row).Count(&count).Error)
		require.EqualValues(t, 1, count)
	}
}

func TestManualInboxProvenancePrimaryAndNoLegacyAdoption(t *testing.T) {
	r, run := manualInboxFixture(t)
	ctx := context.Background()
	require.NoError(t, r.CreateRun(ctx, run))
	require.NoError(t, r.Database().Use(dbresolver.Register(dbresolver.Config{Replicas: []gorm.Dialector{sqlite.Open(filepath.Join(t.TempDir(), "empty.db"))}})))
	stored, err := r.GetRunByRequestID(ctx, run.PipelineID, run.RequestID)
	require.NoError(t, err)
	require.Empty(t, stored.InboxActorID)
	candidate := *run
	candidate.ID = 0
	candidate.RunID = "another"
	require.Error(t, r.CreateVerifiedManualTriggerRun(ctx, "actor", &candidate, permitManualInbox))
	stored, err = r.GetRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Empty(t, stored.InboxActorID)
	require.Zero(t, stored.InboxOrigin)
	require.Error(t, r.CreateVerifiedManualTriggerRun(ctx, strings.Repeat("a", 65), &candidate, permitManualInbox))
}

func TestManualInboxProvenancePreservesExactRetryCapture(t *testing.T) {
	for _, mutation := range []string{"valid", "actor", "project", "pipeline", "run", "definition", "path", "digest"} {
		t.Run(mutation, func(t *testing.T) {
			r, run := manualInboxFixture(t)
			policy := retryapproval.Policy{ID: "policy", Candidates: []string{"reviewer"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 60}
			policy.Fingerprint, _ = policy.Digest()
			capture := retryapproval.RunContext{Version: 1, ProjectID: "project", PipelineID: "pipeline", PipelineRunID: run.RunID, ActorID: "actor", DefinitionCommitSHA: strings.Repeat("a", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: "v1:" + strings.Repeat("b", 64), GatedJobs: []string{"build"}, Policy: policy}
			run.DefinitionCommitSha, run.DefinitionPath = capture.DefinitionCommitSHA, capture.DefinitionPath
			switch mutation {
			case "actor":
				capture.ActorID = "other"
			case "project":
				capture.ProjectID = "other"
			case "pipeline":
				capture.PipelineID = "other"
			case "run":
				capture.PipelineRunID = "other"
			case "definition":
				capture.DefinitionCommitSHA = strings.Repeat("b", 40)
			case "path":
				capture.DefinitionPath = "other.yaml"
			}
			var err error
			run.RetryApprovalContext, err = capture.Canonical()
			require.NoError(t, err)
			run.RetryApprovalContextDigest, err = capture.Digest()
			require.NoError(t, err)
			if mutation == "digest" {
				run.RetryApprovalContextDigest = "v1:" + strings.Repeat("c", 64)
			}
			body, digest := run.RetryApprovalContext, run.RetryApprovalContextDigest
			err = r.CreateVerifiedManualTriggerRun(context.Background(), "actor", run, permitManualInbox)
			if mutation != "valid" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			stored, err := r.GetRun(context.Background(), run.RunID)
			require.NoError(t, err)
			require.Equal(t, "actor", stored.InboxActorID)
			require.Equal(t, body, stored.RetryApprovalContext)
			require.Equal(t, digest, stored.RetryApprovalContextDigest)
		})
	}
}

func TestManualInboxProvenanceRejectsCaseAliasRunAndRequest(t *testing.T) {
	for _, alias := range []string{"run", "request"} {
		t.Run(alias, func(t *testing.T) {
			r, run := manualInboxFixture(t)
			ctx := context.Background()
			old := *run
			if alias == "run" {
				old.RunID = "Manual"
				old.RequestID = "other-request"
			} else {
				old.RunID = "other-run"
				old.RequestID = "Request"
			}
			require.NoError(t, r.CreateRun(ctx, &old))
			require.ErrorIs(t, r.CreateVerifiedManualTriggerRun(ctx, "actor", run, permitManualInbox), ErrManualTriggerConflict)
			if alias == "request" {
				_, err := r.GetRunByRequestID(ctx, run.PipelineID, run.RequestID)
				require.ErrorIs(t, err, ErrManualTriggerConflict)
			}
			var count int64
			require.NoError(t, r.Database().Model(&model.PipelineRun{}).Count(&count).Error)
			require.EqualValues(t, 1, count)
			require.NoError(t, r.Database().Model(&model.InboxWork{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}
