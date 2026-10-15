// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// mysqlAuthStepRequest 构建持久原代次和完整 v2 计划绑定的首步骤请求
func mysqlAuthStepRequest(t *testing.T, f *mysqlAuthFixture) taskqueue.StepAttemptTransition {
	t.Helper()
	var job model.JobRun
	var row model.JobRunGeneration
	if err := f.db.Where("job_run_id = ?", f.request.Identity.JobRunID).First(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Where("generation_id = ?", f.request.GenerationID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	var plan taskqueue.JobRetryPlan
	if err := sonic.UnmarshalString(job.RetryPlan, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Version != 2 || plan.Policy == nil || plan.Policy.Mode != "failed_step" || !plan.RetryApprovalRequired {
		t.Fatal("fixture lacks exact approved v2 plan")
	}
	var generation taskqueue.JobGeneration
	if err := sonic.UnmarshalString(row.GenerationJSON, &generation); err != nil {
		t.Fatal(err)
	}
	step := plan.Steps[0]
	session := taskqueue.StepAttemptSession{SessionID: generation.RuntimeClaimID, SessionIncarnationNonce: uuid.NewString(), Namespace: "fixture", ContainerID: "arcentra-" + generation.RuntimeClaimID, SnapshotID: "arcentra-" + generation.RuntimeClaimID + "-snapshot", Snapshotter: "overlayfs", ResourceOwnerDigest: strings.Repeat("a", 64), WorkspaceID: "job-" + generation.RuntimeClaimID, WorkspaceDevice: 1, WorkspaceInode: 2}
	scope := taskqueue.StepAttemptIdentity{Identity: f.request.Identity, GenerationID: f.request.GenerationID, GenerationBindingDigest: f.request.GenerationBindingDigest, RuntimeClaimID: generation.RuntimeClaimID, InputManifestDigest: f.request.InputManifestDigest, RetryPlanDigest: f.request.RetryPlanDigest, Session: session, LogicalStepRunID: step.AttemptRunIDs[0], StepRunID: step.AttemptRunIDs[0], StepName: step.Name, StepIndex: step.StepIndex, DefinitionDigest: step.DefinitionDigest, Attempt: 1, StepClaimID: agentidentity.StepClaimID(f.request.Identity.ClaimID, step.AttemptRunIDs[0])}
	r := taskqueue.StepAttemptTransition{Version: 1, Operation: "admit", Scope: scope}
	r.RequestDigest, _ = r.Digest()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	return r
}

// mysqlAuthStepRunning 使用真实 repository 与原认领提交执行前 RUNNING
func mysqlAuthStepRunning(f *mysqlAuthFixture, a *taskqueue.StepAttemptAdmission) error {
	ctx := agentidentity.WithStepAttemptReport(agentidentity.WithGenerationReport(context.Background(), a.Scope.GenerationID, a.Scope.GenerationBindingDigest), a.BindingDigest)
	step, err := f.repos.StepRun.GetByStepRunID(ctx, a.Scope.StepRunID)
	if err != nil {
		return err
	}
	return f.repos.StepRun.(*repo.StepRunRepo).PatchReportedStatus(ctx, step, map[string]any{"status": 3, "claim_id": a.Scope.StepClaimID, "generation_id": a.Scope.GenerationID, "generation_binding_digest": a.Scope.GenerationBindingDigest})
}

// TestStepRetryApprovalMySQLServiceCurrentGraph 验证真实授权图撤销阻止新步骤授权但保留历史准入
func TestStepRetryApprovalMySQLServiceCurrentGraph(t *testing.T) {
	for _, kind := range []string{"role", "grant", "team", "binding"} {
		for _, stage := range []string{"first-admission", "running"} {
			t.Run(kind+"/"+stage, func(t *testing.T) {
				db, other, p := mysqlAuthDatabase(t)
				f := mysqlAuthSeedMode(t, db, p, p+"project", p+"pipeline", p+"alice", p+"bob", 60, time.Time{}, "failed_step")
				mysqlAuthApprove(t, f)
				if r, err := f.admit(context.Background()); err != nil || r == nil || !r.ExecutionAllowed {
					t.Fatalf("startup approval: %v", err)
				}
				req := mysqlAuthStepRequest(t, f)
				jobs := repo.NewJobRunRepo(generationServiceDatabase{db}).(*repo.JobRunRepo)
				var a *taskqueue.StepAttemptAdmission
				var original string
				if stage == "running" {
					r, err := jobs.ReconcileStepAttempt(context.Background(), req)
					if err != nil || r == nil || !r.ExecutionAllowed {
						t.Fatalf("first admission: %v", err)
					}
					a = r.Admission
					original, _ = a.Canonical()
				}
				audit := mysqlAuthAuditQueries(t, db, p+"alice", p+"bob")
				var err error
				switch kind {
				case "role":
					err = other.Model(&model.Role{}).Where("role_id = ?", p+"review-role").Update("is_enabled", 0).Error
				case "grant":
					err = other.Model(&model.RoleGrant{}).Where("grant_id = ?", p+"reviewer-grant").Update("is_enabled", 0).Error
				case "team":
					err = other.Model(&model.Team{}).Where("team_id = ?", p+"team").Update("is_enabled", 0).Error
				case "binding":
					err = other.Where("role_id = ? AND permission_id = ?", p+"review-role", p+"approval:approve").Delete(&model.RolePermissionBinding{}).Error
				}
				if err != nil {
					t.Fatal(err)
				}
				if stage == "running" {
					if mysqlAuthStepRunning(f, a) == nil {
						t.Fatal("revoked actual graph allowed RUNNING")
					}
					r, err := jobs.ReconcileStepAttempt(context.Background(), req)
					if err != nil || r == nil || r.ExecutionAllowed || r.Admission == nil {
						t.Fatalf("historical admission lost or executable: %+v %v", r, err)
					}
					got, _ := r.Admission.Canonical()
					if got != original {
						t.Fatal("revocation changed committed admission bytes")
					}
				} else {
					r, err := jobs.ReconcileStepAttempt(context.Background(), req)
					if err == nil && (r == nil || r.ExecutionAllowed) {
						t.Fatal("revoked actual graph allowed admission")
					}
					var step model.StepRun
					if err := db.Where("step_run_id = ?", req.Scope.StepRunID).First(&step).Error; err != nil {
						t.Fatal(err)
					}
					if step.AttemptAdmission != "" {
						t.Fatal("denied new admission wrote ledger")
					}
				}
				audit.check(t, false)
			})
		}
	}
}

// TestStepRetryApprovalMySQLServiceCrossedGraphs 验证两作业交叉审批人通过实际服务授权图并发准入
func TestStepRetryApprovalMySQLServiceCrossedGraphs(t *testing.T) {
	db, other, p := mysqlAuthDatabase(t)
	a := mysqlAuthSeedMode(t, db, p+"a-", p+"project", p+"pipeline", p+"alice", p+"bob", 60, time.Time{}, "failed_step")
	b := mysqlAuthSeedMode(t, other, p+"b-", p+"project", p+"pipeline", p+"bob", p+"alice", 60, time.Time{}, "failed_step")
	for _, f := range []*mysqlAuthFixture{a, b} {
		mysqlAuthApprove(t, f)
		if r, err := f.admit(context.Background()); err != nil || r == nil || !r.ExecutionAllowed {
			t.Fatalf("startup: %v", err)
		}
	}
	requests := []taskqueue.StepAttemptTransition{mysqlAuthStepRequest(t, a), mysqlAuthStepRequest(t, b)}
	audits := []*mysqlAuthAudit{mysqlAuthAuditQueries(t, db, p+"alice", p+"bob"), mysqlAuthAuditQueries(t, other, p+"alice", p+"bob")}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start, done := make(chan struct{}), make(chan error, 2)
	for i, f := range []*mysqlAuthFixture{a, b} {
		go func(f *mysqlAuthFixture, req taskqueue.StepAttemptTransition) {
			<-start
			r, err := repo.NewJobRunRepo(generationServiceDatabase{f.db}).(*repo.JobRunRepo).ReconcileStepAttempt(ctx, req)
			if err == nil && (r == nil || !r.ExecutionAllowed || r.Admission == nil) {
				err = fmt.Errorf("missing current approval-backed step admission")
			}
			done <- err
		}(f, requests[i])
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := mysqlAuthAwait(t, done); err != nil {
			t.Fatal(err)
		}
	}
	for _, audit := range audits {
		audit.check(t, true)
	}
}

// TestStepRetryApprovalServiceFixtureSQLite 仅验证组合 fixture 与真实服务授权契约，不作为 MySQL 锁证据
func TestStepRetryApprovalServiceFixtureSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "fixture.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = repo.SetRetryApprovalAuthorizer(repo.NewRepositories(generationServiceDatabase{db}, nil), nil)
		_ = pool.Close()
	})
	if err := db.AutoMigrate(&model.Agent{}, &model.PipelineRun{}, &model.Pipeline{}, &model.Project{}, &model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}, &model.JobRunGeneration{}, &model.User{}, &model.TeamMember{}, &model.Team{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Role{}, &model.RoleGrant{}); err != nil {
		t.Fatal(err)
	}
	f := mysqlAuthSeedMode(t, db, "local-", "local-project", "local-pipeline", "local-actor", "local-voter", 60, time.Time{}, "failed_step")
	req := mysqlAuthStepRequest(t, f)
	jobs := repo.NewJobRunRepo(generationServiceDatabase{db}).(*repo.JobRunRepo)
	if r, err := jobs.ReconcileStepAttempt(context.Background(), req); err == nil && (r == nil || r.ExecutionAllowed) {
		t.Fatal("pending approval admitted first step")
	}
	mysqlAuthApprove(t, f)
	if r, err := f.admit(context.Background()); err != nil || r == nil || !r.ExecutionAllowed {
		t.Fatalf("startup: %+v %v", r, err)
	}
	r, err := jobs.ReconcileStepAttempt(context.Background(), req)
	if err != nil || r == nil || !r.ExecutionAllowed {
		t.Fatalf("first: %+v %v", r, err)
	}
	before, _ := r.Admission.Canonical()
	if err := db.Model(&model.Team{}).Where("team_id = ?", "local-team").Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	if mysqlAuthStepRunning(f, r.Admission) == nil {
		t.Fatal("revoked voter team allowed RUNNING")
	}
	replay, err := jobs.ReconcileStepAttempt(context.Background(), req)
	if err != nil || replay == nil || replay.Admission == nil || replay.ExecutionAllowed {
		t.Fatalf("historical ACK: %+v %v", replay, err)
	}
	after, _ := replay.Admission.Canonical()
	if after != before {
		t.Fatal("historical ACK bytes changed")
	}
}
