package outbox

import (
	"errors"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"os"
	"path/filepath"
	"testing"

	"github.com/arcentrix/arcentra/internal/shared/executor"
)

func TestAgentManagerHasNoGenericPluginOrHTTPFallback(t *testing.T) {
	manager := ProvideExecutorManager(nil)
	for _, step := range []*executor.StepInfo{
		{Uses: "unknown-plugin"},
		{Uses: "unknown-plugin", Args: map[string]any{"url": "https://example.invalid"}},
		{Uses: "http", Args: map[string]any{"url": "https://example.invalid"}},
	} {
		if _, err := manager.SelectExecutor(&executor.ExecutionRequest{Step: step}); err == nil {
			t.Fatalf("generic fallback remains for %+v", step)
		}
	}
	if _, err := manager.SelectExecutor(&executor.ExecutionRequest{Step: &executor.StepInfo{Uses: "shell", Args: map[string]any{"run": "true"}}}); err != nil {
		t.Fatalf("supported shell removed: %v", err)
	}
}

func TestOutboxProviderDefersUntilAuthenticatedStartup(t *testing.T) {
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "synthetic-agent", WorkspaceDir: t.TempDir()}, Grpc: config.GrpcConfig{Token: "synthetic-agent:signature"}}
	ob, err := ProvideOutbox(conf, nil)
	if err != nil || ob != nil {
		t.Fatal("provider started publisher before authentication", err)
	}
	if _, err := os.Lstat(filepath.Join(conf.Agent.WorkspaceDir, "data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("provider touched the event journal before authentication")
	}
}
