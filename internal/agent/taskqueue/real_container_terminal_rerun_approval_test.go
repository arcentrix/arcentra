//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/agent/config"
	controlconfig "github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestContainerTerminalRerunProductionApproval 验证真实监听器的审批查询及立即关闭无上下文竞态
func TestContainerTerminalRerunProductionApproval(t *testing.T) {
	commands := &terminalSessionCommands{store: cache.NewFastCache(cache.FastCacheConfig{MaxBytes: 1024 * 1024})}
	t.Cleanup(commands.store.Clear)
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	client.AddHook(commands)
	t.Cleanup(func() { _ = client.Close() })
	store := cache.NewRedisCache(client)
	var services *controlservice.Services
	options := controllerWorkerFixtureOptions{repositoryCache: store, timeout: 30 * time.Second}
	options.configureRepositories = func(repositories *repo.Repositories) {
		adapter := repositories.GetDB()
		if err := adapter.Database().WithContext(context.Background()).AutoMigrate(&model.User{}, &model.UserExt{}, &model.Role{}, &model.RoleGrant{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Menu{}, &model.Team{}, &model.TeamMember{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}, &model.InboxDelivery{}, &model.InboxCursor{}, &model.InboxWork{}, &model.AuditLog{}); err != nil {
			t.Fatal(err)
		}
		var err error
		services, err = controlservice.ProvideServices(adapter, store, repositories, taskpayload.OfflineRuntimePolicy{})
		if err != nil {
			t.Fatal(err)
		}
	}
	f := newControllerWorkerFixture(t, false, options)
	actor := "owner-" + id.GetUild()
	reviewers := []string{"reviewer-a-" + id.GetUild(), "reviewer-b-" + id.GetUild()}
	seedContainerApprovalActors(t, f, actor, reviewers)
	h := newTerminalRerunHTTP(t, f, services, store, &controlconfig.AppConfig{}, append([]string{actor}, reviewers...))
	run := &model.PipelineRun{RunID: id.GetUild(), PipelineID: f.pipelineID, Status: model.PipelineStatusRunning}
	if err := f.db.WithContext(f.ctx).Create(run).Error; err != nil {
		t.Fatal(err)
	}
	candidates, err := sonic.MarshalString(reviewers)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Minute)
	approval := &model.ApprovalRequest{ApprovalID: id.GetUild(), PipelineRunID: run.RunID, RequestedBy: actor, CandidateApprovers: candidates, RequiredApproverCount: 2, Mode: "all", ExpiresAt: &expires}
	if err := services.Approval.CreateApprovalRequest(f.ctx, approval); err != nil {
		t.Fatal(err)
	}
	for _, reviewer := range reviewers {
		h.request(t, f.ctx, reviewer, http.MethodPost, "/approvals/"+approval.ApprovalID+"/approve", map[string]string{"reason": "independent fixture review"}, nil)
	}
}

// TestContainerTerminalRerunSharedRetryActors 验证父子夹具共享候选人与生产触发和审批链路
// SQLite, Redis transport, Kafka broker and runtime sessions are local substitutes;
// this is not containerd, MySQL, Redis wire or hosted acceptance evidence.
func TestContainerTerminalRerunSharedRetryActors(t *testing.T) {
	commands := &terminalSessionCommands{store: cache.NewFastCache(cache.FastCacheConfig{MaxBytes: 1024 * 1024})}
	t.Cleanup(commands.store.Clear)
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	client.AddHook(commands)
	t.Cleanup(func() { _ = client.Close() })
	store := cache.NewRedisCache(client)
	image := "registry.example/approved@sha256:" + strings.Repeat("a", 64)
	runtimePolicy := taskpayload.OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowWholeJobRetryApproval: true, AllowedImages: []string{image}, MaxCPU: "1", MaxMemory: "128Mi"}
	policy := &retryapproval.Policy{ID: "approval-" + id.GetUild(), Candidates: []string{"reviewer-a-" + id.GetUild(), "reviewer-b-" + id.GetUild()}, Mode: "all", RequiredApproverCount: 2, TimeoutSeconds: 60}
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")))
	var services *controlservice.Services
	options := controllerWorkerTLS(t)
	options.repositoryCache = store
	options.timeout = 30 * time.Second
	options.processConfig = &controlconfig.AppConfig{Pipeline: controlconfig.PipelineConfig{OfflineRuntime: runtimePolicy, RetryApproval: policy}}
	options.configureRepositories = func(repositories *repo.Repositories) {
		adapter := repositories.GetDB()
		if err := adapter.Database().WithContext(context.Background()).AutoMigrate(&model.User{}, &model.UserExt{}, &model.Role{}, &model.RoleGrant{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Menu{}, &model.Team{}, &model.TeamMember{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}, &model.InboxDelivery{}, &model.InboxCursor{}, &model.InboxWork{}, &model.AuditLog{}, &model.Organization{}, &model.ProjectMember{}, &model.ProjectVariable{}, &model.Secret{}, &model.Setting{}, &model.PipelineTemplate{}, &model.PipelineTemplateLibrary{}, &model.CopilotDefinitionSnapshot{}, &model.CopilotRerunIntent{}, &model.CopilotRerunAdmissionGate{}, &model.CopilotRerunSchedule{}); err != nil {
			t.Fatal(err)
		}
		var err error
		services, err = controlservice.ProvideServices(adapter, store, repositories, runtimePolicy)
		if err != nil {
			t.Fatal(err)
		}
		if err := services.ConfigureRetryApprovalPolicy(policy); err != nil {
			t.Fatal(err)
		}
		if err := services.ConfigureRetryApprovalAuthorization(); err != nil {
			t.Fatal(err)
		}
		if err := services.ConfigureCopilotRerunCapture(controlservice.CopilotRerunCaptureEligibleVerifiedManual); err != nil {
			t.Fatal(err)
		}
	}
	runtime := &fakeGenerationRuntime{t: t, root: t.TempDir(), drain: make(chan struct{}), ready: true}
	var executions atomic.Int32
	runtime.execute = func(s *fakeGenerationSession, _ *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		executions.Add(1)
		return fakeGenerationResult(s, 0, nil)
	}
	options.configureAgent = func(conf *config.AgentConfig) {
		conf.Agent.Mode = "sandbox"
		conf.Agent.Sandbox = config.SandboxConfig{Enable: true, Runtime: "containerd", Containerd: config.ContainerdConfig{Policy: runtimePolicy}}
	}
	options.startWorker = func(ctx context.Context, conf *config.AgentConfig, client *agentgrpc.ClientWrapper, manager *executor.Manager) (nova.TaskQueue, error) {
		return startFakeGenerationIntegrationWorker(ctx, conf, client, manager, runtime, &generationACKLossTransport{})
	}
	options.afterProcess = func(f *controllerWorkerFixture) {
		if err := services.BindPipelineEngine(f.engine); err != nil {
			t.Fatal(err)
		}
	}
	f := newControllerWorkerFixture(t, false, options)
	t.Cleanup(func() { _ = repo.SetRetryApprovalAuthorizer(f.repos, nil) })
	capabilities, err := sonic.Marshal(taskpayload.AgentRuntimeCapabilities{ExecutionMode: taskpayload.RuntimeContainerdOfflineV1, ContainerdOfflineV1: true, ContainerdWholeJobRetryV1: true, ContainerdRetryApprovalV1: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repos.Agent.Patch(f.ctx, f.conf.Agent.ID, map[string]any{"os": "linux", "arch": "amd64", "runtime_capabilities": capabilities}); err != nil {
		t.Fatal(err)
	}
	// The earlier durable suite owns these reviewers until the parent completes.
	seedContainerApprovalActors(t, f, "earlier-owner-"+id.GetUild(), policy.Candidates)
	t.Run("terminal-scope", func(t *testing.T) {
		actor := "terminal-owner-" + id.GetUild()
		grants := seedContainerApprovalActors(t, f, actor, policy.Candidates)
		for _, reviewer := range policy.Candidates {
			var count int64
			if err := f.db.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", reviewer).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("shared reviewer must have exactly one identity: count=%d err=%v", count, err)
			}
		}
		h := newTerminalRerunHTTP(t, f, services, store, options.processConfig, append([]string{actor}, policy.Candidates...))
		source := newTerminalRerunSource(t, f.ctx, image, time.Now().UTC().Add(time.Minute).Truncate(time.Second))
		if err := f.db.WithContext(f.ctx).Model(&model.Pipeline{}).Where("pipeline_id = ?", f.pipelineID).Updates(map[string]any{"repo_url": source.URL, "default_branch": "main", "pipeline_file_path": terminalRerunDefinitionPath}).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.WithContext(f.ctx).Model(&model.Project{}).Where("project_id = ?", f.projectID).Updates(map[string]any{"repo_url": source.URL, "repo_type": model.RepoTypeGit}).Error; err != nil {
			t.Fatal(err)
		}
		// Reproduce the former double-seed state before the original trigger.
		// Production trigger capture still reads one candidate identity, whereas
		// the later transaction-local decision reader rejects any ambiguity.
		duplicate := &model.User{UserID: policy.Candidates[0], IsEnabled: 1}
		if err := f.db.WithContext(f.ctx).Create(duplicate).Error; err != nil {
			t.Fatal(err)
		}

		var triggered struct {
			RunID string `json:"runId"`
		}
		h.request(t, f.ctx, actor, http.MethodPost, "/pipelines/"+f.pipelineID+"/trigger", map[string]any{"requestId": id.GetUild(), "variables": source.Variables}, &triggered)
		job := terminalRerunWaitJob(t, f, triggered.RunID)
		approval := terminalRerunWaitApproval(t, f, job.JobRunID)
		if approval.BindingKind != retryapproval.Kind || approval.RetryJobRunID == nil || *approval.RetryJobRunID != job.JobRunID {
			t.Fatal("production source did not create retry-bound approval")
		}
		var snapshot model.CopilotDefinitionSnapshot
		if err := f.db.WithContext(f.ctx).Where("run_id = ?", triggered.RunID).First(&snapshot).Error; err != nil || snapshot.DefinitionRevision != source.Commit {
			t.Fatal("prospective source capture missing", err)
		}
		path := "/approvals/" + approval.ApprovalID + "/approve"
		h.reject(t, f.ctx, actor, path, map[string]string{"reason": "requester is not reviewer"}, http.StatusForbidden)
		if _, err := services.Approval.DecisionEligibility(f.ctx, approval, policy.Candidates[0], model.ApprovalDecisionApprove); !errors.Is(err, repo.ErrApprovalForbidden) {
			t.Fatal("duplicate identity did not fail transaction-local retry authorization", err)
		}
		h.reject(t, f.ctx, policy.Candidates[0], path, map[string]string{"reason": "ambiguous reviewer"}, http.StatusForbidden)
		t.Log("exact duplicate reviewer identity rejected with HTTP 403 by retry-bound production authorization")
		var votes int64
		if err := f.db.WithContext(f.ctx).Model(&model.ApprovalDecision{}).Where("approval_id = ?", approval.ApprovalID).Count(&votes).Error; err != nil || votes != 0 || executions.Load() != 0 {
			t.Fatal("denied ambiguity mutated approval or executed work", err)
		}
		if err := f.db.WithContext(f.ctx).Where("id = ?", duplicate.ID).Delete(&model.User{}).Error; err != nil {
			t.Fatal(err)
		}
		h.request(t, f.ctx, policy.Candidates[0], http.MethodPost, path, map[string]string{"reason": "first independent reviewer"}, nil)
		saved, err := services.Approval.GetApproval(f.ctx, approval.ApprovalID)
		if err != nil || saved.ApprovedCount != 1 || saved.Status != model.ApprovalStatusPending || executions.Load() != 0 {
			t.Fatal("first vote bypassed two-person quorum", err)
		}
		// Disable every current grant, including the earlier suite's grant; no stale
		// login/route cache or freshly added fixture role may replace live authority.
		if err := f.db.WithContext(f.ctx).Model(&model.RoleGrant{}).Where("subject_type = ? AND subject_id = ?", "user", policy.Candidates[1]).Update("is_enabled", 0).Error; err != nil {
			t.Fatal(err)
		}
		h.reject(t, f.ctx, policy.Candidates[1], path, map[string]string{"reason": "revoked reviewer"}, http.StatusForbidden)
		if err := f.db.WithContext(f.ctx).Model(&model.RoleGrant{}).Where("grant_id = ?", grants[2]).Update("is_enabled", 1).Error; err != nil {
			t.Fatal(err)
		}
		h.request(t, f.ctx, policy.Candidates[1], http.MethodPost, path, map[string]string{"reason": "second independent reviewer"}, nil)
		saved, err = services.Approval.GetApproval(f.ctx, approval.ApprovalID)
		if err != nil || saved.ApprovedCount != 2 || saved.Status != model.ApprovalStatusApproved {
			t.Fatal("independent quorum did not commit", err)
		}
		if err := f.engine.CancelRun(triggered.RunID); err != nil {
			t.Fatal(err)
		}
		f.waitRun(triggered.RunID, model.PipelineStatusCancelled)
		t.Log("real HTTP source trigger, frozen approval binding, requester denial, duplicate denial, live revocation, independent quorum and cancellation verified with local substitutes")
	})
	for _, reviewer := range policy.Candidates {
		var count int64
		if err := f.db.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", reviewer).Count(&count).Error; err != nil || count != 1 {
			t.Fatal("nested fixture cleanup deleted parent-owned reviewer", err)
		}
	}
}

// TestContainerTerminalApprovalActorIdentity 验证夹具不复活禁用用户也不消除歧义身份
func TestContainerTerminalApprovalActorIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "actors.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.WithContext(ctx).AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	created, err := ensureContainerApprovalActor(ctx, db, "candidate")
	if err != nil || !created {
		t.Fatal("missing actor was not created exactly once", err)
	}
	created, err = ensureContainerApprovalActor(ctx, db, "candidate")
	if err != nil || created {
		t.Fatal("existing actor was recreated", err)
	}
	if err := db.WithContext(ctx).Model(&model.User{}).Where("user_id = ?", "candidate").Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	if created, err := ensureContainerApprovalActor(ctx, db, "candidate"); err == nil || created {
		t.Fatal("disabled identity was silently revived")
	}
	if err := db.WithContext(ctx).Create(&model.User{UserID: "candidate", IsEnabled: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if created, err := ensureContainerApprovalActor(ctx, db, "candidate"); err == nil || created {
		t.Fatal("ambiguous identity was silently repaired")
	}
	var rows []model.User
	if err := db.WithContext(ctx).Where("user_id = ?", "candidate").Order("id").Find(&rows).Error; err != nil || len(rows) != 2 || rows[0].IsEnabled != 0 || rows[1].IsEnabled != 1 {
		t.Fatal("failure mutated original actor rows", err)
	}
}
