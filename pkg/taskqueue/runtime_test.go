package taskqueue

import (
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/bytedance/sonic"
	"strings"
	"testing"
)

const runtimeTestImage = "registry.example/team/build@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func runtimeTestPolicy() OfflineRuntimePolicy {
	return OfflineRuntimePolicy{Enabled: true, AllowedImages: []string{runtimeTestImage}, MaxCPU: "2", MaxMemory: "1Gi"}
}
func runtimeTestJob(t *testing.T) *JobRunTaskPayload {
	t.Helper()
	request := &RuntimeRequest{Type: RuntimeContainerdOfflineV1, Image: runtimeTestImage, Resources: RuntimeResources{CPU: "500m", Memory: "64Mi"}}
	digest, err := RuntimeBindingDigest("pipeline", "run", "job", "agent", request)
	if err != nil {
		t.Fatal(err)
	}
	request.BindingDigest = digest
	return &JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", Runtime: request, Steps: []StepPayload{{Uses: "builtin:artifacts", Action: "download", Args: map[string]any{"paths": []any{"input/**"}}}, {Uses: "shell", Action: "run", Args: map[string]any{"run": "echo ok"}}, {Uses: "builtin:artifacts", Action: "upload", Args: map[string]any{"paths": []any{"out/**"}}}}}
}
func TestOfflineRuntimeContractRejectsUnsafeProfiles(t *testing.T) {
	for name, change := range map[string]func(*JobRunTaskPayload){
		"missing CPU": func(p *JobRunTaskPayload) { p.Runtime.Resources.CPU = "" }, "missing memory": func(p *JobRunTaskPayload) { p.Runtime.Resources.Memory = "" },
		"CPU maximum": func(p *JobRunTaskPayload) { p.Runtime.Resources.CPU = "2.01" }, "memory maximum": func(p *JobRunTaskPayload) { p.Runtime.Resources.Memory = "2Gi" },
		"CPU overflow": func(p *JobRunTaskPayload) { p.Runtime.Resources.CPU = strings.Repeat("9", 60) }, "memory overflow": func(p *JobRunTaskPayload) { p.Runtime.Resources.Memory = "999999999999999T" },
		"mutable image": func(p *JobRunTaskPayload) { p.Runtime.Image = "alpine:latest" }, "image not approved": func(p *JobRunTaskPayload) {
			p.Runtime.Image = strings.Replace(runtimeTestImage, "team/build", "team/other", 1)
		},
		"wrong version": func(p *JobRunTaskPayload) { p.Runtime.Type = "containerd-offline-v2" }, "host workspace": func(p *JobRunTaskPayload) { p.Workspace = "/etc" },
		"local checkout": func(p *JobRunTaskPayload) { p.Source = &SourcePayload{Type: "git", Repo: "/private/repo"} }, "file checkout": func(p *JobRunTaskPayload) { p.Source = &SourcePayload{Type: "git", Repo: "file:///private/repo"} },
		"HTTP step": func(p *JobRunTaskPayload) { p.Steps[1].Uses = "http" }, "native plugin": func(p *JobRunTaskPayload) { p.Steps[1].Uses = "native" }, "deployment": func(p *JobRunTaskPayload) { p.Steps[1].Uses = "builtin:kubernetes" },
		"shell command action": func(p *JobRunTaskPayload) { p.Steps[1].Action = "command" }, "shell flags": func(p *JobRunTaskPayload) { p.Steps[1].Args["privileged"] = false },
		"download after shell": func(p *JobRunTaskPayload) { p.Steps[2].Action = "download" }, "shell after upload": func(p *JobRunTaskPayload) { p.Steps = append(p.Steps, StepPayload{Uses: "shell"}) },
		"upload before shell": func(p *JobRunTaskPayload) { p.Steps[0].Action = "upload" }, "no shell": func(p *JobRunTaskPayload) { p.Steps = p.Steps[:1] },
		"artifact flags": func(p *JobRunTaskPayload) { p.Steps[2].Args["public"] = false }, "artifact host path": func(p *JobRunTaskPayload) { p.Steps[2].Args["path"] = "/etc/passwd" }, "artifact escape": func(p *JobRunTaskPayload) { p.Steps[2].Args["path"] = "../secret" },
		"retry": func(p *JobRunTaskPayload) { p.Retry = &pipelinev1.Retry{} }, "retry attempts": func(p *JobRunTaskPayload) { p.Steps[1].AttemptRunIDs = []string{"a", "b"} },
		"missing binding": func(p *JobRunTaskPayload) { p.Runtime.BindingDigest = "" }, "foreign agent": func(p *JobRunTaskPayload) { p.AgentID = "other" }, "foreign pipeline": func(p *JobRunTaskPayload) { p.PipelineID = "other" }, "foreign run": func(p *JobRunTaskPayload) { p.PipelineRunID = "other" }, "foreign job": func(p *JobRunTaskPayload) { p.JobRunID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			payload := runtimeTestJob(t)
			change(payload)
			if err := ValidateJobRuntime(payload, runtimeTestPolicy()); err == nil {
				t.Fatal("unsafe runtime accepted")
			}
		})
	}
	payload := runtimeTestJob(t)
	if err := ValidateJobRuntime(payload, runtimeTestPolicy()); err != nil {
		t.Fatal(err)
	}
	if err := ValidateJobRuntime(payload, OfflineRuntimePolicy{}); err == nil {
		t.Fatal("default enabled runtime")
	}
	if err := runtimeTestPolicy().ValidateProduction(); err == nil {
		t.Fatal("unaccepted runtime enabled in production")
	}
	if err := ValidateStandaloneRuntime(&StepRunTaskPayload{Runtime: payload.Runtime}); err == nil {
		t.Fatal("standalone runtime accepted")
	}
	if err := ValidateJobRuntime(&JobRunTaskPayload{Workspace: "legacy-host-path"}, OfflineRuntimePolicy{}); err != nil {
		t.Fatalf("legacy payload changed: %v", err)
	}
}
func TestRuntimeStrictJSONAndBindingOwnership(t *testing.T) {
	for _, raw := range []string{`{"type":"containerd-offline-v1","privileged":false}`, `{"type":"containerd-offline-v1","resources":{"cpu":"1","memory":"64Mi","cpuShares":10}}`} {
		var request RuntimeRequest
		if err := sonic.UnmarshalString(raw, &request); err == nil {
			t.Fatal("unknown runtime options ignored")
		}
	}
	payload := runtimeTestJob(t)
	encoded, err := sonic.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded JobRunTaskPayload
	if err := sonic.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := ValidateJobRuntime(&decoded, runtimeTestPolicy()); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*RuntimeRequest){func(r *RuntimeRequest) { r.Image = strings.Replace(runtimeTestImage, "team/build", "other/build", 1) }, func(r *RuntimeRequest) { r.Resources.CPU = "1" }, func(r *RuntimeRequest) { r.Resources.Memory = "65Mi" }} {
		changed := *payload.Runtime
		mutate(&changed)
		digest, err := RuntimeBindingDigest(payload.PipelineID, payload.PipelineRunID, payload.JobRunID, payload.AgentID, &changed)
		if err != nil || digest == payload.Runtime.BindingDigest {
			t.Fatal("runtime data not bound")
		}
	}
	policy := runtimeTestPolicy()
	copy := policy.Clone()
	policy.AllowedImages[0] = "changed"
	if copy.AllowedImages[0] != runtimeTestImage {
		t.Fatal("policy aliases caller slice")
	}
}
