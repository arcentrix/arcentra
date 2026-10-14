// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
)

func TestRetryApprovalPolicyNormalizationIsIndependentAndBounded(t *testing.T) {
	p := &retryapproval.Policy{ID: "retry", Candidates: []string{"b", "a"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 60, Fingerprint: "v1:" + strings.Repeat("f", 64)}
	service := &Services{}
	if err := service.ConfigureRetryApprovalPolicy(p); err != nil {
		t.Fatal(err)
	}
	original := service.retryApprovalPolicy.Fingerprint
	if original == p.Fingerprint || service.retryApprovalPolicy.Candidates[0] != "a" {
		t.Fatal("caller digest or candidate order trusted")
	}
	p.Candidates[0] = "mutated"
	if service.retryApprovalPolicy.Candidates[1] != "b" {
		t.Fatal("configuration aliases caller")
	}
	for _, seconds := range []int64{0, 3601, -1} {
		copy := *p
		copy.TimeoutSeconds = seconds
		if err := service.ConfigureRetryApprovalPolicy(&copy); err == nil {
			t.Fatal("unbounded policy accepted")
		}
	}
	if service.retryApprovalPolicy.Fingerprint != original {
		t.Fatal("failed update changed trusted policy")
	}
}

func TestRetryApprovalTriggerRejectsSpoofAndUsesCurrentProjectAuthorization(t *testing.T) {
	_, db, _, repositories := liveDeploymentAuthorizationFixture(t, false)
	for _, value := range []any{&model.User{UserID: "reviewer", IsEnabled: 1}, &model.RoleGrant{GrantID: "reviewer-grant", SubjectType: "user", SubjectID: "reviewer", RoleID: "deploy-role", ScopeType: "project", ScopeID: "project", IsEnabled: 1}, &model.Permission{PermissionID: "approval:approve", ResourceType: "approval", Action: "approve"}, &model.RolePermissionBinding{RoleID: "deploy-role", PermissionID: "approval:approve"}} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	services := &Services{repos: repositories}
	if err := services.ConfigureRetryApprovalPolicy(&retryapproval.Policy{ID: "retry", Candidates: []string{"actor", "reviewer"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	svc := &PipelineServiceImpl{services: services, offlineRuntimePolicy: taskqueue.OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowWholeJobRetryApproval: true}}
	parsed := &pipelinev1.Spec{Namespace: "test", Runtime: &pipelinev1.Runtime{Type: taskqueue.RuntimeContainerdOfflineV1}, Jobs: []*pipelinev1.Job{{Name: "build", Retry: &pipelinev1.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}, Approval: &pipelinev1.Approval{Required: true, Type: "manual", Plugin: retryapproval.Plugin}}}}
	request := &pipelinev1.TriggerPipelineRequest{PipelineId: "pipe", TriggeredBy: "actor"}
	pipeline := &model.Pipeline{PipelineID: "pipe", ProjectID: "project"}
	fresh := func() *model.PipelineRun {
		return &model.PipelineRun{RunID: "run", PipelineID: "pipe", DefinitionCommitSha: strings.Repeat("a", 40), DefinitionPath: ".arcentra.yaml", TriggerType: 1, TriggeredBy: "actor"}
	}
	if err := svc.captureRetryApprovalTrigger(context.Background(), request, pipeline, fresh(), parsed); err == nil {
		t.Fatal("body actor authorized direct invocation")
	}
	ctx := WithVerifiedPipelineActor(context.Background(), "actor")
	spoof := proto.Clone(request).(*pipelinev1.TriggerPipelineRequest)
	spoof.TriggeredBy = "reviewer"
	if err := svc.captureRetryApprovalTrigger(ctx, spoof, pipeline, fresh(), parsed); err == nil {
		t.Fatal("spoofed actor authorized")
	}
	run := fresh()
	if err := svc.captureRetryApprovalTrigger(ctx, request, pipeline, run, parsed); err != nil {
		t.Fatal(err)
	}
	capture, err := retryapproval.ParseRunContext(run.RetryApprovalContext)
	if err != nil || len(capture.Policy.Candidates) != 1 || capture.Policy.Candidates[0] != "reviewer" || capture.ActorID != "actor" || capture.Policy.Fingerprint != services.retryApprovalPolicy.Fingerprint {
		t.Fatalf("bad trusted capture: %+v %v", capture, err)
	}
	if err := validateRetryApprovalDuplicate(ctx, request, run); err != nil {
		t.Fatal(err)
	}
	if err := validateRetryApprovalDuplicate(context.Background(), request, run); err == nil {
		t.Fatal("unverified request-ID replay accepted")
	}
	cross := *pipeline
	cross.ProjectID = "foreign"
	if err := svc.captureRetryApprovalTrigger(ctx, request, &cross, fresh(), parsed); err == nil {
		t.Fatal("cross-project authorization accepted")
	}
	if err := db.Model(&model.RoleGrant{}).Where("grant_id = ?", "reviewer-grant").Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.captureRetryApprovalTrigger(ctx, request, pipeline, fresh(), parsed); err == nil {
		t.Fatal("revoked sole approver left valid quorum")
	}
}

func TestRetryApprovalDecisionRoutingFailsClosedForBrokenKinds(t *testing.T) {
	s := &ApprovalService{}
	job := "job"
	release := "release"
	for _, value := range []*model.ApprovalRequest{
		{Plugin: retryapproval.Plugin}, {BindingKind: retryapproval.Kind}, {BindingKind: "unknown"}, {Plugin: retryapproval.Plugin, BindingKind: retryapproval.Kind, RetryJobRunID: &job, DeploymentReleaseID: &release}, {Plugin: "builtin:deployment"},
	} {
		if err := s.validateDeploymentDecision(context.Background(), value, "actor", model.ApprovalDecisionApprove); err == nil {
			t.Fatalf("broken binding accepted: %+v", value)
		}
	}
}
