package validation

import (
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"testing"
)

const schemaRuntimeImage = "registry.example/build@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func schemaRuntimeFixture() (*spec.Pipeline, taskqueue.OfflineRuntimePolicy) {
	return &spec.Pipeline{Namespace: "test", Runtime: &spec.Runtime{Type: taskqueue.RuntimeContainerdOfflineV1, Image: schemaRuntimeImage, Resources: &spec.Resources{CpuLimit: "500m", MemoryLimit: "64Mi"}}, Jobs: []*spec.Job{{Name: "build", Steps: []*spec.Step{{Name: "shell", Uses: "shell", Action: "run", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"run": "echo ok"})}}}}}, taskqueue.OfflineRuntimePolicy{Enabled: true, AllowedImages: []string{schemaRuntimeImage}, MaxCPU: "1", MaxMemory: "128Mi"}
}
func TestOfflineRuntimeSchemaIsExplicitAndDefaultDisabled(t *testing.T) {
	for name, change := range map[string]func(*spec.Pipeline){"two jobs": func(p *spec.Pipeline) { p.Jobs = append(p.Jobs, p.Jobs[0]) }, "host step": func(p *spec.Pipeline) { p.Jobs[0].Steps[0].RunOnAgent = false }, "deployment target": func(p *spec.Pipeline) { p.Jobs[0].Target = &spec.Target{} }, "requests": func(p *spec.Pipeline) { p.Runtime.Resources.CpuRequest = "1" }, "host paths": func(p *spec.Pipeline) { p.Runtime.Config = spec.MapAsStruct(map[string]any{"mounts": []any{}}) }, "network none unsupported": func(p *spec.Pipeline) { p.Runtime.Config = spec.MapAsStruct(map[string]any{"network": "none"}) }, "retry": func(p *spec.Pipeline) { p.Jobs[0].Retry = &spec.Retry{} }, "local source": func(p *spec.Pipeline) { p.Jobs[0].Source = &spec.Source{Type: "git", Repo: "/tmp/repo"} }, "script action": func(p *spec.Pipeline) { p.Jobs[0].Steps[0].Action = "script" }} {
		t.Run(name, func(t *testing.T) {
			p, policy := schemaRuntimeFixture()
			change(p)
			if err := NewSchemaValidatorWithRuntimePolicy(policy).Validate(p); err == nil {
				t.Fatal("unsupported runtime shape accepted")
			}
		})
	}
	p, policy := schemaRuntimeFixture()
	if err := NewSchemaValidator().Validate(p); err == nil {
		t.Fatal("default constructor enabled runtime")
	}
	validator := NewSchemaValidatorWithRuntimePolicy(policy)
	policy.AllowedImages[0] = "changed"
	if err := validator.Validate(p); err != nil {
		t.Fatal(err)
	}
	p, policy = schemaRuntimeFixture()
	request, err := ResolveRuntime(p, policy)
	if err != nil {
		t.Fatal(err)
	}
	p.Runtime.Resources.CpuLimit = "999"
	if request.Resources.CPU != "500m" {
		t.Fatal("runtime snapshot aliases protobuf")
	}
}
