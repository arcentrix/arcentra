// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"context"
	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"path/filepath"
	"strings"
	"testing"
)

func retryApprovalServiceFixture(t *testing.T) (*gorm.DB, *service.Services, agentv1.AgentServiceClient, *retryapproval.Request, string) {
	t.Helper()
	db, admit, manifest := generationServiceFixture(t, filepath.Join(t.TempDir(), "approval.db"))
	ctx := context.Background()
	if err := db.AutoMigrate(&model.Pipeline{}, &model.Project{}, &model.User{}, &model.Team{}, &model.TeamMember{}, &model.Role{}, &model.RoleGrant{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{
		&model.User{UserID: "requester", IsEnabled: 1}, &model.User{UserID: "reviewer", IsEnabled: 1}, &model.Role{RoleID: "review-role", Name: "review", IsEnabled: 1},
		&model.RoleGrant{GrantID: "requester-grant", SubjectType: "user", SubjectID: "requester", RoleID: "review-role", ScopeType: "project", ScopeID: "project", IsEnabled: 1},
		&model.RoleGrant{GrantID: "reviewer-grant", SubjectType: "user", SubjectID: "reviewer", RoleID: "review-role", ScopeType: "project", ScopeID: "project", IsEnabled: 1},
	} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"project:read", "pipeline:read", "pipeline:trigger", "approval:approve", "approval:reject"} {
		parts := strings.SplitN(action, ":", 2)
		for _, value := range []any{&model.Permission{PermissionID: action, ResourceType: parts[0], Action: parts[1]}, &model.RolePermissionBinding{RoleID: "review-role", PermissionID: action}} {
			if err := db.Create(value).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	policy := retryapproval.Policy{ID: "retry", Candidates: []string{"reviewer"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 60}
	policy.Fingerprint, _ = policy.Digest()
	capture := retryapproval.RunContext{Version: 1, ProjectID: "project", PipelineID: "pipeline", PipelineRunID: "run", ActorID: "requester", DefinitionCommitSHA: strings.Repeat("c", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: "v1:" + strings.Repeat("d", 64), GatedJobs: []string{"build"}, Policy: policy}
	body, err := capture.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := capture.Digest()
	if err := db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Updates(map[string]any{"retry_approval_context": body, "retry_approval_context_digest": digest, "trigger_type": 1, "triggered_by": "requester", "definition_commit_sha": capture.DefinitionCommitSHA, "definition_path": capture.DefinitionPath}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"job_name": "build", "retry_approval_required": true}).Error; err != nil {
		t.Fatal(err)
	}

	// 重建带显式门禁和固定预算的完整计划，而非复用旧无门禁计划摘要
	runtime := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: "fixture/image@sha256:" + strings.Repeat("a", 64), Resources: taskqueue.RuntimeResources{CPU: "500m", Memory: "64Mi"}, BindingDigest: admit.Identity.RuntimeBindingDigest}
	payload := &taskqueue.JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", JobName: "build", AgentID: "agent", Runtime: runtime, Retry: &pipelinev1.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}, RetryApprovalRequired: true, RetryApprovalTimeoutSeconds: policy.TimeoutSeconds, Steps: []taskqueue.StepPayload{{Name: "shell", StepRunID: "step-1", AttemptRunIDs: []string{"step-1", "step-2"}, Uses: "shell", Args: map[string]any{"run": "exit 9"}}}}
	plan, err := taskqueue.NewJobRetryPlan(payload)
	if err != nil {
		t.Fatal(err)
	}
	planBody, _ := plan.Canonical()
	planDigest, _ := plan.Digest("pipeline", "run", "job", "agent", runtime.BindingDigest)
	if err := db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"retry_plan": planBody, "retry_plan_digest": planDigest}).Error; err != nil {
		t.Fatal(err)
	}
	admit.RetryPlanDigest = planDigest
	admit.RequestDigest, _ = admit.Digest()
	repositories := repo.NewRepositories(generationServiceDatabase{db}, nil)
	services := service.NewServices(generationServiceDatabase{db}, nil, repositories)
	if err := services.ConfigureRetryApprovalPolicy(&policy); err != nil {
		t.Fatal(err)
	}
	if err := services.ConfigureRetryApprovalAuthorization(); err != nil {
		t.Fatal(err)
	}
	client, _, _ := generationClient(t, db, nil)
	generation, err := reconcileGeneration(t, client, admit)
	if err != nil {
		t.Fatal(err)
	}
	pin := generationServicePin(t, client, admit, manifest)
	job, err := repositories.JobRun.GetByJobRunID(ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	request := &retryapproval.Request{Version: 1, Operation: "observe", Identity: admit.Identity, GenerationID: generation.GenerationID, GenerationBindingDigest: generation.BindingDigest, RetryPlanDigest: admit.RetryPlanDigest, InputManifestDigest: pin}
	return db, services, client, request, job.RetryApprovalID
}

func callServiceRetryApproval(ctx context.Context, client agentv1.AgentServiceClient, r *retryapproval.Request) (*retryapproval.Response, error) {
	body, err := r.Canonical()
	if err != nil {
		return nil, err
	}
	response, err := client.ReconcileJobRetryApproval(ctx, &agentv1.ReconcileJobRetryApprovalRequest{AgentId: r.Identity.AgentID, RequestJson: body})
	if err != nil {
		return nil, err
	}
	return retryapproval.ParseResponse(response.ResponseJson)
}

func TestRetryApprovalAuthenticatedRPCAndCurrentAuthorization(t *testing.T) {
	db, services, client, request, approvalID := retryApprovalServiceFixture(t)
	if _, err := callServiceRetryApproval(context.Background(), client, request); status.Code(err) != codes.Unauthenticated {
		t.Fatal("anonymous approval observation accepted", err)
	}
	response, err := callServiceRetryApproval(generationRPCContext("original"), client, request)
	if err != nil || response.State != "pending" || response.ExecutionAllowed || response.Admission != nil {
		t.Fatal("pending grants execution", response, err)
	}
	foreign := *request
	foreign.Identity.AgentID = "other"
	if _, err := callServiceRetryApproval(generationRPCContext("original"), client, &foreign); err == nil {
		t.Fatal("cross Agent request accepted")
	}
	if _, err := callServiceRetryApproval(generationRPCContext("foreign-claim"), client, request); err == nil {
		t.Fatal("foreign claim accepted")
	}
	request.Operation = "admit"
	if _, err := callServiceRetryApproval(generationRPCContext("original"), client, request); err == nil {
		t.Fatal("pending startup accepted")
	}
	if err := services.Approval.Approve(context.Background(), approvalID, "requester", ""); err == nil {
		t.Fatal("self approval accepted")
	}
	if err := services.Approval.Approve(context.Background(), approvalID, "reviewer", "approved"); err != nil {
		t.Fatal(err)
	}
	response, err = callServiceRetryApproval(generationRPCContext("original"), client, request)
	if err != nil || !response.ExecutionAllowed || response.Admission == nil {
		t.Fatal("approved exact startup denied", response, err)
	}
	receipt, _ := response.Admission.Canonical()
	// 已提交撤权必须在下一次准入的同事务权限读取中可见
	if err := db.Model(&model.RoleGrant{}).Where("grant_id = ?", "reviewer-grant").Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := callServiceRetryApproval(generationRPCContext("original"), client, request); err == nil {
		t.Fatal("revoked approving voter admitted startup")
	}
	if err := services.Approval.Approve(context.Background(), approvalID, "reviewer", "approved"); err != nil {
		t.Fatal("historical vote ACK replay denied", err)
	}
	var votes int64
	if err := db.Model(&model.ApprovalDecision{}).Where("approval_id = ?", approvalID).Count(&votes).Error; err != nil || votes != 1 {
		t.Fatal("duplicate vote", votes, err)
	}
	var generation model.JobRunGeneration
	if err := db.Where("generation_id = ?", request.GenerationID).First(&generation).Error; err != nil {
		t.Fatal(err)
	}
	if generation.RetryApprovalAdmission != receipt {
		t.Fatal("historical receipt changed after revocation")
	}
}

func TestRetryApprovalCurrentRequesterAndPolicyRemainRequired(t *testing.T) {
	for _, name := range []string{"requester disabled", "policy changed", "role disabled", "pipeline disabled"} {
		t.Run(name, func(t *testing.T) {
			db, services, client, request, approvalID := retryApprovalServiceFixture(t)
			if err := services.Approval.Approve(context.Background(), approvalID, "reviewer", ""); err != nil {
				t.Fatal(err)
			}
			var err error
			switch name {
			case "requester disabled":
				err = db.Model(&model.User{}).Where("user_id = ?", "requester").Update("is_enabled", 0).Error
			case "policy changed":
				err = services.ConfigureRetryApprovalPolicy(&retryapproval.Policy{ID: "retry", Candidates: []string{"reviewer"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 61})
			case "role disabled":
				err = db.Model(&model.Role{}).Where("role_id = ?", "review-role").Update("is_enabled", 0).Error
			case "pipeline disabled":
				err = db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipeline").Update("is_enabled", 0).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			request.Operation = "admit"
			if _, err := callServiceRetryApproval(generationRPCContext("original"), client, request); err == nil {
				t.Fatal("invalid live authority admitted startup")
			}
		})
	}
}
