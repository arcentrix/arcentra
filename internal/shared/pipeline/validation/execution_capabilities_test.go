package validation

import (
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
)

func capabilityPipeline(step *spec.Step) *spec.Pipeline {
	return &spec.Pipeline{Namespace: "test", Jobs: []*spec.Job{{Name: "build", Steps: []*spec.Step{step}}}}
}
func TestNamedConcurrencyFailsClearlyInProductionValidation(t *testing.T) {
	for _, key := range []string{"production-deploy", " "} {
		p := capabilityPipeline(&spec.Step{Name: "run", Uses: "shell"})
		p.Jobs[0].Concurrency = key
		if err := NewSchemaValidator().Validate(p); err == nil || !strings.Contains(err.Error(), "named concurrency is unsupported") {
			t.Fatalf("concurrency %q: %v", key, err)
		}
	}
}
func TestAgentStepsRejectNativePluginsEvenWithFallbackArguments(t *testing.T) {
	for _, uses := range []string{"native-plugin", "native-plugin@1.0.0", "shell@1.0.0", "builtin:reports", "artifacts"} {
		p := capabilityPipeline(&spec.Step{Name: "native", Uses: uses, RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"run": "touch forbidden", "url": "https://example.invalid"})})
		if err := NewSchemaValidator().Validate(p); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("native uses %q accepted: %v", uses, err)
		}
	}
	p := capabilityPipeline(&spec.Step{Name: "remote", Uses: "shell", RunOnAgent: true})
	p.Jobs[0].Steps = append(p.Jobs[0].Steps, &spec.Step{Name: "implicitly-remote", Uses: "native-plugin"})
	if err := NewSchemaValidator().Validate(p); err == nil {
		t.Fatal("native step in whole Agent job accepted")
	}
}
func TestSupportedAgentStepNamesAndActionsRemainValid(t *testing.T) {
	for _, entry := range []struct{ uses, action string }{
		{"shell", ""}, {"shell", "run"}, {"builtin:shell", "script"}, {"builtin:shell", "command"},
		{"builtin:artifacts", "upload"}, {"builtin:artifacts", "download"}, {"http", ""}, {"http", "request"},
	} {
		p := capabilityPipeline(&spec.Step{Name: "step", Uses: entry.uses, Action: entry.action, RunOnAgent: true})
		if err := NewSchemaValidator().Validate(p); err != nil {
			t.Fatalf("%+v rejected: %v", entry, err)
		}
	}
	for _, entry := range []struct{ uses, action string }{{"shell", "deploy"}, {"builtin:artifacts", ""}, {"http", "deploy"}} {
		if err := NewSchemaValidator().Validate(capabilityPipeline(&spec.Step{Name: "step", Uses: entry.uses, Action: entry.action, RunOnAgent: true})); err == nil {
			t.Fatalf("unsupported action accepted: %+v", entry)
		}
	}
	if err := NewSchemaValidator().Validate(capabilityPipeline(&spec.Step{Name: "local", Uses: "native-plugin@1.0.0"})); err != nil {
		t.Fatalf("Agent restriction changed local plugin validation: %v", err)
	}
}
