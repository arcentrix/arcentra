package taskqueue

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/arcentrix/arcentra/internal/agent/config"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
)

func TestStartupRejectsUnsupportedCapacity(t *testing.T) {
	for _, capacity := range []int{-1, 2, 5} {
		if err := ValidateExecutionConfig(&config.AgentConfig{Agent: config.AgentInfo{Mode: "baremetal", MaxConcurrentJobs: capacity}}); err == nil || !strings.Contains(err.Error(), "one job at a time") {
			t.Fatalf("capacity %d accepted: %v", capacity, err)
		}
	}
	for _, capacity := range []int{0, 1} {
		if err := ValidateExecutionConfig(&config.AgentConfig{Agent: config.AgentInfo{Mode: "baremetal", MaxConcurrentJobs: capacity}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnknownAgentPluginFailsBeforeWorkspaceAndReportsTerminalState(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		name := "whole-job"
		if standalone {
			name = "standalone"
		}
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "never-created")
			client := &artifactStatusClient{}
			wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
			conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: root}}
			args := map[string]any{"run": "touch forbidden", "url": "https://example.invalid"}
			var err error
			if standalone {
				err = executeStepRunViaExecutor(context.Background(), conf, wrapper, nil, &taskpayload.StepRunTaskPayload{PipelineRunID: "cap-run", JobID: name, StepRunID: "cap-" + name, Uses: "native-plugin", Args: args})
				if len(client.stepReports) != 2 || int(client.stepReports[1].Status) != 5 {
					t.Fatalf("missing terminal step failure: %+v", client.stepReports)
				}
			} else {
				err = executeJobRun(context.Background(), conf, wrapper, nil, &taskpayload.JobRunTaskPayload{PipelineRunID: "cap-run", JobRunID: name, Source: &taskpayload.SourcePayload{Type: "unsupported-must-not-reach"}, Steps: []taskpayload.StepPayload{{Name: "native", StepRunID: "cap-" + name, Uses: "native-plugin", Args: args}}})
				if len(client.jobReports) != 2 || client.jobReports[1].Status != 5 {
					t.Fatalf("missing terminal job failure: %+v", client.jobReports)
				}
			}
			if err == nil || !strings.Contains(err.Error(), "native plugins are not available") || acknowledgeCompleted(err) != nil {
				t.Fatalf("unsupported plugin not clearly completed: %v", err)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("unsupported plugin prepared host workspace: %v", err)
			}
		})
	}
}

func TestExplicitHTTPDoesNotFallThroughToShell(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	workspace := t.TempDir()
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: workspace}}
	payload := &taskpayload.StepRunTaskPayload{StepRunID: "cap-http", Uses: "http", Action: "request", Workspace: workspace, Args: map[string]any{"url": server.URL, "run": "touch forbidden"}}
	if err := executeStepRunViaExecutor(context.Background(), conf, nil, nil, payload); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("expected one HTTP request, got %d", requests.Load())
	}
	if _, err := os.Stat(filepath.Join(workspace, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("HTTP step executed shell argument")
	}
}

func TestAgentStepArgumentValidation(t *testing.T) {
	for _, args := range []map[string]any{{}, {"url": "file:///tmp/secret"}, {"url": "https://user:secret@example.invalid"}, {"url": "/relative"}} {
		if err := validateStepExecution("http", "request", args, true); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid HTTP URL accepted or exposed: %v", err)
		}
	}
	if err := validateStepExecution("shell", "run", map[string]any{"url": "https://example.invalid"}, true); err == nil {
		t.Fatal("shell without command accepted")
	}
	if err := validateStepExecution("builtin:artifacts", "upload", nil, false); err == nil {
		t.Fatal("standalone artifact step accepted without job")
	}
	if err := validateStepExecution("", "", map[string]any{"run": "true"}, false); err != nil {
		t.Fatalf("legacy implicit shell rejected: %v", err)
	}
}
