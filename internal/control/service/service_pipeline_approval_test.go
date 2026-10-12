package service

import (
	"context"
	"strings"
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	cluster "github.com/arcentrix/arcentra/internal/shared/deployment"
)

func TestPipelineApprovalReviewBindsOperatorPolicyAndTriggerCannotOmitGate(t *testing.T) {
	s, db, execution, adapter, _ := deploymentApprovalFixture(t)
	run, err := s.pipelines.GetRun(context.Background(), execution.PipelineRunID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := cluster.ParsePipelinePlan(run.DeploymentPlan, run.DeploymentPlanHash)
	if err != nil {
		t.Fatal(err)
	}
	definition := pipelineTriggerSpec(t, plan.EnvironmentID)
	definition.Jobs[1].Approval = &pipelinev1.Approval{Required: true, Type: "manual", Plugin: "builtin:deployment"}
	pipelines := &PipelineServiceImpl{pipelineRepo: s.pipelines, deployment: s}
	review, token, err := pipelines.GetPipelineDeploymentReview(context.Background(), execution.PipelineID, definition)
	if err != nil || review == nil || !review.ApprovalRequired || review.Target.PipelineApproval == nil || token == "" {
		t.Fatalf("gate review missing: %+v %q %v", review, token, err)
	}
	request := &pipelinev1.TriggerPipelineRequest{PipelineId: execution.PipelineID, TriggeredBy: execution.ActorID, AllowDeployments: true, ExpectedDefinitionCommitSha: strings.Repeat("a", 40), ExpectedDeploymentPlanHash: token}
	ctx := WithVerifiedPipelineActor(context.Background(), execution.ActorID)
	pipeline := &model.Pipeline{PipelineID: execution.PipelineID, ProjectID: execution.ProjectID}
	if consent, _, err := pipelines.authorizeDeploymentTrigger(ctx, request, pipeline, request.ExpectedDefinitionCommitSha, definition, false); err != nil || consent == nil {
		t.Fatalf("approved review trigger rejected: %+v %v", consent, err)
	}
	definition.Jobs[1].Approval = nil
	if _, code, err := pipelines.authorizeDeploymentTrigger(ctx, request, pipeline, request.ExpectedDefinitionCommitSha, definition, false); err == nil || code != 400 {
		t.Fatalf("operator policy bypassed: %d %v", code, err)
	}
	definition.Jobs[1].Approval = &pipelinev1.Approval{Required: true, Type: "manual", Plugin: "builtin:deployment"}
	binding := s.targets["staging"]
	binding.PipelineApproval.TimeoutSeconds++
	binding.Fingerprint = deploymentTargetFingerprint("staging", binding, "")
	s.targets["staging"] = binding
	if _, code, err := pipelines.authorizeDeploymentTrigger(ctx, request, pipeline, request.ExpectedDefinitionCommitSha, definition, false); err == nil || code != 409 {
		t.Fatalf("policy change reused old consent: %d %v", code, err)
	}
	if adapter.inspect+adapter.apply+adapter.observe != 0 {
		t.Fatal("review or trigger touched Kubernetes")
	}
	// Missing decision/authorization wiring must not silently disable the gate.
	s.ConfigureDeploymentApprovals(repo.NewApprovalDecisionRepo(artifactServiceDatabase{db}), nil, nil)
	if _, token, err := pipelines.GetPipelineDeploymentReview(context.Background(), execution.PipelineID, definition); err == nil || token != "" {
		t.Fatal("unwired durable gate became executable")
	}
}

func TestDeploymentApprovalFingerprintNormalizesPolicyAndBindsEveryLimit(t *testing.T) {
	base := DeploymentTargetBinding{ProjectID: "project", Tier: "staging", Target: cluster.Target{Namespace: "staging", Deployment: "web", Container: "app"}, PipelineApproval: &cluster.PipelineApprovalPolicy{Approvers: []string{"b", "a"}, MinApprovals: 1, TimeoutSeconds: 60}}
	first := deploymentTargetFingerprint("target", base, "https://fixture.invalid")
	ordered := base
	ordered.PipelineApproval = cluster.ClonePipelineApprovalPolicy(base.PipelineApproval)
	if deploymentTargetFingerprint("target", ordered, "https://fixture.invalid") != first {
		t.Fatal("equivalent candidate ordering changed target fingerprint")
	}
	for _, mutate := range []func(*cluster.PipelineApprovalPolicy){func(p *cluster.PipelineApprovalPolicy) { p.TimeoutSeconds++ }, func(p *cluster.PipelineApprovalPolicy) { p.MinApprovals++ }, func(p *cluster.PipelineApprovalPolicy) { p.Approvers[0] = "other" }} {
		copy := base
		copy.PipelineApproval = cluster.ClonePipelineApprovalPolicy(base.PipelineApproval)
		mutate(copy.PipelineApproval)
		if deploymentTargetFingerprint("target", copy, "https://fixture.invalid") == first {
			t.Fatal("policy limit missing from target fingerprint")
		}
	}
	base.PipelineApproval = nil
	if deploymentTargetFingerprint("target", base, "https://fixture.invalid") == first {
		t.Fatal("policy removal retained old fingerprint")
	}
}
