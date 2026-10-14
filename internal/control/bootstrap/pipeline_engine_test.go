package bootstrap

import (
	"strings"
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/process"
	"github.com/arcentrix/arcentra/internal/control/router"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
)

type alreadyBoundPipelineEngine struct{ service.IPipelineEngine }

func TestNewAppStopsNewEngineWhenStartupBindingFails(t *testing.T) {
	services := &service.Services{}
	if err := services.BindPipelineEngine(&alreadyBoundPipelineEngine{}); err != nil {
		t.Fatal(err)
	}
	engine := process.NewProcess(nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(engine.Stop)
	// No HTTP configuration exists: reaching router/listener setup would fail this test.
	app, cleanup, err := NewApp(&router.Router{Services: services}, nil, nil, nil, nil, nil, nil, nil, nil, nil, engine)
	if err == nil || !strings.Contains(err.Error(), "already bound") || app != nil || cleanup != nil {
		t.Fatalf("startup continued after engine replacement: %v %v", app, err)
	}
	parsed := &pipelinev1.Spec{Namespace: "test", Jobs: []*pipelinev1.Job{{Name: "build", Steps: []*pipelinev1.Step{{Name: "run", Uses: "shell", Action: "run", Args: spec.MapAsStruct(map[string]any{"run": "echo unreachable"})}}}}}
	if err := engine.Submit(&model.PipelineRun{RunID: "never-started"}, parsed); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("failed startup left new engine running: %v", err)
	}
}
