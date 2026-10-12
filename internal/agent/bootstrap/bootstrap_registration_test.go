package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/agent/config"
)

func TestBootstrapBlocksJournalEnrollmentBeforeWaitingForConnection(t *testing.T) {
	workspace := t.TempDir()
	directory := filepath.Join(workspace, ".terminal-report-outbox")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	identity := []byte("synthetic existing binding\n")
	if err := os.WriteFile(filepath.Join(directory, "identity"), identity, 0o640); err != nil {
		t.Fatal(err)
	}
	app := &Agent{AgentConf: &config.AgentConfig{
		Agent: config.AgentInfo{WorkspaceDir: workspace},
		Grpc:  config.GrpcConfig{ServerAddr: "synthetic-controller:9090", RegistrationToken: "art_synthetic"},
	}}
	started := time.Now()
	// 无连接时仍须立即拒绝，不能进入连接等待、注册或启动工作器
	app.waitForRegistrationAndStartHeartbeat()
	if time.Since(started) >= time.Second {
		t.Fatal("bootstrap waited for the connection before checking journal identity")
	}
	if app.TaskQueue != nil || app.Outbox != nil || app.AgentConf.Agent.ID != "" || app.AgentConf.Grpc.Token != "" {
		t.Fatal("blocked bootstrap initialized agent execution state")
	}
	actual, err := os.ReadFile(filepath.Join(directory, "identity"))
	if err != nil || string(actual) != string(identity) {
		t.Fatalf("blocked bootstrap changed the journal: %v", err)
	}
}
