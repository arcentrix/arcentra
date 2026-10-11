package taskqueue

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/agent/config"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
)

func TestAgentExecutionModeFailsClosed(t *testing.T) {
	for _, conf := range []*config.AgentConfig{
		{}, {Agent: config.AgentInfo{Mode: "sandbox"}}, {Agent: config.AgentInfo{Mode: "baremetal", Sandbox: config.SandboxConfig{Enable: true}}}, {Agent: config.AgentInfo{Mode: "baremetal", DeniedCommands: []string{"rm"}}}, {Agent: config.AgentInfo{Mode: "baremetal", ResourceLimits: map[string]string{"cpu": "1"}}},
	} {
		if err := ValidateExecutionConfig(conf); err == nil {
			t.Fatalf("unsupported configuration accepted: %+v", conf.Agent)
		}
	}
	if err := ValidateExecutionConfig(&config.AgentConfig{Agent: config.AgentInfo{Mode: "baremetal"}}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspacePreparationAndArtifactFailure(t *testing.T) {
	conf := &config.AgentConfig{Agent: config.AgentInfo{WorkspaceDir: t.TempDir()}}
	if _, err := prepareWorkspace(conf, &taskpayload.JobRunTaskPayload{PipelineRunID: "../escape", JobRunID: "job"}); err == nil {
		t.Fatal("unsafe run identifier accepted")
	}
	workspace, err := prepareWorkspace(conf, &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: "job"})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(workspace) {
		t.Fatal("workspace must be absolute")
	}
	if err := handleArtifactDownload(context.Background(), map[string]string{"build/output": "object"}, workspace, nil); err == nil {
		t.Fatal("missing artifact storage ignored")
	}
}

func TestSourceCloneRelativeWorkspaceAndPinnedCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = source
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		bytes, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, bytes, err)
		}
		return strings.TrimSpace(string(bytes))
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(source, "hello.txt"), []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "hello.txt")
	run("commit", "-m", "first")
	commit := run("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(source, "hello.txt"), []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "hello.txt")
	run("commit", "-m", "second")
	workspace := filepath.Join(root, "checkout")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeWorkspace, err := filepath.Rel(cwd, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := handleSourceClone(context.Background(), &taskpayload.SourcePayload{Type: "git", Repo: source, Branch: "main", Commit: commit}, relativeWorkspace); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(filepath.Join(workspace, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes) != "first" {
		t.Fatalf("commit pin ignored: %s", bytes)
	}
}

func TestQueuedCancellationPreventsExecution(t *testing.T) {
	jobID := "queued-cancellation-test"
	conf := &config.AgentConfig{Agent: config.AgentInfo{WorkspaceDir: t.TempDir()}}
	CancelJobRun(jobID)
	err := executeJobRun(context.Background(), conf, nil, nil, &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: jobID, Steps: []taskpayload.StepPayload{{Name: "should-not-run", StepRunID: "queued-step", Args: map[string]any{"run": "touch executed"}}}})
	if err == nil {
		t.Fatal("queued cancellation was ignored")
	}
	if _, err := os.Stat(filepath.Join(conf.Agent.WorkspaceDir, "run", jobID, "executed")); !os.IsNotExist(err) {
		t.Fatal("cancelled queued job executed")
	}
}

func TestGitSourceRejectsEmbeddedURLCredentials(t *testing.T) {
	err := handleSourceClone(context.Background(), &taskpayload.SourcePayload{Type: "git", Repo: "https://user:secret-password@example.invalid/repo.git"}, t.TempDir())
	if err == nil || strings.Contains(err.Error(), "secret-password") || !strings.Contains(err.Error(), "source.auth") {
		t.Fatalf("credential-bearing URL was not rejected safely: %v", err)
	}
}
