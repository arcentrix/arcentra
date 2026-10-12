package deployment

import (
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"testing"
)

func deploymentPlanSpec(t *testing.T) *pipelinev1.Spec {
	t.Helper()
	args, err := structpb.NewStruct(map[string]any{"environmentId": "environment-1", "artifact": map[string]any{"job": "build", "step": "upload", "name": "image-manifest.json"}})
	if err != nil {
		t.Fatal(err)
	}
	return &pipelinev1.Spec{Namespace: "test", Jobs: []*pipelinev1.Job{{Name: "build", Steps: []*pipelinev1.Step{{Name: "upload", Uses: "builtin:artifacts", Action: "upload"}}}, {Name: "deploy", DependsOn: []string{"build"}, Timeout: "30s", Steps: []*pipelinev1.Step{{Name: "release", Uses: "builtin:kubernetes", Action: "deploy", Args: args}}}}}
}
func TestPipelineDeploymentPlanCanonicalRoundTrip(t *testing.T) {
	plan, err := DecodePipelinePlan(deploymentPlanSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	data, hash, err := plan.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParsePipelinePlan(data, hash)
	if err != nil || decoded.EnvironmentID != "environment-1" || len(decoded.Predecessors) != 1 || decoded.Predecessors[0].Steps[0].Index != 0 {
		t.Fatalf("roundtrip: %+v %v", decoded, err)
	}
	if _, err := ParsePipelinePlan(data+" ", hash); err == nil {
		t.Fatal("tamper accepted")
	}
}
func TestPipelineDeploymentPlanRejectsUnbridgedOrUnboundedShapes(t *testing.T) {
	tests := map[string]func(*pipelinev1.Spec){
		"wrong action":    func(p *pipelinev1.Spec) { p.Jobs[1].Steps[0].Action = "apply" },
		"retry":           func(p *pipelinev1.Spec) { p.Jobs[1].Retry = &pipelinev1.Retry{} },
		"agent":           func(p *pipelinev1.Spec) { p.Jobs[1].Steps[0].RunOnAgent = true },
		"selector":        func(p *pipelinev1.Spec) { p.Jobs[1].Steps[0].AgentSelector = &pipelinev1.AgentSelector{} },
		"ignored failure": func(p *pipelinev1.Spec) { p.Jobs[1].Steps[0].ContinueOnError = true },
		"job source":      func(p *pipelinev1.Spec) { p.Jobs[1].Source = &pipelinev1.Source{} },
		"legacy target":   func(p *pipelinev1.Spec) { p.Jobs[0].Target = &pipelinev1.Target{} },
		"any approval":    func(p *pipelinev1.Spec) { p.Jobs[0].Approval = &pipelinev1.Approval{} },
		"mixed steps": func(p *pipelinev1.Spec) {
			p.Jobs[1].Steps = append(p.Jobs[1].Steps, &pipelinev1.Step{Name: "shell", Uses: "builtin:shell"})
		},
		"no timeout":       func(p *pipelinev1.Spec) { p.Jobs[1].Timeout = "" },
		"large timeout":    func(p *pipelinev1.Spec) { p.Jobs[1].Timeout = "2h" },
		"fraction timeout": func(p *pipelinev1.Spec) { p.Jobs[1].Timeout = "1.5s" },
		"condition":        func(p *pipelinev1.Spec) { p.Jobs[1].When = "false" },
		"interpolation": func(p *pipelinev1.Spec) {
			p.Jobs[1].Steps[0].Args.Fields["environmentId"] = structpb.NewStringValue("${ENV}")
		},
		"arbitrary endpoint": func(p *pipelinev1.Spec) {
			p.Jobs[1].Steps[0].Args.Fields["endpoint"] = structpb.NewStringValue("https://bad")
		},
		"missing explicit producer": func(p *pipelinev1.Spec) { p.Jobs[1].DependsOn = nil },
		"extra root": func(p *pipelinev1.Spec) {
			p.Jobs = append(p.Jobs, &pipelinev1.Job{Name: "unchecked", Steps: []*pipelinev1.Step{{Name: "test", Uses: "builtin:shell"}}})
		},
		"second deployment": func(p *pipelinev1.Spec) {
			p.Jobs = append(p.Jobs, proto.Clone(p.Jobs[1]).(*pipelinev1.Job))
			p.Jobs[2].Name = "another"
		},
		"producer not upload":      func(p *pipelinev1.Spec) { p.Jobs[0].Steps[0].Uses = "builtin:shell" },
		"producer failure ignored": func(p *pipelinev1.Spec) { p.Jobs[0].Steps[0].ContinueOnError = true },
		"cycle":                    func(p *pipelinev1.Spec) { p.Jobs[0].DependsOn = []string{"deploy"} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := deploymentPlanSpec(t)
			mutate(p)
			if plan, err := DecodePipelinePlan(p); err == nil {
				t.Fatalf("unsafe plan accepted: %+v", plan)
			}
		})
	}
}
func TestPipelineDeploymentRequiresCompletePredecessorClosure(t *testing.T) {
	p := deploymentPlanSpec(t)
	p.Jobs = append(p.Jobs, &pipelinev1.Job{Name: "test", DependsOn: []string{"build"}, Steps: []*pipelinev1.Step{{Name: "check", Uses: "builtin:shell"}}})
	p.Jobs[1].DependsOn = append(p.Jobs[1].DependsOn, "test")
	plan, err := DecodePipelinePlan(p)
	if err != nil || len(plan.Predecessors) != 2 {
		t.Fatalf("valid predecessor closure rejected: %+v %v", plan, err)
	}
}
func TestPipelineWithoutDeploymentPreservesExistingApprovalParsing(t *testing.T) {
	p := deploymentPlanSpec(t)
	p.Jobs = p.Jobs[:1]
	if plan, err := DecodePipelinePlan(p); err != nil || plan != nil {
		t.Fatalf("ordinary CI changed: %+v %v", plan, err)
	}
	p.Jobs[0].Approval = &pipelinev1.Approval{Required: true}
	if plan, err := DecodePipelinePlan(p); err != nil || plan != nil {
		t.Fatal("ordinary pipeline approval parsing changed")
	}
}

func TestPipelineDeploymentRejectsAutomaticTriggersButPreservesOrdinaryCI(t *testing.T) {
	for _, kind := range []string{"cron", "event", "unknown"} {
		p := deploymentPlanSpec(t)
		p.Triggers = []*pipelinev1.Trigger{{Type: kind}}
		if _, err := DecodePipelinePlan(p); err == nil {
			t.Fatalf("%s deployment trigger accepted", kind)
		}
		p.Jobs = p.Jobs[:1]
		if plan, err := DecodePipelinePlan(p); err != nil || plan != nil {
			t.Fatalf("ordinary %s pipeline changed: %v", kind, err)
		}
	}
	p := deploymentPlanSpec(t)
	p.Triggers = []*pipelinev1.Trigger{{Type: "manual"}}
	if _, err := DecodePipelinePlan(p); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineDeploymentAcceptsOnlyExactDurableFinalGate(t *testing.T) {
	p := deploymentPlanSpec(t)
	p.Jobs[1].Approval = &pipelinev1.Approval{Required: true, Type: "manual", Plugin: "builtin:deployment"}
	plan, err := DecodePipelinePlan(p)
	if err != nil || plan == nil || !plan.ApprovalRequired {
		t.Fatalf("durable gate rejected: %+v %v", plan, err)
	}
	data, digest, err := plan.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParsePipelinePlan(data, digest)
	if err != nil || !decoded.ApprovalRequired {
		t.Fatal("gate missing from pinned plan")
	}
	for _, mutate := range []func(*pipelinev1.Spec){
		func(p *pipelinev1.Spec) { p.Jobs[1].Approval.Required = false },
		func(p *pipelinev1.Spec) { p.Jobs[1].Approval.Type = "auto" },
		func(p *pipelinev1.Spec) { p.Jobs[1].Approval.Type = "" },
		func(p *pipelinev1.Spec) { p.Jobs[1].Approval.Plugin = "external" },
		func(p *pipelinev1.Spec) { p.Jobs[1].Approval.Plugin = "" },
		func(p *pipelinev1.Spec) { p.Jobs[0].Approval = p.Jobs[1].Approval; p.Jobs[1].Approval = nil },
		func(p *pipelinev1.Spec) {
			p.Jobs[1].Approval.Params, _ = structpb.NewStruct(map[string]any{"approvers": []any{"requester"}})
		},
	} {
		copy := proto.Clone(p).(*pipelinev1.Spec)
		mutate(copy)
		if _, err := DecodePipelinePlan(copy); err == nil {
			t.Fatalf("unsupported gate accepted: %+v", copy)
		}
	}
}

func TestPipelineApprovalPolicyValidatesAndCopiesOperatorCandidates(t *testing.T) {
	valid := &PipelineApprovalPolicy{Approvers: []string{"bob", "alice"}, MinApprovals: 2, TimeoutSeconds: 600}
	if err := ValidatePipelineApprovalPolicy(valid); err != nil {
		t.Fatal(err)
	}
	copy := ClonePipelineApprovalPolicy(valid)
	valid.Approvers[0] = "changed"
	if copy.Approvers[0] != "alice" || copy.Approvers[1] != "bob" {
		t.Fatal("operator candidate copy changed")
	}
	for _, invalid := range []*PipelineApprovalPolicy{
		{}, {Approvers: []string{"a", "a"}, MinApprovals: 1, TimeoutSeconds: 30},
		{Approvers: []string{"a"}, MinApprovals: 2, TimeoutSeconds: 30},
		{Approvers: []string{"a"}, MinApprovals: 0, TimeoutSeconds: 30},
		{Approvers: []string{"a"}, MinApprovals: 1, TimeoutSeconds: 0},
		{Approvers: []string{"a"}, MinApprovals: 1, TimeoutSeconds: 3601},
		{Approvers: []string{" bad "}, MinApprovals: 1, TimeoutSeconds: 30},
	} {
		if err := ValidatePipelineApprovalPolicy(invalid); err == nil {
			t.Fatalf("invalid policy accepted: %+v", invalid)
		}
	}
}
