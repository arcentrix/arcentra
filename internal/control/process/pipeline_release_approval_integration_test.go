package process

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	cluster "github.com/arcentrix/arcentra/internal/shared/deployment"
)

func submitApprovalLifecycle(t *testing.T, f *lifecycleFixture) (*model.PipelineRun, *model.DeploymentRelease, *service.ApprovalService) {
	t.Helper()
	definition := f.pipeline()
	definition.Jobs[1].Approval = &pipelinev1.Approval{Required: true, Type: "manual", Plugin: "builtin:deployment"}
	run := f.submit(t, definition)
	release := f.waitRelease(t, "queued")
	if release.ApprovalID == nil || release.ApprovalEvidenceHash == "" || len(release.ApprovalTarget) == 0 {
		t.Fatal("release has no immutable approval evidence")
	}
	approvals := service.NewApprovalService(f.repos.Approval, f.repos.ApprovalDecision, nil)
	approvals.SetDecisionValidator(f.deployment.ValidateApprovalDecision)
	return run, release, approvals
}

func TestPipelineApprovalIntegrationPinsEvidenceAndRequiresQuorum(t *testing.T) {
	f := newLifecycleFixture(t, &cluster.PipelineApprovalPolicy{Approvers: []string{"reviewer-a", "reviewer-b", "lifecycle-actor"}, MinApprovals: 2, TimeoutSeconds: 30})
	run, release, approvals := submitApprovalLifecycle(t, f)
	ctx := context.Background()
	approval, err := approvals.GetApproval(ctx, *release.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := model.ApprovalCandidates(approval)
	if err != nil || len(candidates) != 2 || approval.RequestedBy != run.DeploymentActorID || approval.EvidenceHash != release.ApprovalEvidenceHash || approval.DeploymentReleaseID == nil || *approval.DeploymentReleaseID != release.ReleaseID {
		t.Fatalf("invalid pinned approval %+v %v", approval, err)
	}
	if err := approvals.Approve(ctx, approval.ApprovalID, "lifecycle-actor", "self nomination"); err == nil {
		t.Fatal("requester approved own release")
	}
	if err := approvals.Approve(ctx, approval.ApprovalID, "reviewer-a", "reviewed exact manifest"); err != nil {
		t.Fatal(err)
	}
	if err := approvals.Approve(ctx, approval.ApprovalID, "reviewer-a", "repeat after uncertain response"); err != nil {
		t.Fatal(err)
	}
	first, err := approvals.GetApproval(ctx, approval.ApprovalID)
	if err != nil || first.Status != model.ApprovalStatusPending || first.ApprovedCount != 1 {
		t.Fatalf("duplicate vote changed quorum %+v %v", first, err)
	}
	f.kube.mu.Lock()
	gets := f.kube.gets
	f.kube.mu.Unlock()
	if f.kube.patchCount() != 0 || gets != 0 {
		t.Fatal("cluster contacted before approval quorum")
	}
	f.kube.setReady(true)
	if err := approvals.Approve(ctx, approval.ApprovalID, "reviewer-b", "approved target and image"); err != nil {
		t.Fatal(err)
	}
	waitRun(t, f.repos, run.RunID, model.PipelineStatusSuccess)
	if f.kube.patchCount() != 1 || f.oneRelease(t).ReleaseID != release.ReleaseID {
		t.Fatal("quorum did not result in exactly one linked release")
	}
	// A service restart retains the same verdict and audit; replay cannot initiate work.
	restarted := service.NewApprovalService(f.repos.Approval, f.repos.ApprovalDecision, nil)
	if err := restarted.Approve(ctx, approval.ApprovalID, "reviewer-b", "replayed"); err != nil {
		t.Fatal(err)
	}
	decisions, err := f.repos.ApprovalDecision.ListByApproval(ctx, approval.ApprovalID)
	if err != nil || len(decisions) != 2 || f.kube.patchCount() != 1 {
		t.Fatal("restart/replay duplicated votes or release", err)
	}
}

func TestPipelineApprovalIntegrationRejectCancelExpireWithoutPatch(t *testing.T) {
	for _, mode := range []string{"reject", "cancel", "expire"} {
		t.Run(mode, func(t *testing.T) {
			ttl := int64(30)
			if mode == "expire" {
				ttl = 1
			}
			f := newLifecycleFixture(t, &cluster.PipelineApprovalPolicy{Approvers: []string{"reviewer"}, MinApprovals: 1, TimeoutSeconds: ttl})
			run, release, approvals := submitApprovalLifecycle(t, f)
			expectedRun, expectedApproval := model.PipelineStatusFailed, model.ApprovalStatusRejected
			switch mode {
			case "reject":
				if err := approvals.Reject(context.Background(), *release.ApprovalID, "reviewer", "wrong release"); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				expectedRun, expectedApproval = model.PipelineStatusCancelled, model.ApprovalStatusCanceled
				if err := f.engine.CancelRun(run.RunID); err != nil {
					t.Fatal(err)
				}
			case "expire":
				expectedApproval = model.ApprovalStatusExpired
			}
			waitRun(t, f.repos, run.RunID, expectedRun)
			current := f.waitRelease(t, "cancelled")
			approved, err := approvals.GetApproval(context.Background(), *release.ApprovalID)
			if err != nil || approved.Status != expectedApproval {
				t.Fatalf("wrong durable verdict %+v %v", approved, err)
			}
			if current.ReleaseID != release.ReleaseID || f.kube.patchCount() != 0 {
				t.Fatal("blocked approval emitted PATCH or replaced release")
			}
			if err := approvals.Approve(context.Background(), approved.ApprovalID, "reviewer", "late approval"); err == nil {
				t.Fatal("terminal verdict changed")
			}
			environment, err := f.repos.Deployment.GetEnvironment(context.Background(), f.projectID, f.environmentID)
			if err != nil || environment.ActiveReleaseID != "" {
				t.Fatal("queued cancellation retained reservation", err)
			}
		})
	}
}

func configureLifecycleReviewer(t *testing.T, f *lifecycleFixture) {
	t.Helper()
	if err := f.db.AutoMigrate(&model.User{}, &model.Team{}, &model.TeamMember{}, &model.Role{}, &model.RoleGrant{}, &model.Permission{}, &model.RolePermissionBinding{}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{&model.User{UserID: "reviewer", IsEnabled: 1}, &model.Role{RoleID: "review-role", Name: "review", IsEnabled: 1}, &model.RoleGrant{GrantID: "review-grant", SubjectType: "user", SubjectID: "reviewer", RoleID: "review-role", ScopeType: "project", ScopeID: f.projectID, IsEnabled: 1}} {
		if err := f.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.Model(&model.Project{}).Where("project_id = ?", f.projectID).Updates(map[string]any{"is_enabled": 1, "status": model.ProjectStatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"project:read", "pipeline:read", "approval:approve", "release:deploy_staging"} {
		parts := strings.SplitN(action, ":", 2)
		if err := f.db.Create(&model.Permission{PermissionID: action, ResourceType: parts[0], Action: parts[1]}).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.Create(&model.RolePermissionBinding{RoleID: "review-role", PermissionID: action}).Error; err != nil {
			t.Fatal(err)
		}
	}
	repositories := repo.NewRepositories(processTestDB{f.db}, nil)
	f.deployment.ConfigureDeploymentApprovals(f.repos.ApprovalDecision, service.NewDeploymentApprovalAuthorizer(repositories, f.deployment), service.NewDeploymentRejectionAuthorizer(repositories, f.deployment))
}

func TestPipelineApprovalIntegrationRevokedVoterAtFinalReadBlocksPatch(t *testing.T) {
	f := newLifecycleFixture(t, &cluster.PipelineApprovalPolicy{Approvers: []string{"reviewer"}, MinApprovals: 1, TimeoutSeconds: 30})
	configureLifecycleReviewer(t, f)
	f.kube.afterRead = func(n int) {
		if n == 2 {
			if err := f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "review-grant").Update("is_enabled", 0).Error; err != nil {
				t.Error(err)
			}
		}
	}
	run, release, approvals := submitApprovalLifecycle(t, f)
	if err := approvals.Approve(context.Background(), *release.ApprovalID, "reviewer", "reviewed"); err != nil {
		t.Fatal(err)
	}
	waitRun(t, f.repos, run.RunID, model.PipelineStatusFailed)
	current := f.oneRelease(t)
	if current.Status != "applying" || f.kube.patchCount() != 0 {
		t.Fatal("revoked vote allowed mutation or lost uncertainty state")
	}
	if _, err := f.deployment.RecoverAuthorized(context.Background(), f.projectID, current.ReleaseID, "lifecycle-actor"); err == nil {
		t.Fatal("recovery bypassed revoked approval")
	}
	if f.kube.patchCount() != 0 {
		t.Fatal("failed recovery emitted PATCH")
	}
}

func TestPipelineApprovalIntegrationLostReceiptRetainsGateOnRestart(t *testing.T) {
	f := newLifecycleFixture(t, &cluster.PipelineApprovalPolicy{Approvers: []string{"reviewer"}, MinApprovals: 1, TimeoutSeconds: 30})
	f.kube.losePatchResponse = true
	run, release, approvals := submitApprovalLifecycle(t, f)
	if err := approvals.Approve(context.Background(), *release.ApprovalID, "reviewer", "exact release approved"); err != nil {
		t.Fatal(err)
	}
	waitRun(t, f.repos, run.RunID, model.PipelineStatusFailed)
	f.engine.Stop()
	current := f.oneRelease(t)
	if current.Status != "applying" || current.ApprovalID == nil || *current.ApprovalID != *release.ApprovalID || current.ApprovalEvidenceHash != release.ApprovalEvidenceHash || f.kube.patchCount() != 1 {
		t.Fatal("lost receipt did not retain exact approved release")
	}
	f.assertRestartDoesNotApply(t, run, current)
	f.assertInspectionAndAdvanceDoNotPatch(t, current)
	f.kube.setReady(true)
	recovered, err := f.deployment.RecoverAuthorized(context.Background(), f.projectID, release.ReleaseID, "lifecycle-actor")
	if err != nil || recovered.Status != "observing" || recovered.LastRecoveryBy != "lifecycle-actor" {
		t.Fatalf("explicit approved recovery failed %+v %v", recovered, err)
	}
	completed, err := f.deployment.AdvanceAuthorized(context.Background(), f.projectID, release.ReleaseID, "lifecycle-actor")
	if err != nil || completed.Status != "succeeded" || f.kube.patchCount() != 1 {
		t.Fatal("same release recovery duplicated patch", err)
	}
	stopped, err := f.repos.Pipeline.GetRun(context.Background(), run.RunID)
	if err != nil || stopped.Status != model.PipelineStatusFailed {
		t.Fatal("recovery resurrected failed pipeline", err)
	}
}

func TestPipelineApprovalIntegrationExpiryBlocksUncertainRecovery(t *testing.T) {
	f := newLifecycleFixture(t, &cluster.PipelineApprovalPolicy{Approvers: []string{"reviewer"}, MinApprovals: 1, TimeoutSeconds: 3})
	f.kube.losePatchResponse = true
	run, release, approvals := submitApprovalLifecycle(t, f)
	if err := approvals.Approve(context.Background(), *release.ApprovalID, "reviewer", "reviewed"); err != nil {
		t.Fatal(err)
	}
	waitRun(t, f.repos, run.RunID, model.PipelineStatusFailed)
	approval, err := approvals.GetApproval(context.Background(), *release.ApprovalID)
	if err != nil || approval.ExpiresAt == nil {
		t.Fatal("missing expiry", err)
	}
	// Let the real signed evidence expire instead of editing its hash-bound timestamp.
	if delay := time.Until(*approval.ExpiresAt); delay > 0 {
		time.Sleep(delay + 10*time.Millisecond)
	}
	f.assertInspectionAndAdvanceDoNotPatch(t, f.oneRelease(t))
	if _, err := f.deployment.RecoverAuthorized(context.Background(), f.projectID, release.ReleaseID, "lifecycle-actor"); err == nil {
		t.Fatal("expired approval recovered uncertain apply")
	}
	if f.kube.patchCount() != 1 || f.oneRelease(t).Status != "applying" {
		t.Fatal("expiry repeated mutation or concealed uncertain apply")
	}
}

func TestPipelineApprovalIntegrationRejectOnlyReviewer(t *testing.T) {
	f := newLifecycleFixture(t, &cluster.PipelineApprovalPolicy{Approvers: []string{"reviewer"}, MinApprovals: 1, TimeoutSeconds: 30})
	configureLifecycleReviewer(t, f)
	if err := f.db.Where("role_id = ? AND permission_id = ?", "review-role", "approval:approve").Delete(&model.RolePermissionBinding{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.Permission{PermissionID: "approval:reject", ResourceType: "approval", Action: "reject"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.RolePermissionBinding{RoleID: "review-role", PermissionID: "approval:reject"}).Error; err != nil {
		t.Fatal(err)
	}
	run, release, approvals := submitApprovalLifecycle(t, f)
	if err := approvals.Approve(context.Background(), *release.ApprovalID, "reviewer", "not authorized"); err == nil {
		t.Fatal("reject-only reviewer approved release")
	}
	if err := approvals.Reject(context.Background(), *release.ApprovalID, "reviewer", "blocked by authorized reviewer"); err != nil {
		t.Fatal("reject-only reviewer denied", err)
	}
	waitRun(t, f.repos, run.RunID, model.PipelineStatusFailed)
	current, err := approvals.GetApproval(context.Background(), *release.ApprovalID)
	if err != nil || current.Status != model.ApprovalStatusRejected || current.RejectedCount != 1 || current.ApprovedCount != 0 || f.kube.patchCount() != 0 {
		t.Fatal("reject-only verdict not durable", err)
	}
}

func TestPipelineApprovalIntegrationExpiryDuringFinalAuthorizationBlocksPatch(t *testing.T) {
	f := newLifecycleFixture(t, &cluster.PipelineApprovalPolicy{Approvers: []string{"reviewer"}, MinApprovals: 1, TimeoutSeconds: 3})
	var finalRead atomic.Bool
	var expiry atomic.Int64
	f.kube.afterRead = func(n int) {
		if n == 2 {
			finalRead.Store(true)
		}
	}
	authorize := func(ctx context.Context, actor, project, pipeline, environment string) error {
		if actor != "reviewer" || project != f.projectID || pipeline != f.pipelineID || environment != f.environmentID {
			return service.ErrDeploymentAuthorization
		}
		if finalRead.Load() {
			// Simulate a permission storage read which completes after the approval TTL.
			delay := time.Until(time.Unix(0, expiry.Load())) + 20*time.Millisecond
			if delay > 0 {
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
		return nil
	}
	f.deployment.ConfigureDeploymentApprovals(f.repos.ApprovalDecision, authorize, authorize)
	run, release, approvals := submitApprovalLifecycle(t, f)
	approval, err := approvals.GetApproval(context.Background(), *release.ApprovalID)
	if err != nil || approval.ExpiresAt == nil {
		t.Fatal("missing gate expiry", err)
	}
	expiry.Store(approval.ExpiresAt.UnixNano())
	if err := approvals.Approve(context.Background(), approval.ApprovalID, "reviewer", "reviewed"); err != nil {
		t.Fatal(err)
	}
	waitRun(t, f.repos, run.RunID, model.PipelineStatusFailed)
	if !finalRead.Load() || f.kube.patchCount() != 0 || f.oneRelease(t).Status != "applying" {
		t.Fatal("approval expiring within final guard permitted PATCH")
	}
}
