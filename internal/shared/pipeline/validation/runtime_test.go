package validation

import (
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"testing"
)

func TestUnsupportedRuntimeCannotSilentlyExecuteOnHost(t *testing.T) {
	for _, runtime := range []*spec.Runtime{{Type: "docker"}, {Type: "container"}, {Type: "kubernetes"}, {Image: "alpine:latest"}, {Resources: &spec.Resources{CpuLimit: "1"}}, {Config: spec.MapAsStruct(map[string]any{"privileged": false})}} {
		definition := &spec.Pipeline{Namespace: "test", Runtime: runtime, Jobs: []*spec.Job{{Name: "build", Steps: []*spec.Step{{Name: "shell", Uses: "shell"}}}}}
		if err := NewSchemaValidator().Validate(definition); err == nil {
			t.Fatalf("unsupported runtime accepted: %+v", runtime)
		}
	}
	definition := &spec.Pipeline{Namespace: "test", Runtime: &spec.Runtime{Type: "baremetal", Env: map[string]string{"APP_ENV": "test"}}, Jobs: []*spec.Job{{Name: "build", Steps: []*spec.Step{{Name: "shell", Uses: "shell"}}}}}
	if err := NewSchemaValidator().Validate(definition); err != nil {
		t.Fatalf("supported runtime environment rejected: %v", err)
	}
}
