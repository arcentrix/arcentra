package router

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func approvalRouterFixture(t *testing.T) (*fiber.App, *Router, *gorm.DB) {
	t.Helper()
	app, rt := collectionRouterFixture(t)
	db := rt.Services.ProjectRepo.(*repo.ProjectRepo).Database()
	adapter := collectionTestDB{db}
	repos := repo.NewRepositories(adapter, nil)
	// 使用真实服务聚合以逐次重读角色、授权和过期条件
	rt.Services = service.NewServices(adapter, nil, repos)
	ctx := context.Background()
	for _, id := range []string{"team-user"} {
		require.NoError(t, db.WithContext(ctx).Create(&model.User{UserID: id, Username: id, IsEnabled: 1}).Error)
	}
	// The base collection fixture now seeds active users and the scoped role.
	for _, permission := range []string{consts.PermApprovalRead, consts.PermApprovalApprove, consts.PermApprovalReject} {
		require.NoError(t, db.WithContext(ctx).Create(&model.RolePermissionBinding{RoleID: "scoped-test", PermissionID: permission}).Error)
	}
	for _, scope := range []string{"a", "b"} {
		require.NoError(t, db.WithContext(ctx).Create(&model.PipelineRun{RunID: "run-" + scope, PipelineID: "pipe-" + scope, Status: model.PipelineStatusRunning}).Error)
		expires := time.Now().Add(time.Hour)
		require.NoError(t, rt.Services.Approval.CreateApprovalRequest(ctx, &model.ApprovalRequest{ApprovalID: "approval-" + scope, PipelineRunID: "run-" + scope, RequestedBy: "requester", CandidateApprovers: `["project-user","org-user"]`, RequiredApproverCount: 2, ExpiresAt: &expires}))
	}
	authMW := func(c *fiber.Ctx) error {
		claims, err := jwt.ParseToken(strings.TrimPrefix(c.Get("Authorization"), "Bearer "), "test-secret")
		if err != nil {
			return apphttp.Err(c, apphttp.Unauthorized.Code, apphttp.Unauthorized.Msg)
		}
		c.Locals("claims", claims)
		return c.Next()
	}
	rt.approvalRouter(app, authMW, middleware.SubjectMiddleware(nil))
	return app, rt, db
}
func approvalHTTP(t *testing.T, app *fiber.App, user, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		token, _, err := jwt.GenToken(user, []byte("test-secret"), time.Hour, time.Hour)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := app.Test(req, routerTestTimeoutMillis)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Contains(t, res.Header.Get("Cache-Control"), "no-store")
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, sonic.Unmarshal(raw, &result), string(raw))
	return res.StatusCode, result
}
func TestApprovalHTTPUsesAuthenticatedActorAndStrictBody(t *testing.T) {
	app, _, db := approvalRouterFixture(t)
	for _, body := range []string{"", `null`, `[]`, `{bad`, `{"approvedBy":"org-user","reason":"spoof"}`, `{"reason":"ok","extra":true}`, `{} {}`, `{"reason":123}`, `{"reason":null}`, `{"Reason":"alias"}`, `{"reason":"first","reason":"second"}`, `{"reason":"` + strings.Repeat("x", 2049) + `"}`} {
		status, _ := approvalHTTP(t, app, "project-user", http.MethodPost, "/approvals/approval-a/approve", body)
		require.Equal(t, http.StatusBadRequest, status, body)
	}
	status, _ := approvalHTTP(t, app, "project-user", http.MethodPost, "/approvals/approval-a/approve?user=org-user", `{}`)
	require.Equal(t, 400, status)
	status, _ = approvalHTTP(t, app, "", http.MethodPost, "/approvals/approval-a/approve?token=signed&expires=9999999999", `{}`)
	require.Equal(t, 401, status)
	var count int64
	require.NoError(t, db.Model(&model.ApprovalDecision{}).Count(&count).Error)
	require.Zero(t, count)
	status, result := approvalHTTP(t, app, "project-user", http.MethodPost, "/approvals/approval-a/approve", `{"reason":"reviewed"}`)
	require.Equal(t, 200, status)
	detail := result["detail"].(map[string]any)
	require.EqualValues(t, model.ApprovalStatusPending, detail["status"])
	require.Equal(t, "project-user", detail["approvedBy"])
	require.Equal(t, "approved", detail["viewerDecision"])
	require.Equal(t, false, detail["canApprove"])
	status, _ = approvalHTTP(t, app, "project-user", http.MethodPost, "/approvals/approval-a/approve", `{}`)
	require.Equal(t, 200, status)
	status, _ = approvalHTTP(t, app, "project-user", http.MethodPost, "/approvals/approval-a/reject", `{}`)
	require.Equal(t, 409, status)
	var votes []model.ApprovalDecision
	require.NoError(t, db.Where("approval_id = ?", "approval-a").Find(&votes).Error)
	require.Len(t, votes, 1)
	require.Equal(t, "project-user", votes[0].UserID)
	require.Equal(t, "reviewed", votes[0].Comment)
}
func TestApprovalHTTPScopesListReadAndActionToRunProject(t *testing.T) {
	app, _, _ := approvalRouterFixture(t)
	for _, path := range []string{"/approvals/?pipeline_run_id=run-b", "/approvals/approval-b"} {
		status, _ := approvalHTTP(t, app, "project-user", http.MethodGet, path, "")
		require.Equal(t, 403, status, path)
	}
	status, _ := approvalHTTP(t, app, "project-user", http.MethodPost, "/approvals/approval-b/approve", `{}`)
	require.Equal(t, 403, status)
	status, _ = approvalHTTP(t, app, "admin", http.MethodPost, "/approvals/approval-a/approve", `{}`)
	require.Equal(t, 403, status, "platform permission is not candidate membership")
	status, result := approvalHTTP(t, app, "project-user", http.MethodGet, "/approvals/?pipeline_run_id=run-a", "")
	require.Equal(t, 200, status)
	list := result["detail"].([]any)
	require.Len(t, list, 1)
	detail := list[0].(map[string]any)
	require.Equal(t, "approval-a", detail["approvalId"])
	require.Equal(t, true, detail["canApprove"])
	require.Equal(t, true, detail["canReject"])
	status, _ = approvalHTTP(t, app, "project-user", http.MethodGet, "/approvals/?pipeline_run_id=missing", "")
	require.Equal(t, 404, status)
	status, _ = approvalHTTP(t, app, "project-user", http.MethodGet, "/approvals/", "")
	require.Equal(t, 400, status)
}
func TestApprovalHTTPRevokedPermissionAndLinkedEligibilityFailClosed(t *testing.T) {
	app, rt, db := approvalRouterFixture(t)
	status, result := approvalHTTP(t, app, "project-user", http.MethodGet, "/approvals/approval-a", "")
	require.Equal(t, 200, status)
	require.Equal(t, true, result["detail"].(map[string]any)["canApprove"])
	// 不调用缓存 Reload，下一次请求仍必须看到撤销
	require.NoError(t, db.Where("role_id = ? AND permission_id = ?", "scoped-test", consts.PermApprovalApprove).Delete(&model.RolePermissionBinding{}).Error)
	status, result = approvalHTTP(t, app, "project-user", http.MethodGet, "/approvals/approval-a", "")
	require.Equal(t, 200, status)
	require.Equal(t, false, result["detail"].(map[string]any)["canApprove"])
	status, _ = approvalHTTP(t, app, "project-user", http.MethodPost, "/approvals/approval-a/approve", `{}`)
	require.Equal(t, 403, status)
	release := "release"
	require.NoError(t, db.Model(&model.ApprovalRequest{}).Where("approval_id = ?", "approval-a").Update("deployment_release_id", release).Error)
	status, result = approvalHTTP(t, app, "project-user", http.MethodGet, "/approvals/approval-a", "")
	require.Equal(t, 200, status)
	require.Equal(t, false, result["detail"].(map[string]any)["canReject"])
	called := false
	rt.Services.Approval.SetDecisionValidator(func(context.Context, *model.ApprovalRequest, string, string) error {
		called = true
		return repo.ErrApprovalForbidden
	})
	status, _ = approvalHTTP(t, app, "project-user", http.MethodPost, "/approvals/approval-a/reject", `{}`)
	require.Equal(t, 403, status)
	require.True(t, called)
	var count int64
	require.NoError(t, db.Model(&model.ApprovalDecision{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestApprovalHTTPRebuildsEnabledUserAndTeamSubject(t *testing.T) {
	for _, change := range []string{"membership", "team", "user", "expired-grant", "role"} {
		t.Run(change, func(t *testing.T) {
			app, _, db := approvalRouterFixture(t)
			ctx := context.Background()
			require.NoError(t, db.WithContext(ctx).Create(&model.Team{TeamID: "reviewers", Name: "reviewers", IsEnabled: 1}).Error)
			require.NoError(t, db.WithContext(ctx).Create(&model.TeamMember{TeamID: "reviewers", UserID: "team-user"}).Error)
			require.NoError(t, db.WithContext(ctx).Create(&model.RoleGrant{GrantID: "team-grant", SubjectType: "team", SubjectID: "reviewers", RoleID: "scoped-test", ScopeType: "project", ScopeID: "a", IsEnabled: 1}).Error)
			status, _ := approvalHTTP(t, app, "team-user", http.MethodGet, "/approvals/approval-a", "")
			require.Equal(t, 200, status)
			switch change {
			case "membership":
				require.NoError(t, db.WithContext(ctx).Where("team_id = ? AND user_id = ?", "reviewers", "team-user").Delete(&model.TeamMember{}).Error)
			case "team":
				require.NoError(t, db.WithContext(ctx).Model(&model.Team{}).Where("team_id = ?", "reviewers").Update("is_enabled", 0).Error)
			case "user":
				require.NoError(t, db.WithContext(ctx).Model(&model.User{}).Where("user_id = ?", "team-user").Update("is_enabled", 0).Error)
			case "expired-grant":
				require.NoError(t, db.WithContext(ctx).Model(&model.RoleGrant{}).Where("grant_id = ?", "team-grant").Update("expires_at", time.Now().Add(-time.Minute)).Error)
			case "role":
				require.NoError(t, db.WithContext(ctx).Model(&model.Role{}).Where("role_id = ?", "scoped-test").Update("is_enabled", 0).Error)
			}
			status, _ = approvalHTTP(t, app, "team-user", http.MethodGet, "/approvals/approval-a", "")
			require.Equal(t, 403, status)
			status, _ = approvalHTTP(t, app, "team-user", http.MethodGet, "/approvals/?pipeline_run_id=run-a", "")
			require.Equal(t, 403, status)
		})
	}
}

func TestApprovalHTTPRejectOnlyReviewerHasIndependentEligibility(t *testing.T) {
	app, rt, db := approvalRouterFixture(t)
	ctx := context.Background()
	require.NoError(t, db.WithContext(ctx).Where("role_id = ? AND permission_id = ?", "scoped-test", consts.PermApprovalApprove).Delete(&model.RolePermissionBinding{}).Error)
	require.NoError(t, db.WithContext(ctx).Model(&model.PipelineRun{}).Where("run_id = ?", "run-a").Updates(map[string]any{"allow_deployments": true, "deployment_actor_id": "requester", "deployment_plan_hash": "plan", "deployment_target_fingerprint": "target", "definition_commit_sha": "commit"}).Error)
	releaseID, approvalID, stepID := "release", "approval-a", "step"
	records := []any{
		&model.JobRun{JobRunID: "job", PipelineRunID: "run-a", PipelineID: "pipe-a", Status: model.JobRunStatusRunning},
		&model.StepRun{StepRunID: stepID, JobRunID: "job", PipelineRunID: "run-a", PipelineID: "pipe-a", Status: 3, DeploymentReleaseID: releaseID},
		&model.DeploymentEnvironment{EnvironmentID: "env", ProjectID: "a", ActiveReleaseID: releaseID},
		&model.DeploymentRelease{ReleaseID: releaseID, ProjectID: "a", RequestID: "request", Origin: "pipeline_job", PipelineID: "pipe-a", PipelineRunID: "run-a", ExecutionJobRunID: "job", ExecutionStepRunID: &stepID, EnvironmentID: "env", Status: "queued", ApprovalID: &approvalID, ApprovalEvidenceHash: "evidence", CreatedBy: "requester", DeploymentPlanHash: "plan", TargetFingerprint: "target", CommitSHA: "commit"},
	}
	for _, record := range records {
		require.NoError(t, db.WithContext(ctx).Create(record).Error)
	}
	require.NoError(t, db.WithContext(ctx).Model(&model.ApprovalRequest{}).Where("approval_id = ?", approvalID).Updates(map[string]any{"deployment_release_id": releaseID, "evidence_hash": "evidence", "environment": "env"}).Error)
	rt.Services.Approval.SetDecisionValidator(func(_ context.Context, _ *model.ApprovalRequest, actor, decision string) error {
		if actor != "project-user" || decision != model.ApprovalDecisionReject {
			return service.ErrDeploymentAuthorization
		}
		return nil
	})
	status, result := approvalHTTP(t, app, "project-user", http.MethodGet, "/approvals/approval-a", "")
	require.Equal(t, 200, status)
	detail := result["detail"].(map[string]any)
	require.Equal(t, false, detail["canApprove"])
	require.Equal(t, true, detail["canReject"])
	status, _ = approvalHTTP(t, app, "project-user", http.MethodPost, "/approvals/approval-a/approve", `{}`)
	require.Equal(t, 403, status)
	status, result = approvalHTTP(t, app, "project-user", http.MethodPost, "/approvals/approval-a/reject", `{"reason":"do not deploy"}`)
	require.Equal(t, 200, status)
	detail = result["detail"].(map[string]any)
	require.EqualValues(t, model.ApprovalStatusRejected, detail["status"])
	require.Equal(t, "rejected", detail["viewerDecision"])
}
