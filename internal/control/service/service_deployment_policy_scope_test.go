package service

import (
	"context"
	"testing"

	"github.com/bytedance/sonic"
)

func TestDeploymentEnvironmentPolicyRequiresCompleteTargetBinding(t *testing.T) {
	for _, name := range []string{"valid", "foreign-project", "wrong-namespace", "wrong-deployment", "wrong-container", "unknown-ref", "nil-adapter"} {
		t.Run(name, func(t *testing.T) {
			s, _, _, adapter, _ := deploymentApprovalFixture(t)
			binding := s.targets["staging"]
			binding.Target.Namespace = "policy-view"
			s.targets["policy-view"] = binding
			project := binding.ProjectID
			input := DeploymentEnvironmentInput{Name: "Policy boundary", TargetRef: "policy-view", Namespace: binding.Target.Namespace, Deployment: binding.Target.Deployment, Container: binding.Target.Container}
			switch name {
			case "foreign-project":
				project = "other-project"
			case "wrong-namespace":
				input.Namespace = "other-namespace"
			case "wrong-deployment":
				input.Deployment = "other-workload"
			case "wrong-container":
				input.Container = "other-container"
			case "unknown-ref":
				input.TargetRef = "missing-ref"
			case "nil-adapter":
				binding.Adapter = nil
				s.targets["policy-view"] = binding
			}
			created, err := s.CreateEnvironment(context.Background(), project, input)
			if err != nil {
				t.Fatal(err)
			}
			check := func(where string, view *DeploymentEnvironmentView) {
				t.Helper()
				if name == "valid" {
					if !view.Configured || view.PipelineApproval == nil || view.PipelineApproval.MinApprovals != 1 || view.PipelineApproval.TimeoutSeconds != 30 || len(view.PipelineApproval.Approvers) != 2 {
						t.Fatalf("%s lost authorized policy: %+v", where, view)
					}
					view.PipelineApproval.Approvers[0] = "response-mutation"
					if s.targets["policy-view"].PipelineApproval.Approvers[0] == "response-mutation" {
						t.Fatal("view aliases operator approval policy")
					}
				} else {
					if view.Configured || view.AllowPipelineDeployments || view.Tier != "" || view.PipelineApproval != nil {
						t.Fatalf("%s exposed unbound operator policy: %+v", where, view)
					}
					raw, err := sonic.Marshal(view)
					if err != nil {
						t.Fatal(err)
					}
					var public map[string]any
					if err := sonic.Unmarshal(raw, &public); err != nil {
						t.Fatal(err)
					}
					if _, present := public["pipelineApproval"]; present {
						t.Fatalf("%s serialized unbound policy", where)
					}
				}
			}
			check("create", created)
			single, err := s.Environment(context.Background(), project, created.EnvironmentID)
			if err != nil {
				t.Fatal(err)
			}
			check("single", single)
			page, err := s.Environments(context.Background(), project, 1, 100)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for i := range page.List {
				if page.List[i].EnvironmentID == created.EnvironmentID {
					found = true
					check("list", &page.List[i])
				}
			}
			if !found {
				t.Fatal("created environment missing from project list")
			}
			if adapter.inspect+adapter.apply+adapter.observe != 0 {
				t.Fatal("configuration reads contacted Kubernetes")
			}
		})
	}
}
