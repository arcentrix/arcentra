// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var inboxRetryJourneyModels = []any{&model.Agent{}, &model.PipelineRun{}, &model.Pipeline{}, &model.Project{}, &model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}, &model.JobRunGeneration{}, &model.User{}, &model.TeamMember{}, &model.Team{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Role{}, &model.RoleGrant{}, &model.InboxDelivery{}, &model.InboxWork{}, &model.InboxCursor{}, &model.InboxSweep{}}

type inboxRetryJourney struct {
	db                                            *gorm.DB
	services                                      *service.Services
	repos                                         *repo.Repositories
	run                                           *model.PipelineRun
	actor, voter, outsider, approval, job, prefix string
}

// This is a real live permission callback for the repository's typed trusted
// writer. No test directly sets InboxActorID, InboxOrigin or approval records.
func inboxJourneyTriggerAuthorization(ctx context.Context, view *repo.Repositories, p *model.Project, pipeline *model.Pipeline, actor string) error {
	user, err := view.User.(repo.IInboxActorReader).GetInboxActor(ctx, actor)
	if err != nil || user.UserID != actor {
		return repo.ErrManualTriggerForbidden
	}
	checker, err := authz.NewLiveAuthorizer(ctx, view)
	if err != nil {
		return err
	}
	subject := authz.Subject{UserID: actor}
	for _, permission := range []string{"project:read", "pipeline:read", "pipeline:trigger"} {
		scope := authz.ResourceRef{Type: "pipeline", ID: pipeline.PipelineID, ProjectID: p.ProjectID, OrgID: p.OrgID}
		if permission == "project:read" {
			scope.Type, scope.ID = "project", p.ProjectID
		}
		allowed, err := checker.Check(ctx, subject, permission, scope)
		if err != nil {
			return err
		}
		if !allowed {
			return repo.ErrManualTriggerForbidden
		}
	}
	return nil
}

func newInboxRetryJourney(t *testing.T, db *gorm.DB, prefix, mode string) *inboxRetryJourney {
	t.Helper()
	ctx := context.Background()
	adapter := generationServiceDatabase{db}
	repos := repo.NewRepositories(adapter, nil)
	project, pipeline, actor, voter, outsider := prefix+"project", prefix+"pipeline", prefix+"actor", prefix+"voter", prefix+"outsider"
	for _, value := range []any{&model.Pipeline{Metadata: "{}", PipelineID: pipeline, ProjectID: project, IsEnabled: 1}, &model.Project{ProjectID: project, IsEnabled: 1, Status: model.ProjectStatusActive}, &model.User{UserID: actor, IsEnabled: 1}, &model.User{UserID: voter, IsEnabled: 1}, &model.User{UserID: outsider, IsEnabled: 1}, &model.Role{RoleID: prefix + "role", IsEnabled: 1}, &model.Team{TeamID: prefix + "team", IsEnabled: 1}, &model.TeamMember{TeamID: prefix + "team", UserID: voter}, &model.RoleGrant{GrantID: prefix + "actor-grant", SubjectType: "user", SubjectID: actor, RoleID: prefix + "role", ScopeType: "project", ScopeID: project, IsEnabled: 1}, &model.RoleGrant{GrantID: prefix + "voter-grant", SubjectType: "team", SubjectID: prefix + "team", RoleID: prefix + "role", ScopeType: "project", ScopeID: project, IsEnabled: 1}} {
		require.NoError(t, mysqlAuthCreate(db, value))
	}
	for _, permission := range []string{"project:read", "pipeline:read", "pipeline:trigger", "approval:read", "approval:approve", "approval:reject"} {
		parts := strings.SplitN(permission, ":", 2)
		for _, value := range []any{&model.Permission{PermissionID: prefix + permission, ResourceType: parts[0], Action: parts[1]}, &model.RolePermissionBinding{RoleID: prefix + "role", PermissionID: prefix + permission}} {
			require.NoError(t, mysqlAuthCreate(db, value))
		}
	}
	policy := retryapproval.Policy{ID: "inbox-retry", Candidates: []string{voter}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300}
	policy.Fingerprint, _ = policy.Digest()
	capture := retryapproval.RunContext{Version: 1, ProjectID: project, PipelineID: pipeline, PipelineRunID: prefix + "run", ActorID: actor, DefinitionCommitSHA: strings.Repeat("a", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: "v1:" + strings.Repeat("b", 64), GatedJobs: []string{"build"}, Policy: policy}
	body, err := capture.Canonical()
	require.NoError(t, err)
	digest, err := capture.Digest()
	require.NoError(t, err)
	run := &model.PipelineRun{RunID: capture.PipelineRunID, PipelineID: pipeline, RequestID: prefix + "request", TriggerType: 1, TriggeredBy: actor, Status: model.PipelineStatusPending, DefinitionCommitSha: capture.DefinitionCommitSHA, DefinitionPath: capture.DefinitionPath, RetryApprovalContext: body, RetryApprovalContextDigest: digest}
	writer := repos.Pipeline.(*repo.PipelineRepo)
	verified := service.WithVerifiedPipelineActor(ctx, actor)
	require.NoError(t, writer.CreateVerifiedManualTriggerRun(verified, service.VerifiedPipelineActor(verified), run, inboxJourneyTriggerAuthorization))
	admitted, err := writer.StartManualRunIfPending(ctx, run)
	require.NoError(t, err)
	require.True(t, admitted)
	services := service.NewServices(adapter, nil, repos)
	t.Cleanup(func() {
		_ = services.LogAggregator.Close(context.Background())
		_ = repo.SetRetryApprovalAuthorizer(repos, nil)
	})
	require.NoError(t, services.ConfigureRetryApprovalPolicy(&policy))
	require.NoError(t, services.ConfigureRetryApprovalAuthorization())
	agentID, jobID := prefix+"agent", prefix+"job"
	require.NoError(t, db.Create(&model.Agent{AgentID: agentID, IsEnabled: 1}).Error)
	jobs := repos.JobRun.(*repo.JobRunRepo)
	require.NoError(t, jobs.Create(ctx, &model.JobRun{JobRunID: jobID, JobName: "build", PipelineID: pipeline, PipelineRunID: run.RunID, Status: model.JobRunStatusPending}))
	retryPolicy := &pipelinev1.Retry{Mode: mode, MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}
	policyJSON, err := sonic.MarshalString(retryPolicy)
	require.NoError(t, err)
	for index, id := range []string{prefix + "step-1", prefix + "step-2"} {
		previous := ""
		if index > 0 {
			previous = prefix + "step-1"
		}
		require.NoError(t, repos.StepRun.Create(ctx, &model.StepRun{Args: `{"run":"exit 9"}`, StepRunID: id, Name: "shell", JobRunID: jobID, PipelineID: pipeline, PipelineRunID: run.RunID, Attempt: index + 1, RetryMode: mode, RetryPolicy: policyJSON, PreviousStepRunID: previous, Uses: "shell", Status: 1}))
	}
	runtime := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: "fixture/image@sha256:" + strings.Repeat("a", 64), Resources: taskqueue.RuntimeResources{CPU: "500m", Memory: "64Mi"}}
	runtime.BindingDigest, err = taskqueue.RuntimeBindingDigest(pipeline, run.RunID, jobID, agentID, runtime)
	require.NoError(t, err)
	payload := &taskqueue.JobRunTaskPayload{PipelineID: pipeline, PipelineRunID: run.RunID, JobRunID: jobID, JobName: "build", AgentID: agentID, Runtime: runtime, Retry: retryPolicy, RetryApprovalRequired: true, RetryApprovalTimeoutSeconds: policy.TimeoutSeconds, Steps: []taskqueue.StepPayload{{Name: "shell", StepRunID: prefix + "step-1", AttemptRunIDs: []string{prefix + "step-1", prefix + "step-2"}, Uses: "shell", Args: map[string]any{"run": "exit 9"}}}}
	plan, err := taskqueue.NewJobRetryPlan(payload)
	require.NoError(t, err)
	require.NoError(t, jobs.CaptureRuntimeRetryExpectation(ctx, pipeline, run.RunID, jobID, agentID, nil, runtime, nil, nil, plan))
	job, err := jobs.GetByJobRunID(ctx, jobID)
	require.NoError(t, err)
	require.NoError(t, jobs.PatchReportedStatus(ctx, job, map[string]any{"status": model.JobRunStatusRunning, "claim_id": "original"}))
	job, err = jobs.GetByJobRunID(ctx, jobID)
	require.NoError(t, err)
	identity := taskqueue.JobInputIdentity{PipelineID: pipeline, PipelineRunID: run.RunID, JobRunID: jobID, AgentID: agentID, ClaimID: "original", RuntimeBindingDigest: runtime.BindingDigest}
	transition := taskqueue.JobGenerationTransition{Version: 1, Identity: identity, RetryPlanDigest: job.RetryPlanDigest, Operation: "admit"}
	transition.RequestDigest, err = transition.Digest()
	require.NoError(t, err)
	_, err = jobs.ReconcileJobGeneration(ctx, transition)
	require.NoError(t, err)
	manifest, err := taskqueue.NewJobInputManifest("", nil, nil)
	require.NoError(t, err)
	pinned, err := jobs.GetOrRecordJobInputs(ctx, identity, manifest)
	require.NoError(t, err)
	require.NotEmpty(t, pinned.RetryApprovalID)
	var work model.InboxWork
	require.NoError(t, db.Where("source_kind = ? AND source_id = ?", "approval", pinned.RetryApprovalID).First(&work).Error)
	var approval model.ApprovalRequest
	require.NoError(t, db.Select("inbox_origin").Where("approval_id = ?", pinned.RetryApprovalID).First(&approval).Error)
	require.Equal(t, 1, approval.InboxOrigin)
	return &inboxRetryJourney{db: db, services: services, repos: repos, run: run, actor: actor, voter: voter, outsider: outsider, approval: pinned.RetryApprovalID, job: jobID, prefix: prefix}
}

func assertInboxRetryJourney(t *testing.T, f *inboxRetryJourney, reopened *gorm.DB, outcome string) {
	t.Helper()
	ctx := context.Background()
	inbox := f.services.Inbox
	reconcile := func() {
		require.NoError(t, f.db.Model(&model.InboxWork{}).Where("state = 0").UpdateColumn("next_check_at", time.Now().Add(-time.Minute)).Error)
		result, err := inbox.ReconcileOnce(ctx)
		require.NoError(t, err)
		require.Zero(t, result.Invalid)
	}
	// Cancellation may commit before the first poll; both durable facts survive.
	if outcome != "cancelled" {
		reconcile()
		page, err := inbox.List(ctx, f.voter, "all", "", 20)
		require.NoError(t, err)
		require.Len(t, page.List, 1)
		require.Equal(t, "approval_requested", page.List[0].Kind)
		require.Equal(t, "pending", page.List[0].Status)
	}
	actorPage, err := inbox.List(ctx, f.actor, "all", "", 20)
	require.NoError(t, err)
	require.Empty(t, actorPage.List)
	switch outcome {
	case "approved":
		require.NoError(t, f.services.Approval.Approve(ctx, f.approval, f.voter, "private-canary-decision"))
	case "cancelled":
		require.NoError(t, f.repos.JobRun.(*repo.JobRunRepo).CancelAgentJobIfCurrent(ctx, f.job, model.JobRunStatusRunning, map[string]any{"status": model.JobRunStatusCancelled}))
	default:
		t.Fatal("unsupported fixture outcome")
	}
	reconcile()
	actorPage, err = inbox.List(ctx, f.actor, "all", "", 20)
	require.NoError(t, err)
	require.Len(t, actorPage.List, 1)
	require.Equal(t, "approval_result", actorPage.List[0].Kind)
	require.Equal(t, outcome, actorPage.List[0].Status)
	voterPage, err := inbox.List(ctx, f.voter, "all", "", 20)
	require.NoError(t, err)
	require.Len(t, voterPage.List, 1)
	require.Equal(t, "approval_requested", voterPage.List[0].Kind)
	require.Equal(t, outcome, voterPage.List[0].Status)
	outsider, err := inbox.List(ctx, f.outsider, "all", "", 20)
	require.NoError(t, err)
	require.Empty(t, outsider.List)
	_, err = inbox.MarkRead(ctx, f.voter, actorPage.List[0].ID)
	require.Error(t, err)
	read, err := inbox.MarkRead(ctx, f.actor, actorPage.List[0].ID)
	require.NoError(t, err)
	inbox.Start(ctx)
	inbox.Stop()
	restarted := service.NewInboxService(repo.NewRepositories(generationServiceDatabase{reopened}, nil))
	actorPage, err = restarted.List(ctx, f.actor, "all", "", 20)
	require.NoError(t, err)
	require.Len(t, actorPage.List, 1)
	require.True(t, read.ReadAt.Equal(*actorPage.List[0].ReadAt))
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", f.prefix+"voter-grant").Update("is_enabled", 0).Error)
	voterPage, err = restarted.List(ctx, f.voter, "all", "", 20)
	require.NoError(t, err)
	require.Empty(t, voterPage.List)
	count, err := restarted.UnreadCount(ctx, f.voter)
	require.NoError(t, err)
	require.Zero(t, count.Count)
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", f.prefix+"voter-grant").Update("is_enabled", 1).Error)
	voterPage, err = restarted.List(ctx, f.voter, "all", "", 20)
	require.NoError(t, err)
	require.Len(t, voterPage.List, 1)
	var deliveries []model.InboxDelivery
	require.NoError(t, f.db.Where("source_kind = ? AND source_id = ?", "approval", f.approval).Find(&deliveries).Error)
	require.Len(t, deliveries, 2)
	bytes, err := sonic.Marshal(actorPage)
	require.NoError(t, err)
	require.NotContains(t, string(bytes), "private-canary")
	require.NotContains(t, string(bytes), "evidence")
	require.NotContains(t, string(bytes), "inboxActor")
}

func TestInboxRetryVerifiedServiceFixtureSQLite(t *testing.T) {
	for _, mode := range []string{"whole_job", "failed_step"} {
		for _, outcome := range []string{"approved", "cancelled"} {
			t.Run(mode+"/"+outcome, func(t *testing.T) {
				db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "inbox-retry.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
				require.NoError(t, err)
				pool, err := db.DB()
				require.NoError(t, err)
				pool.SetMaxOpenConns(1)
				t.Cleanup(func() { _ = pool.Close() })
				require.NoError(t, db.AutoMigrate(inboxRetryJourneyModels...))
				f := newInboxRetryJourney(t, db, "sqlite-", mode)
				assertInboxRetryJourney(t, f, db, outcome)
			})
		}
	}
}

func TestInboxMySQLVerifiedRetryServiceJourney(t *testing.T) {
	for _, collation := range []string{"utf8mb4_0900_ai_ci", "utf8mb4_bin"} {
		for _, mode := range []string{"whole_job", "failed_step"} {
			for _, outcome := range []string{"approved", "cancelled"} {
				t.Run(collation+"/"+mode+"/"+outcome, func(t *testing.T) {
					db, other, prefix := mysqlAuthDatabase(t)
					for _, entity := range []any{&model.InboxDelivery{}, &model.InboxWork{}, &model.InboxCursor{}, &model.InboxSweep{}} {
						if db.Migrator().HasTable(entity) {
							var n int64
							require.NoError(t, db.Model(entity).Count(&n).Error)
							require.Zero(t, n, "inbox acceptance refuses existing data")
						}
					}
					require.NoError(t, db.AutoMigrate(inboxRetryJourneyModels...))
					// The shared fixture guarantees empty disposable tables before these DDLs.
					for _, table := range []string{"agent", "pipeline_run", "pipeline", "project", "job_run", "step_run", "project_artifact", "approval_request", "approval_decision", "job_run_generation", "user", "team_member", "team", "permission", "role_permission_binding", "role", "role_grant", "inbox_delivery", "inbox_work", "inbox_cursor", "inbox_sweep"} {
						require.NoError(t, db.Exec("ALTER TABLE `"+table+"` CONVERT TO CHARACTER SET utf8mb4 COLLATE "+collation).Error)
					}
					t.Cleanup(func() {
						var approvals []string
						require.NoError(t, db.Model(&model.ApprovalRequest{}).Where("retry_job_run_id LIKE ?", prefix+"%").Pluck("approval_id", &approvals).Error)
						require.NoError(t, db.Where("recipient_id LIKE ?", prefix+"%").Delete(&model.InboxDelivery{}).Error)
						require.NoError(t, db.Where("source_id LIKE ? OR source_id IN ?", prefix+"%", approvals).Delete(&model.InboxWork{}).Error)
						require.NoError(t, db.Where("owner_id LIKE ?", prefix+"%").Delete(&model.InboxCursor{}).Error)
						require.NoError(t, db.Where("kind IN ?", []string{"run", "approval"}).Delete(&model.InboxSweep{}).Error)
					})
					f := newInboxRetryJourney(t, db, prefix, mode)
					assertInboxRetryJourney(t, f, other, outcome)
				})
			}
		}
	}
}
