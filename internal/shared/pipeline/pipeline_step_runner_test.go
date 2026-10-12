package pipeline

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/logstream"
	"go.uber.org/zap"
)

type localShellLogCapture struct {
	mu     sync.Mutex
	output strings.Builder
}

func (p *localShellLogCapture) Publish(_ context.Context, entry *logstream.BuildLogMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.output.WriteString(entry.Content)
	return nil
}

func TestLocalShellRedactsSecretsAcrossOutputChunks(t *testing.T) {
	execution := NewExecutionContext(&spec.Pipeline{Namespace: "test"}, nil, t.TempDir(), log.Logger{SugaredLogger: zap.NewNop().Sugar()})
	execution.Env["secrets.ACCESS_TOKEN"] = "split-sensitive-token"
	publisher := &localShellLogCapture{}
	execution.LogPublisher = publisher
	step := &spec.Step{Name: "shell", Uses: "shell", Args: spec.MapAsStruct(map[string]any{"script": "printf 'prefix split-'; sleep 0.05; printf 'sensitive-token suffix\\n'"})}
	if err := NewStepRunner(execution, &spec.Job{Name: "build", Steps: []*spec.Step{step}}, step).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	output := publisher.output.String()
	if strings.Contains(output, "split-sensitive-token") || strings.Contains(output, "sensitive-token") || !strings.Contains(output, "***") || !strings.Contains(output, "prefix") || !strings.Contains(output, "suffix") {
		t.Fatalf("unsafe or incomplete local shell log: %q", output)
	}
}
