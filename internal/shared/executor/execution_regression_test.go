package executor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/logstream"
	"github.com/arcentrix/arcentra/pkg/safe"
)

func TestShellExecutionEnvironmentAndStreams(t *testing.T) {
	t.Setenv("ARCENTRA_PARENT_VALUE", "inherited")
	req := &ExecutionRequest{Step: &StepInfo{Args: map[string]any{"commands": []string{`printf '%s:%s' "$ARCENTRA_PARENT_VALUE" "$CUSTOM_VALUE"`, `printf 'separate-error' >&2`}}}, Env: map[string]string{"CUSTOM_VALUE": "child"}, Workspace: t.TempDir()}
	result, err := NewShellExecutor().Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "inherited:child" || result.ErrorOutput != "separate-error" {
		t.Fatalf("unexpected streams: %+v", result)
	}
	req.Step.Args["commands"] = []string{"false", "echo not-reached"}
	result, err = NewShellExecutor().Execute(context.Background(), req)
	if err == nil || result.Success || strings.Contains(result.Output, "not-reached") {
		t.Fatalf("failed command was ignored: %+v %v", result, err)
	}
}

func TestShellTimeoutKillsChildProcessGroup(t *testing.T) {
	start := time.Now()
	result, err := NewShellExecutor().Execute(context.Background(), &ExecutionRequest{Step: &StepInfo{Args: map[string]any{"run": "sleep 30 & wait"}}, Options: &ExecutionOptions{Timeout: 50 * time.Millisecond}})
	if !errors.Is(err, context.DeadlineExceeded) || result.Success {
		t.Fatalf("wrong timeout: %+v %v", result, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("child process kept shell pipes open after timeout")
	}
}

func TestManagerRejectsFailedAndMissingResults(t *testing.T) {
	for _, result := range []*ExecutionResult{nil, {Success: false, Error: "declined", ExitCode: 7}} {
		manager := NewExecutorManager()
		manager.Register(&mockExecutor{name: "test", canExecute: func(*ExecutionRequest) bool { return true }, executeFunc: func(context.Context, *ExecutionRequest) (*ExecutionResult, error) { return result, nil }})
		if _, err := manager.Execute(context.Background(), &ExecutionRequest{Step: &StepInfo{Name: "test"}}); err == nil {
			t.Fatal("unsuccessful result was accepted")
		}
	}
	if _, err := NewExecutorManager().Execute(context.Background(), nil); err == nil {
		t.Fatal("nil request accepted")
	}
}

func TestPluginFailureResultIsPreserved(t *testing.T) {
	for _, raw := range []string{`{"success":false,"error":"failed"}`, `{"exit_code":9}`, `{"success":true,"exitCode":3}`, `not json`} {
		result := NewExecutionResult("plugin")
		if err := decodePluginResult(result, []byte(raw)); err == nil || result.Success {
			t.Errorf("plugin failure became success: %s %+v", raw, result)
		}
	}
	result := NewExecutionResult("plugin")
	if err := decodePluginResult(result, []byte(`{"stdout":"ok"}`)); err != nil || !result.Success {
		t.Fatalf("legacy success result rejected: %+v %v", result, err)
	}
}

type liveLogPublisher struct {
	mu      sync.Mutex
	entries []*logstream.BuildLogMessage
	ready   chan struct{}
	once    sync.Once
}

func (p *liveLogPublisher) Publish(_ context.Context, entry *logstream.BuildLogMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries = append(p.entries, entry)
	p.once.Do(func() { close(p.ready) })
	return nil
}

func TestShellLogsArePublishedBeforeCompletion(t *testing.T) {
	publisher := &liveLogPublisher{ready: make(chan struct{})}
	manager := NewExecutorManager()
	manager.Register(NewShellExecutor())
	manager.SetLogPublisher(publisher)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	safe.Go(func() {
		_, err := manager.Execute(ctx, &ExecutionRequest{StepRunID: "step-run-1", AgentID: "agent-1", Step: &StepInfo{Name: "build", Args: map[string]any{"run": "printf 'live'; sleep 30 & wait"}}, Pipeline: &PipelineInfo{Namespace: "pipeline"}})
		done <- err
	})
	select {
	case <-publisher.ready:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("log was buffered until completion")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wrong cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("execution did not stop")
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if len(publisher.entries) != 1 || publisher.entries[0].StepRunID != "step-run-1" || publisher.entries[0].AgentID != "agent-1" {
		t.Fatalf("invalid or duplicate logs: %+v", publisher.entries)
	}
}

func TestBoundedOutput(t *testing.T) {
	var output boundedOutput
	count, err := output.Write([]byte(strings.Repeat("x", maxCapturedOutput+20)))
	if err != nil || count != maxCapturedOutput+20 || output.Len() != maxCapturedOutput || output.total != int64(maxCapturedOutput+20) {
		t.Fatalf("incorrect output limit: %d %d %d %v", count, output.Len(), output.total, err)
	}
}

func TestExpectedHTTPStatusIsExact(t *testing.T) {
	executor := &HTTPExecutor{}
	if executor.checkExpectedStatus(201, []int32{200}) {
		t.Fatal("unexpected 2xx response accepted")
	}
	if !executor.checkExpectedStatus(201, nil) {
		t.Fatal("default 2xx response rejected")
	}
}
