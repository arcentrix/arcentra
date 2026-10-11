package router

import (
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/bytedance/sonic"
)

func TestPipelineSpecHTTPRoundTripCamelCase(t *testing.T) {
	original := &pipelinev1.Spec{Namespace: "test", Jobs: []*pipelinev1.Job{{Name: "build", DependsOn: []string{"prepare"}, Steps: []*pipelinev1.Step{{Name: "shell", Uses: "shell", RunOnAgent: true, ContinueOnError: true, Args: spec.MapAsStruct(map[string]any{"script": "echo real"}), AgentSelector: &pipelinev1.AgentSelector{MatchLabels: map[string]string{"os": "linux"}}}}}}}
	value := pipelineProtoValue(original).(map[string]any)
	raw, err := sonic.MarshalString(value)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseHTTPPipelineSpec(value)
	if err != nil {
		t.Fatal(err)
	}
	step := parsed.Jobs[0].Steps[0]
	if !step.RunOnAgent || !step.ContinueOnError || step.AgentSelector.MatchLabels["os"] != "linux" || parsed.Jobs[0].DependsOn[0] != "prepare" {
		t.Fatalf("HTTP JSON lost proto fields: %s", raw)
	}
	if _, err := parseHTTPPipelineSpec(map[string]any{"namespace": "test", "madeUpField": true}); err == nil {
		t.Fatal("unknown spec field silently accepted")
	}
}

func TestPipelineProtoResponseEnumsAreNumeric(t *testing.T) {
	value := pipelineProtoValue(&pipelinev1.PipelineRunDetail{RunId: "run", Status: pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING}).(map[string]any)
	if value["runId"] != "run" || value["status"] != float64(2) {
		t.Fatalf("unexpected response: %+v", value)
	}
}
