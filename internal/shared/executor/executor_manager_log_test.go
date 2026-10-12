package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/logstream"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/arcentrix/arcentra/pkg/safe"
)

type managerLogPublisherFunc func(context.Context, *logstream.BuildLogMessage) error

func (f managerLogPublisherFunc) Publish(ctx context.Context, entry *logstream.BuildLogMessage) error {
	return f(ctx, entry)
}

type managerEventPublisherFunc func(context.Context, map[string]any) error

func (f managerEventPublisherFunc) Publish(ctx context.Context, event map[string]any) error {
	return f(ctx, event)
}

func (f managerEventPublisherFunc) Close() error { return nil }

func TestManagerLogRejectionCancelsShell(t *testing.T) {
	spoolErr := errors.New("spool rejected append")
	manager := NewExecutorManager()
	manager.Register(NewShellExecutor())
	calls := 0
	manager.SetLogPublisher(managerLogPublisherFunc(func(_ context.Context, entry *logstream.BuildLogMessage) error {
		calls++
		if entry.Content != "command-output" {
			t.Errorf("unexpected output: %q", entry.Content)
		}
		return spoolErr
	}))
	var events []string
	manager.SetEventPublisher(managerEventPublisherFunc(func(_ context.Context, event map[string]any) error {
		events = append(events, event["type"].(string))
		return nil
	}), EventEmitterConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	started := time.Now()
	result, err := manager.Execute(ctx, &ExecutionRequest{
		Step: &StepInfo{Name: "reject-logs", Args: map[string]any{"run": "printf 'command-output'; sleep 30 & wait"}},
		OutputHandler: func(_, _ string) {
			t.Error("rejected log reached the output handler")
		},
	})
	if !errors.Is(err, spoolErr) || !errors.Is(err, ErrBuildLogPersistence) || !errors.Is(err, context.Canceled) {
		t.Fatalf("log rejection or cancellation identity was lost: %v", err)
	}
	if result == nil || result.Success || result.ExitCode == 0 || !strings.Contains(result.Error, ErrBuildLogPersistence.Error()) {
		t.Fatalf("rejected logs were not reflected in the result: %+v", result)
	}
	if strings.Contains(err.Error(), "command-output") {
		t.Fatalf("raw output was included in the error: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("shell did not stop promptly after log rejection: %v", elapsed)
	}
	if calls != 1 {
		t.Fatalf("log was retried or duplicated after rejection: %d", calls)
	}
	for _, eventType := range events {
		if eventType == plugin.EventTypeTaskSucceeded || eventType == plugin.EventTypeTaskLog {
			t.Fatalf("rejected output emitted an accepted-log or success event: %v", events)
		}
	}
	if len(events) != 3 || events[1] != plugin.EventTypeTaskFailed || events[2] != plugin.EventTypeTaskFinished {
		t.Fatalf("missing terminal failure events: %v", events)
	}
}

func TestManagerBufferedLogsPersistBeforeSuccess(t *testing.T) {
	manager := NewExecutorManager()
	var order []string
	manager.SetLogPublisher(managerLogPublisherFunc(func(_ context.Context, entry *logstream.BuildLogMessage) error {
		order = append(order, "persist:"+entry.Stream+":"+entry.Content)
		return nil
	}))
	manager.SetEventPublisher(managerEventPublisherFunc(func(_ context.Context, event map[string]any) error {
		order = append(order, event["type"].(string))
		return nil
	}), EventEmitterConfig{})
	result, err := manager.ExecuteWithExecutor(context.Background(), &ExecutionRequest{Step: &StepInfo{Name: "buffered"}}, &mockExecutor{
		name: "buffered",
		executeFunc: func(context.Context, *ExecutionRequest) (*ExecutionResult, error) {
			return &ExecutionResult{Success: true, Output: "out", ErrorOutput: "err"}, nil
		},
	})
	if err != nil || !result.Success {
		t.Fatalf("buffered result failed: %+v %v", result, err)
	}
	want := []string{plugin.EventTypeTaskStarted, "persist:stdout:out", plugin.EventTypeTaskLog, "persist:stderr:err", plugin.EventTypeTaskLog, plugin.EventTypeTaskSucceeded, plugin.EventTypeTaskFinished}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("buffered logs were not persisted before success: %v, want %v", order, want)
	}
}

func TestManagerBufferedLogRejectionPreservesExecutionFailure(t *testing.T) {
	secret := "private-123"
	spoolErr := errors.New("disk error " + secret)
	executionErr := errors.New("command failed " + secret)
	for _, test := range []struct {
		name   string
		result *ExecutionResult
		err    error
	}{
		{"successful-command", &ExecutionResult{Success: true, Output: secret}, nil},
		{"failed-command", &ExecutionResult{Output: secret, ExitCode: 23, Error: executionErr.Error()}, executionErr},
		{"failed-result", &ExecutionResult{Output: secret, ExitCode: 24, Error: executionErr.Error()}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := NewExecutorManager()
			manager.SetLogPublisher(managerLogPublisherFunc(func(_ context.Context, entry *logstream.BuildLogMessage) error {
				if entry.Content != "***" {
					t.Errorf("unredacted buffered log: %q", entry.Content)
				}
				return spoolErr
			}))
			manager.SetEventPublisher(managerEventPublisherFunc(func(_ context.Context, event map[string]any) error {
				if event["type"] == plugin.EventTypeTaskSucceeded {
					t.Error("success event emitted despite log rejection")
				}
				if strings.Contains(fmt.Sprint(event), secret) {
					t.Error("secret leaked in event")
				}
				return nil
			}), EventEmitterConfig{})
			result, err := manager.ExecuteWithExecutor(context.Background(), &ExecutionRequest{
				Step: &StepInfo{Name: "buffered"}, Env: map[string]string{"secrets.token": secret},
			}, &mockExecutor{name: "buffered", executeFunc: func(context.Context, *ExecutionRequest) (*ExecutionResult, error) {
				return test.result, test.err
			}})
			if !errors.Is(err, spoolErr) || (test.err != nil && !errors.Is(err, test.err)) {
				t.Fatalf("error identity was lost: %v", err)
			}
			if result.Success || result.ExitCode == 0 || result.Error != err.Error() {
				t.Fatalf("result did not reflect rejection: %+v %v", result, err)
			}
			if test.name != "successful-command" && !strings.Contains(err.Error(), "command failed ***") {
				t.Fatalf("original failure was discarded: %v", err)
			}
			if strings.Contains(err.Error()+result.Output+result.Error, secret) {
				t.Fatal("secret leaked from execution or publisher error")
			}
		})
	}
}

func TestManagerStreamedLogsAreNotDuplicatedWithoutMetadata(t *testing.T) {
	manager := NewExecutorManager()
	var output strings.Builder
	manager.SetLogPublisher(managerLogPublisherFunc(func(_ context.Context, entry *logstream.BuildLogMessage) error {
		output.WriteString(entry.Content)
		return nil
	}))
	result, err := manager.ExecuteWithExecutor(context.Background(), &ExecutionRequest{
		Step: &StepInfo{Name: "streamed"}, Env: map[string]string{"secrets.token": "private-123"},
	}, &mockExecutor{name: "streamed", executeFunc: func(_ context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
		req.OutputHandler("stdout", "before private-")
		req.OutputHandler("stdout", "123 after")
		return &ExecutionResult{Success: true, Output: "before private-123 after"}, nil
	}})
	if err != nil || !result.Success {
		t.Fatalf("streamed result failed: %+v %v", result, err)
	}
	if got := output.String(); got != "before *** after" {
		t.Fatalf("streamed log was duplicated or incorrectly redacted: %q", got)
	}
}

func TestManagerRedactorTailRejectionFailsSuccessfulCommand(t *testing.T) {
	manager := NewExecutorManager()
	spoolErr := errors.New("spool full")
	calls := 0
	manager.SetLogPublisher(managerLogPublisherFunc(func(_ context.Context, entry *logstream.BuildLogMessage) error {
		calls++
		if entry.Content != "***" {
			t.Errorf("unredacted tail: %q", entry.Content)
		}
		return spoolErr
	}))
	result, err := manager.ExecuteWithExecutor(context.Background(), &ExecutionRequest{
		Step: &StepInfo{Name: "tail"}, Env: map[string]string{"secrets.token": "private-123"},
	}, &mockExecutor{name: "tail", executeFunc: func(_ context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
		req.OutputHandler("stdout", "private-123")
		if calls != 0 {
			t.Error("secret should remain buffered until final flush")
		}
		return &ExecutionResult{Success: true, Output: "private-123", Metadata: map[string]any{"logsStreamed": true}}, nil
	}})
	if !errors.Is(err, spoolErr) || result.Success || calls != 1 {
		t.Fatalf("final redactor tail rejection was ignored: %+v %v calls=%d", result, err, calls)
	}
}

func TestManagerConcurrentLogRejectionsAreRememberedOnce(t *testing.T) {
	manager := NewExecutorManager()
	spoolErr := errors.New("spool full")
	calls := 0
	manager.SetLogPublisher(managerLogPublisherFunc(func(context.Context, *logstream.BuildLogMessage) error {
		calls++
		return spoolErr
	}))
	result, err := manager.ExecuteWithExecutor(context.Background(), &ExecutionRequest{Step: &StepInfo{Name: "concurrent"}}, &mockExecutor{
		name: "concurrent",
		executeFunc: func(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
			var writers sync.WaitGroup
			for i := 0; i < 20; i++ {
				writers.Add(1)
				safe.Go(func() {
					defer writers.Done()
					req.OutputHandler("stdout", "line")
				})
			}
			writers.Wait()
			return &ExecutionResult{Success: true, Output: "line"}, ctx.Err()
		},
	})
	if !errors.Is(err, spoolErr) || !errors.Is(err, context.Canceled) || result.Success || calls != 1 {
		t.Fatalf("concurrent log failure was lost or retried: %+v %v calls=%d", result, err, calls)
	}
}

func TestManagerCanceledLogsShareBoundedPublicationWindow(t *testing.T) {
	manager := NewExecutorManager()
	var deadlines []time.Time
	manager.SetLogPublisher(managerLogPublisherFunc(func(ctx context.Context, _ *logstream.BuildLogMessage) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second || ctx.Err() != nil {
			t.Errorf("canceled output lacks an independent bounded window: deadline=%v err=%v", deadline, ctx.Err())
		}
		deadlines = append(deadlines, deadline)
		return nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := manager.ExecuteWithExecutor(ctx, &ExecutionRequest{Step: &StepInfo{Name: "cancel"}}, &mockExecutor{
		name: "cancel",
		executeFunc: func(_ context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
			cancel()
			req.OutputHandler("stdout", "first")
			req.OutputHandler("stderr", "second")
			return &ExecutionResult{Success: true}, nil
		},
	})
	if !errors.Is(err, context.Canceled) || result.Success {
		t.Fatalf("external cancellation was ignored: %+v %v", result, err)
	}
	if len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
		t.Fatalf("canceled output renewed its publication deadline: %v", deadlines)
	}
}

func TestManagerPublisherDeadlineCancelsShell(t *testing.T) {
	manager := NewExecutorManager()
	manager.Register(NewShellExecutor())
	manager.SetLogPublisher(managerLogPublisherFunc(func(ctx context.Context, _ *logstream.BuildLogMessage) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	started := time.Now()
	result, err := manager.Execute(ctx, &ExecutionRequest{
		Step: &StepInfo{Name: "blocked-publisher", Args: map[string]any{"run": "printf 'line'; sleep 30 & wait"}},
	})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, context.Canceled) || result.Success {
		t.Fatalf("publisher timeout or command cancellation was lost: %+v %v", result, err)
	}
	if cause := ExecutionContextCause(err); cause != nil {
		t.Fatalf("log publication timeout was misidentified as an execution timeout: %v", cause)
	}
	if elapsed := time.Since(started); elapsed > 6*time.Second {
		t.Fatalf("publication exceeded its bounded window: %v", elapsed)
	}
}

func TestManagerDurableSpoolRejectionCancelsShell(t *testing.T) {
	diskErr := errors.New("test disk sync failed")
	for _, test := range []struct {
		name     string
		maxBytes int64
		err      error
		failSync bool
	}{
		{"full", 1, ErrBuildLogSpoolFull, false},
		{"disk-sync-failed", 1 << 20, diskErr, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			publisher, err := NewDurableLogPublisher(context.Background(), managerLogPublisherFunc(func(context.Context, *logstream.BuildLogMessage) error {
				t.Error("an unstarted spool must not contact its transport")
				return nil
			}), t.TempDir(), test.maxBytes)
			if err != nil {
				t.Fatal(err)
			}
			if test.failSync {
				publisher.syncFile = func(*os.File) error { return diskErr }
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := publisher.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			manager := NewExecutorManager()
			manager.Register(NewShellExecutor())
			manager.SetLogPublisher(publisher)
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			started := time.Now()
			result, err := manager.Execute(ctx, &ExecutionRequest{
				StepRunID: "rejected-spool-step-run",
				Step:      &StepInfo{Name: "rejected-spool", Args: map[string]any{"run": "printf 'cannot-retain-this-output'; sleep 30 & wait"}},
				Workspace: t.TempDir(),
			})
			if !errors.Is(err, test.err) || !errors.Is(err, context.Canceled) || result == nil || result.Success {
				t.Fatalf("real spool rejection did not cancel the shell: %+v %v", result, err)
			}
			if strings.Contains(err.Error(), "cannot-retain-this-output") {
				t.Fatalf("raw output leaked into spool failure: %v", err)
			}
			if elapsed := time.Since(started); elapsed > 3*time.Second {
				t.Fatalf("spool rejection did not promptly stop the command: %v", elapsed)
			}
		})
	}
}

func TestManagerExecutionTimeoutWithRejectedLogTailPreservesCause(t *testing.T) {
	manager := NewExecutorManager()
	manager.Register(NewShellExecutor())
	calls := 0
	manager.SetLogPublisher(managerLogPublisherFunc(func(_ context.Context, entry *logstream.BuildLogMessage) error {
		calls++
		if entry.Content != "***" {
			t.Errorf("unsafe timeout tail: %q", entry.Content)
		}
		return ErrBuildLogSpoolFull
	}))
	result, err := manager.Execute(context.Background(), &ExecutionRequest{
		Step:    &StepInfo{Name: "timeout-tail", Args: map[string]any{"run": "printf 'secret-token-value'; sleep 30 & wait"}},
		Env:     map[string]string{"secrets.token": "secret-token-value"},
		Options: &ExecutionOptions{Timeout: 50 * time.Millisecond},
	})
	if !errors.Is(err, ErrBuildLogPersistence) || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(ExecutionContextCause(err), context.DeadlineExceeded) {
		t.Fatalf("actual execution timeout provenance was lost: %v cause=%v", err, ExecutionContextCause(err))
	}
	if result == nil || result.Success || calls != 1 {
		t.Fatalf("expected failed final log tail: %+v calls=%d", result, calls)
	}
}
