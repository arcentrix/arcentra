// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func inboxServiceSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&model.InboxDelivery{}, &model.InboxWork{}, &model.InboxCursor{}, &model.InboxSweep{}, &model.ApprovalRequest{}))
}
func inboxServiceDue(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Model(&model.InboxWork{}).Where("state = 0").UpdateColumn("next_check_at", time.Now().Add(-time.Minute)).Error)
}
func TestInboxServiceLivePermissionsArchiveMoveAndRestart(t *testing.T) {
	svc, _, db := manualInboxServiceFixture(t)
	inboxServiceSchema(t, db)
	ctx := WithVerifiedPipelineActor(context.Background(), "actor")
	resp, err := svc.TriggerPipeline(ctx, &pipelinev1.TriggerPipelineRequest{PipelineId: "runtime-pipeline", RequestId: "inbox-run", TriggeredBy: "actor"})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Message)
	run, err := svc.pipelineRepo.GetRun(ctx, resp.RunId)
	require.NoError(t, err)
	admitted, err := svc.pipelineRepo.(*repo.PipelineRepo).StartManualRunIfPending(ctx, run)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NoError(t, svc.pipelineRepo.UpdateRun(ctx, run.RunID, map[string]any{"status": model.PipelineStatusSuccess, "end_time": time.Now()}))
	repositories := repo.NewRepositories(artifactServiceDatabase{db}, nil)
	inbox := NewInboxService(repositories)
	_, err = inbox.ReconcileOnce(ctx)
	require.NoError(t, err)
	page, err := inbox.List(ctx, "actor", "all", "", 20)
	require.NoError(t, err)
	require.Len(t, page.List, 1)
	read, err := inbox.MarkRead(ctx, "actor", page.List[0].ID)
	require.NoError(t, err)
	inbox.Start(ctx)
	inbox.Stop()
	restarted := NewInboxService(repositories)
	page, err = restarted.List(ctx, "actor", "all", "", 20)
	require.NoError(t, err)
	require.Equal(t, read.ReadAt, *page.List[0].ReadAt)
	// History survives archive and does not require trigger permission.
	require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", "runtime-project").Update("status", model.ProjectStatusArchived).Error)
	require.NoError(t, db.Where("permission_id = ?", "inbox-trigger-actor:pipeline:trigger").Delete(&model.RolePermissionBinding{}).Error)
	page, err = inbox.List(ctx, "actor", "all", "", 20)
	require.NoError(t, err)
	require.Len(t, page.List, 1)
	require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "runtime-pipeline").Update("project_id", "moved").Error)
	page, err = inbox.List(ctx, "actor", "all", "", 20)
	require.NoError(t, err)
	require.Len(t, page.List, 1)
	require.Nil(t, page.List[0].Target)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "inbox-trigger-actor").Update("is_enabled", 0).Error)
	page, err = inbox.List(ctx, "actor", "all", "", 20)
	require.NoError(t, err)
	require.Empty(t, page.List)
	count, err := inbox.UnreadCount(ctx, "actor")
	require.NoError(t, err)
	require.Zero(t, count.Count)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "inbox-trigger-actor").Updates(map[string]any{"is_enabled": 1, "expires_at": time.Now().Add(-time.Second)}).Error)
	page, err = inbox.List(ctx, "actor", "all", "", 20)
	require.NoError(t, err)
	require.Empty(t, page.List)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "inbox-trigger-actor").Update("expires_at", nil).Error)
	page, err = inbox.List(ctx, "actor", "all", "", 20)
	require.NoError(t, err)
	require.Len(t, page.List, 1)
}

// Existing deployment execution fixtures are synthetic. Replace their old run
// through the actual dedicated writer and admission boundary before preparing
// any release/approval; no test manufactures approval rows or actor bindings.
func trustedInboxDeploymentFixture(t *testing.T) (*DeploymentService, *gorm.DB, PipelineDeploymentExecution, *releaseFakeAdapter, *ApprovalService, *InboxService) {
	t.Helper()
	s, db, execution, adapter, approvals := deploymentApprovalFixture(t)
	inboxServiceSchema(t, db)
	for _, actor := range []string{"user", "reviewer"} {
		seedManualInboxActor(t, db, actor, "project")
		role := "inbox-trigger-" + actor
		permission := role + ":approval:read"
		require.NoError(t, db.Create(&model.Permission{PermissionID: permission, ResourceType: "approval", Action: "read"}).Error)
		require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: role, PermissionID: permission}).Error)
	}
	old, err := s.pipelines.GetRun(context.Background(), execution.PipelineRunID)
	require.NoError(t, err)
	var jobs []model.JobRun
	require.NoError(t, db.Where("pipeline_run_id = ?", old.RunID).Find(&jobs).Error)
	require.NoError(t, db.Where("pipeline_run_id = ?", old.RunID).Delete(&model.JobRun{}).Error)
	require.NoError(t, db.Where("run_id = ?", old.RunID).Delete(&model.PipelineRun{}).Error)
	old.BaseModel = model.BaseModel{}
	old.ProjectID = ""
	old.StartTime = nil
	old.EndTime = nil
	old.Status = model.PipelineStatusPending
	old.RequestID = "inbox-deployment"
	writer := s.pipelines.(*repo.PipelineRepo)
	require.NoError(t, writer.CreateVerifiedManualTriggerRun(context.Background(), "user", old, authorizeManualInboxTrigger))
	admitted, err := writer.StartManualRunIfPending(context.Background(), old)
	require.NoError(t, err)
	require.True(t, admitted)
	for i := range jobs {
		require.NoError(t, db.Create(&jobs[i]).Error)
	}
	return s, db, execution, adapter, approvals, NewInboxService(repo.NewRepositories(artifactServiceDatabase{db}, nil))
}
func TestInboxDeploymentServiceActualApprovalRequestDecisionAndRevocation(t *testing.T) {
	s, db, execution, adapter, approvals, inbox := trustedInboxDeploymentFixture(t)
	ctx := context.Background()
	entry, err := s.PreparePipelineRelease(ctx, execution)
	require.NoError(t, err)
	require.NotNil(t, entry.ApprovalID)
	_, err = inbox.ReconcileOnce(ctx)
	require.NoError(t, err)
	page, err := inbox.List(ctx, "reviewer", "all", "", 20)
	require.NoError(t, err)
	require.Len(t, page.List, 1)
	require.Equal(t, "approval_requested", page.List[0].Kind)
	require.Equal(t, "pending", page.List[0].Status)
	requester, err := inbox.List(ctx, "user", "all", "", 20)
	require.NoError(t, err)
	require.Empty(t, requester.List)
	require.NoError(t, approvals.Approve(ctx, *entry.ApprovalID, "reviewer", "synthetic-private-decision"))
	inboxServiceDue(t, db)
	_, err = inbox.ReconcileOnce(ctx)
	require.NoError(t, err)
	requester, err = inbox.List(ctx, "user", "all", "", 20)
	require.NoError(t, err)
	require.Len(t, requester.List, 1)
	require.Equal(t, "approval_result", requester.List[0].Kind)
	require.Equal(t, "approved", requester.List[0].Status)
	page, err = inbox.List(ctx, "reviewer", "all", "", 20)
	require.NoError(t, err)
	require.Len(t, page.List, 1)
	require.Equal(t, "approved", page.List[0].Status)
	// Ended execution does not relabel a genuine approved historical decision.
	require.NoError(t, s.pipelines.UpdateRun(ctx, execution.PipelineRunID, map[string]any{"status": model.PipelineStatusCancelled, "end_time": time.Now()}))
	page, err = inbox.List(ctx, "reviewer", "all", "", 20)
	require.NoError(t, err)
	require.Equal(t, "approved", page.List[0].Status)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "inbox-trigger-reviewer").Update("is_enabled", 0).Error)
	page, err = inbox.List(ctx, "reviewer", "all", "", 20)
	require.NoError(t, err)
	require.Empty(t, page.List)
	count, err := inbox.UnreadCount(ctx, "reviewer")
	require.NoError(t, err)
	require.Zero(t, count.Count)
	require.Zero(t, adapter.inspect+adapter.apply+adapter.observe, "Inbox must never contact external deployment adapter")
}
func TestInboxDeploymentCancellationAndPendingDeadlineAreAuthoritative(t *testing.T) {
	s, db, execution, _, _, inbox := trustedInboxDeploymentFixture(t)
	ctx := context.Background()
	entry, err := s.PreparePipelineRelease(ctx, execution)
	require.NoError(t, err)
	// Advance wall-clock using a correctly re-hashed immutable synthetic fixture
	// only to test the renderer; expiry itself remains with ApprovalRepo.Expire.
	var a model.ApprovalRequest
	require.NoError(t, db.Where("approval_id = ?", *entry.ApprovalID).First(&a).Error)
	past := time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond)
	a.ExpiresAt = &past
	entry.ApprovalEvidenceHash = repo.InboxDeploymentEvidence(entry, &a)
	a.EvidenceHash = entry.ApprovalEvidenceHash
	require.NoError(t, db.Model(&model.ApprovalRequest{}).Where("approval_id = ?", a.ApprovalID).Updates(map[string]any{"expires_at": past, "evidence_hash": a.EvidenceHash}).Error)
	require.NoError(t, db.Model(&model.DeploymentRelease{}).Where("release_id = ?", entry.ReleaseID).Update("approval_evidence_hash", a.EvidenceHash).Error)
	_, err = inbox.ReconcileOnce(ctx)
	require.NoError(t, err)
	page, err := inbox.List(ctx, "reviewer", "all", "", 20)
	require.NoError(t, err)
	require.Len(t, page.List, 1)
	require.Equal(t, "pending", page.List[0].Status)
	require.Equal(t, "awaiting_resolution", page.List[0].Availability)
	var current model.ApprovalRequest
	require.NoError(t, db.Where("approval_id = ?", a.ApprovalID).First(&current).Error)
	require.Equal(t, model.ApprovalStatusPending, current.Status)
	_, err = s.approvals.Expire(ctx, a.ApprovalID)
	require.NoError(t, err)
	inboxServiceDue(t, db)
	_, err = inbox.ReconcileOnce(ctx)
	require.NoError(t, err)
	page, err = inbox.List(ctx, "user", "all", "", 20)
	require.NoError(t, err)
	require.Len(t, page.List, 1)
	require.Equal(t, "expired", page.List[0].Status)
}

func TestInboxDeploymentDirectTransitionCancellationIsDiscovered(t *testing.T) {
	s, db, execution, _, _, inbox := trustedInboxDeploymentFixture(t)
	ctx := context.Background()
	entry, err := s.PreparePipelineRelease(ctx, execution)
	require.NoError(t, err)
	changed, err := s.records.Transition(ctx, entry, "cancelled", map[string]any{"reason": "synthetic-direct-terminal"})
	require.NoError(t, err)
	require.True(t, changed)
	inboxServiceDue(t, db)
	_, err = inbox.ReconcileOnce(ctx)
	require.NoError(t, err)
	for _, actor := range []string{"user", "reviewer"} {
		page, err := inbox.List(ctx, actor, "all", "", 20)
		require.NoError(t, err)
		require.Len(t, page.List, 1)
		require.Equal(t, "cancelled", page.List[0].Status)
	}
}
func TestInboxUnavailableAuthorizationIsNotEmptyHistory(t *testing.T) {
	svc, _, db := manualInboxServiceFixture(t)
	inboxServiceSchema(t, db)
	inbox := NewInboxService(repo.NewRepositories(artifactServiceDatabase{db}, nil))
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("inbox:auth-unavailable", func(tx *gorm.DB) {
		if tx.Statement.Table == "user" {
			tx.AddError(repo.ErrInboxUnavailable)
		}
	}))
	defer db.Callback().Query().Remove("inbox:auth-unavailable")
	_, err := inbox.List(context.Background(), "actor", "all", "", 20)
	require.ErrorIs(t, err, repo.ErrInboxUnavailable)
	_, err = inbox.UnreadCount(context.Background(), "actor")
	require.ErrorIs(t, err, repo.ErrInboxUnavailable)
	_ = svc
}
