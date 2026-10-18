// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedManualInboxActor(t *testing.T, db *gorm.DB, actor, project string) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Team{}, &model.TeamMember{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Role{}, &model.RoleGrant{}, &model.InboxWork{}))
	role := "inbox-trigger-" + actor
	require.NoError(t, db.Create(&model.User{UserID: actor, IsEnabled: 1}).Error)
	require.NoError(t, db.Create(&model.Role{RoleID: role, IsEnabled: 1}).Error)
	require.NoError(t, db.Create(&model.RoleGrant{GrantID: role, SubjectType: "user", SubjectID: actor, RoleID: role, ScopeType: "project", ScopeID: project, IsEnabled: 1}).Error)
	for _, action := range []string{"project:read", "pipeline:read", "pipeline:trigger"} {
		parts := strings.SplitN(action, ":", 2)
		permission := role + ":" + action
		require.NoError(t, db.Create(&model.Permission{PermissionID: permission, ResourceType: parts[0], Action: parts[1]}).Error)
		require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: role, PermissionID: permission}).Error)
	}
}

func manualInboxServiceFixture(t *testing.T) (*PipelineServiceImpl, *triggerCaptureEngine, *gorm.DB) {
	adapter, repositories, _, _ := pipelineRuntimeFixture(t, pipelineRuntimeSpec())
	seedManualInboxActor(t, adapter.db, "actor", "runtime-project")
	seedManualInboxActor(t, adapter.db, "other", "runtime-project")
	engine := &triggerCaptureEngine{}
	return &PipelineServiceImpl{pipelineRepo: repositories.Pipeline, projectRepo: repositories.Project, engine: engine, offlineRuntimePolicy: pipelineRuntimePolicy()}, engine, adapter.db
}

func TestManualInboxServiceVerifiedContextReplayAndNoBodyAuthority(t *testing.T) {
	svc, engine, db := manualInboxServiceFixture(t)
	req := &pipelinev1.TriggerPipelineRequest{PipelineId: "runtime-pipeline", RequestId: "one", TriggeredBy: "actor", Variables: map[string]string{"X": "one"}}
	ctx := WithVerifiedPipelineActor(context.Background(), "actor")
	response, err := svc.TriggerPipeline(ctx, req)
	require.NoError(t, err)
	require.True(t, response.Success, response.Message)
	require.Equal(t, 1, engine.calls)
	stored, err := svc.pipelineRepo.GetRun(ctx, response.RunId)
	require.NoError(t, err)
	require.Equal(t, "actor", stored.InboxActorID)
	require.Equal(t, model.RunInboxOriginVerifiedManual, stored.InboxOrigin)
	require.Equal(t, stored.InboxActorID, engine.run.InboxActorID)
	replay, err := svc.TriggerPipeline(ctx, req)
	require.NoError(t, err)
	require.True(t, replay.Success)
	require.Equal(t, response.RunId, replay.RunId)
	require.Equal(t, 1, engine.calls)
	req.RequestId = "One"
	alias, err := svc.TriggerPipeline(ctx, req)
	require.NoError(t, err)
	require.False(t, alias.Success)
	require.EqualValues(t, 409, alias.GetError().GetCode())
	require.Equal(t, repo.ErrManualTriggerConflict.Error(), alias.Message)
	req.RequestId = "one"
	req.TriggeredBy = "other"
	denied, err := svc.TriggerPipeline(WithVerifiedPipelineActor(context.Background(), "other"), req)
	require.NoError(t, err)
	require.False(t, denied.Success)
	require.EqualValues(t, 409, denied.GetError().GetCode())
	require.Equal(t, repo.ErrManualTriggerConflict.Error(), denied.Message)
	denied, err = svc.TriggerPipeline(ctx, req)
	require.NoError(t, err)
	require.False(t, denied.Success)
	require.EqualValues(t, 403, denied.GetError().GetCode())
	req.TriggeredBy = "actor"
	denied, err = svc.TriggerPipeline(context.Background(), req)
	require.NoError(t, err)
	require.False(t, denied.Success)
	require.EqualValues(t, 409, denied.GetError().GetCode())
	req.Variables["X"] = "different"
	denied, err = svc.TriggerPipeline(ctx, req)
	require.NoError(t, err)
	require.False(t, denied.Success)
	require.EqualValues(t, 409, denied.GetError().GetCode())
	require.Equal(t, 1, engine.calls)
	var count int64
	require.NoError(t, db.Model(&model.InboxWork{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestManualInboxServiceMissingContextStaysUnboundAndNeverAdopted(t *testing.T) {
	svc, engine, db := manualInboxServiceFixture(t)
	req := &pipelinev1.TriggerPipelineRequest{PipelineId: "runtime-pipeline", RequestId: "legacy", TriggeredBy: "actor"}
	response, err := svc.TriggerPipeline(context.Background(), req)
	require.NoError(t, err)
	require.True(t, response.Success, response.Message)
	stored, err := svc.pipelineRepo.GetRun(context.Background(), response.RunId)
	require.NoError(t, err)
	require.Empty(t, stored.InboxActorID)
	require.Zero(t, stored.InboxOrigin)
	denied, err := svc.TriggerPipeline(WithVerifiedPipelineActor(context.Background(), "actor"), req)
	require.NoError(t, err)
	require.False(t, denied.Success)
	require.EqualValues(t, 409, denied.GetError().GetCode())
	require.Equal(t, 1, engine.calls)
	var count int64
	require.NoError(t, db.Model(&model.InboxWork{}).Count(&count).Error)
	require.Zero(t, count)
	require.Equal(t, " actor ", VerifiedPipelineActor(WithVerifiedPipelineActor(context.Background(), " actor ")))
}

func TestManualInboxServiceLiveAuthorizationRevocation(t *testing.T) {
	for _, change := range []string{"grant", "expiry", "role", "permission", "user"} {
		t.Run(change, func(t *testing.T) {
			svc, engine, db := manualInboxServiceFixture(t)
			switch change {
			case "grant":
				require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "inbox-trigger-actor").Update("is_enabled", 0).Error)
			case "expiry":
				require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "inbox-trigger-actor").Update("expires_at", time.Now().Add(-time.Second)).Error)
			case "role":
				require.NoError(t, db.Model(&model.Role{}).Where("role_id = ?", "inbox-trigger-actor").Update("is_enabled", 0).Error)
			case "permission":
				require.NoError(t, db.Where("role_id = ? AND permission_id = ?", "inbox-trigger-actor", "inbox-trigger-actor:pipeline:trigger").Delete(&model.RolePermissionBinding{}).Error)
			case "user":
				require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", "actor").Update("is_enabled", 0).Error)
			}
			response, err := svc.TriggerPipeline(WithVerifiedPipelineActor(context.Background(), "actor"), &pipelinev1.TriggerPipelineRequest{PipelineId: "runtime-pipeline", RequestId: "denied", TriggeredBy: "actor"})
			require.NoError(t, err)
			require.False(t, response.Success)
			require.Zero(t, engine.calls)
			var count int64
			require.NoError(t, db.Model(&model.PipelineRun{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

type unknownManualCreation struct {
	repo.IPipelineRepository
	creator repo.IVerifiedManualTriggerCreator
	lookups int
}

func (r *unknownManualCreation) GetRunByRequestID(ctx context.Context, pipeline, request string) (*model.PipelineRun, error) {
	r.lookups++
	if r.lookups == 1 {
		return nil, nil
	}
	return r.IPipelineRepository.GetRunByRequestID(ctx, pipeline, request)
}
func (r *unknownManualCreation) CreateVerifiedManualTriggerRun(ctx context.Context, actor string, run *model.PipelineRun, authorize repo.VerifiedManualTriggerAuthorize) error {
	if err := r.creator.CreateVerifiedManualTriggerRun(ctx, actor, run, authorize); err != nil {
		return err
	}
	return errors.New("synthetic lost commit acknowledgement")
}

func TestManualInboxServiceUnknownCreationCommitNeverResubmits(t *testing.T) {
	svc, engine, db := manualInboxServiceFixture(t)
	svc.pipelineRepo = &unknownManualCreation{IPipelineRepository: svc.pipelineRepo, creator: svc.pipelineRepo.(repo.IVerifiedManualTriggerCreator)}
	ctx := WithVerifiedPipelineActor(context.Background(), "actor")
	req := &pipelinev1.TriggerPipelineRequest{PipelineId: "runtime-pipeline", RequestId: "unknown", TriggeredBy: "actor"}
	response, err := svc.TriggerPipeline(ctx, req)
	require.NoError(t, err)
	require.True(t, response.Success, response.Message)
	require.NotEmpty(t, response.RunId)
	require.Zero(t, engine.calls)
	replay, err := svc.TriggerPipeline(ctx, req)
	require.NoError(t, err)
	require.True(t, replay.Success)
	require.Equal(t, response.RunId, replay.RunId)
	require.Zero(t, engine.calls)
	var count int64
	require.NoError(t, db.Model(&model.InboxWork{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestManualInboxServiceTransactionLockDirection(t *testing.T) {
	svc, _, db := manualInboxServiceFixture(t)
	var tables []string
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("manual-inbox:lock-order", func(tx *gorm.DB) {
		if _, locked := tx.Statement.Clauses["FOR"]; locked {
			tables = append(tables, tx.Statement.Table)
		}
	}))
	run := &model.PipelineRun{RunID: "lock-order", PipelineID: "runtime-pipeline", RequestID: "lock-order", TriggerType: 1, TriggeredBy: "actor", Status: model.PipelineStatusPending, Env: "{}"}
	require.NoError(t, createPipelineTriggerRun(WithVerifiedPipelineActor(context.Background(), "actor"), svc.pipelineRepo, run))
	require.GreaterOrEqual(t, len(tables), 7)
	require.Equal(t, []string{"pipeline", "project"}, tables[:2])
	for _, table := range tables[2:] {
		require.NotEqual(t, "pipeline", table, "pipeline lock cannot follow project/authorization locks")
	}
	permission, grant := -1, -1
	for i, table := range tables {
		if table == "permission" {
			permission = i
		}
		if table == "role_grant" {
			grant = i
		}
	}
	require.Greater(t, grant, permission)
	require.Greater(t, permission, 2)
}

func TestManualInboxServiceTeamGrantRefreshAndExactness(t *testing.T) {
	for _, change := range []string{"valid", "removed-membership", "disabled-team", "duplicate-team", "case-alias-team", "case-alias-membership"} {
		t.Run(change, func(t *testing.T) {
			svc, engine, db := manualInboxServiceFixture(t)
			require.NoError(t, db.Create(&model.Team{TeamID: "inbox-team", IsEnabled: 1}).Error)
			require.NoError(t, db.Create(&model.TeamMember{TeamID: "inbox-team", UserID: "actor"}).Error)
			require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "inbox-trigger-actor").Updates(map[string]any{"subject_type": "team", "subject_id": "inbox-team"}).Error)
			switch change {
			case "removed-membership":
				require.NoError(t, db.Where("team_id = ? AND user_id = ?", "inbox-team", "actor").Delete(&model.TeamMember{}).Error)
			case "disabled-team":
				require.NoError(t, db.Model(&model.Team{}).Where("team_id = ?", "inbox-team").Update("is_enabled", 0).Error)
			case "duplicate-team":
				require.NoError(t, db.Create(&model.Team{TeamID: "inbox-team", IsEnabled: 1}).Error)
			case "case-alias-team":
				require.NoError(t, db.Create(&model.Team{TeamID: "Inbox-team", IsEnabled: 1}).Error)
			case "case-alias-membership":
				require.NoError(t, db.Create(&model.TeamMember{TeamID: "inbox-team", UserID: "Actor"}).Error)
			}
			response, err := svc.TriggerPipeline(WithVerifiedPipelineActor(context.Background(), "actor"), &pipelinev1.TriggerPipelineRequest{PipelineId: "runtime-pipeline", RequestId: "team", TriggeredBy: "actor"})
			require.NoError(t, err)
			if change == "valid" {
				require.True(t, response.Success, response.Message)
				require.Equal(t, 1, engine.calls)
			} else {
				require.False(t, response.Success)
				require.Zero(t, engine.calls)
			}
		})
	}
}

func TestManualInboxSubjectPreservesInfrastructureFailures(t *testing.T) {
	for _, table := range []string{"user", "team_member", "team"} {
		t.Run(table, func(t *testing.T) {
			_, _, db := manualInboxServiceFixture(t)
			require.NoError(t, db.Create(&model.Team{TeamID: "failure-team", IsEnabled: 1}).Error)
			require.NoError(t, db.Create(&model.TeamMember{TeamID: "failure-team", UserID: "actor"}).Error)
			failure := errors.New("injected authorization store unavailable")
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("manual-inbox:infra", func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					tx.AddError(failure)
				}
			}))
			err := db.Transaction(func(tx *gorm.DB) error {
				view, _ := repo.NewScopedAuthorizationView(tx)
				_, err := liveManualInboxSubject(context.Background(), view, "actor")
				return err
			})
			require.ErrorIs(t, err, failure)
			require.NotErrorIs(t, err, repo.ErrManualTriggerForbidden)
		})
	}
}
