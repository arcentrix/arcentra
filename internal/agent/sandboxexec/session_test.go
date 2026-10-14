// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/pkg/logstream"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/sandbox"
)

type fakeBackend struct {
	create  func(context.Context, *sandbox.CreateOptions) (string, error)
	start   func(context.Context, string) error
	execute func(context.Context, string, []string, *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error)
	remove  func(context.Context, string) error
}

func (f *fakeBackend) Create(ctx context.Context, opts *sandbox.CreateOptions) (string, error) {
	if f.create != nil {
		return f.create(ctx, opts)
	}
	return "test-container", nil
}
func (f *fakeBackend) Start(ctx context.Context, id string) error {
	if f.start != nil {
		return f.start(ctx, id)
	}
	return nil
}
func (f *fakeBackend) Execute(ctx context.Context, id string, command []string, opts *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
	if f.execute != nil {
		return f.execute(ctx, id, command, opts)
	}
	return &sandbox.ExecuteResult{}, nil
}
func (f *fakeBackend) Remove(ctx context.Context, id string) error {
	if f.remove != nil {
		return f.remove(ctx, id)
	}
	return nil
}
func testOptions(t *testing.T) Options {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return Options{WorkspaceRoot: root, JobRunID: "job-123", Image: "approved-test-image", Resources: &sandbox.Resources{CPU: "500m", Memory: "64Mi"}, User: "1000:1001"}
}
func newTestSession(t *testing.T, backend *fakeBackend) *Session {
	t.Helper()
	s, err := NewSession(context.Background(), backend, testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return s
}
func request(s *Session, command string) *executor.ExecutionRequest {
	return &executor.ExecutionRequest{Step: &executor.StepInfo{Uses: "shell", Action: "run", Args: map[string]any{"run": command}}, Workspace: s.Workspace(), Env: map[string]string{"ONLY": "explicit"}, Options: &executor.ExecutionOptions{Timeout: time.Second}}
}

func TestSessionOwnsMinimalWorkspaceAndExplicitOptions(t *testing.T) {
	opts := testOptions(t)
	var create *sandbox.CreateOptions
	backend := &fakeBackend{create: func(_ context.Context, value *sandbox.CreateOptions) (string, error) {
		create = value
		return "one", nil
	}}
	s, err := NewSession(context.Background(), backend, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	rel, err := filepath.Rel(opts.WorkspaceRoot, s.Workspace())
	if err != nil || filepath.Dir(rel) != "." || rel == "." {
		t.Fatalf("workspace escaped root: %q %v", rel, err)
	}
	if create.User != opts.User || create.Image != opts.Image || create.NetworkMode != "none" || create.Privileged || len(create.Mounts) != 1 || create.Mounts[0].Source != s.Workspace() || create.Mounts[0].Target != "/workspace" || create.Mounts[0].ReadOnly || create.Mounts[0].Type != "bind" {
		t.Fatalf("unexpected create options: %+v", create)
	}
	if len(create.Env) != 0 || !reflect.DeepEqual(create.Resources, opts.Resources) || create.Resources == opts.Resources {
		t.Fatalf("unexpected environment/resources: %+v", create)
	}
	info, err := os.Stat(s.Workspace())
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("workspace not private: %v %v", info, err)
	}
	other, err := NewSession(context.Background(), backend, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(context.Background())
	if other.Workspace() == s.Workspace() {
		t.Fatal("job attempts shared a workspace")
	}
}

func TestSessionRejectsUnsafeConfigurationBeforeRuntime(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Options)
	}{
		{"relative-root", func(o *Options) { o.WorkspaceRoot = "relative" }},
		{"missing-root", func(o *Options) { o.WorkspaceRoot += "/missing" }},
		{"traversal-id", func(o *Options) { o.JobRunID = "../escape" }},
		{"empty-image", func(o *Options) { o.Image = "" }},
		{"missing-limits", func(o *Options) { o.Resources = nil }},
		{"missing-cpu", func(o *Options) { o.Resources.CPU = "" }},
		{"named-user", func(o *Options) { o.User = "root" }},
		{"overflow-user", func(o *Options) { o.User = "4294967296" }},
		{"writable-root", func(o *Options) {
			if err := os.Chmod(o.WorkspaceRoot, 0o777); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink-root", func(o *Options) {
			target := o.WorkspaceRoot
			o.WorkspaceRoot = filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(target, o.WorkspaceRoot); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := testOptions(t)
			test.mutate(&opts)
			backend := &fakeBackend{create: func(context.Context, *sandbox.CreateOptions) (string, error) {
				t.Fatal("invalid config reached runtime")
				return "", nil
			}}
			if s, err := NewSession(context.Background(), backend, opts); s != nil || err == nil {
				t.Fatalf("invalid config accepted: %v %v", s, err)
			}
		})
	}
}

func TestSessionAdapterForwardsCommandAndStreamsWithoutHostEnvironment(t *testing.T) {
	t.Setenv("HOST_ONLY_SECRET", "never-forward")
	backend := &fakeBackend{execute: func(_ context.Context, id string, cmd []string, opts *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
		if id != "test-container" || !reflect.DeepEqual(cmd, []string{"sh", "-ec", "echo sample"}) || opts.WorkingDir != "/workspace" || opts.User != "1000:1001" || opts.Timeout != time.Second || !reflect.DeepEqual(opts.Env, map[string]string{"ONLY": "explicit"}) {
			t.Errorf("bad forwarded execution: %q %+v", cmd, opts)
		}
		_, _ = io.WriteString(opts.Stdout, "output")
		_, _ = io.WriteString(opts.Stderr, "problem")
		return &sandbox.ExecuteResult{Stdout: "output", Stderr: "problem", OutputTruncated: true}, nil
	}}
	s := newTestSession(t, backend)
	req := request(s, "echo sample")
	var streams []string
	req.OutputHandler = func(stream, content string) { streams = append(streams, stream+":"+content) }
	result, err := s.Execute(context.Background(), req)
	if err != nil || !result.Success || result.Output != "output" || result.ErrorOutput != "problem" || result.Metadata["outputBytes"] != int64(13) || result.Metadata["logsStreamed"] != true || result.Metadata["outputTruncated"] != true || !reflect.DeepEqual(streams, []string{"stdout:output", "stderr:problem"}) {
		t.Fatalf("bad execution result: %+v %v %v", result, streams, err)
	}
}

func TestSessionRejectsFallbackAndWorkspaceOverride(t *testing.T) {
	backend := &fakeBackend{execute: func(context.Context, string, []string, *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
		t.Fatal("unsupported execution reached backend")
		return nil, nil
	}}
	s := newTestSession(t, backend)
	for _, mutate := range []func(*executor.ExecutionRequest){
		func(r *executor.ExecutionRequest) { r.Step.Uses = "native-plugin" },
		func(r *executor.ExecutionRequest) { r.Step.Uses = "http"; r.Step.Args["url"] = "https://example.test" },
		func(r *executor.ExecutionRequest) { r.Step.RunRemotely = true },
		func(r *executor.ExecutionRequest) { r.Step.Action = "upload" },
		func(r *executor.ExecutionRequest) { r.Workspace = t.TempDir() },
		func(r *executor.ExecutionRequest) { r.Step.Args = nil },
	} {
		req := request(s, "exit 0")
		mutate(req)
		if result, err := s.Execute(context.Background(), req); err == nil || result.Success {
			t.Fatal("unsupported execution accepted")
		}
	}
}

func TestSessionNonzeroExitCanContinueButRuntimeErrorPoisonsSession(t *testing.T) {
	calls := 0
	s := newTestSession(t, &fakeBackend{execute: func(context.Context, string, []string, *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
		calls++
		if calls == 1 {
			return &sandbox.ExecuteResult{ExitCode: 7}, nil
		}
		return nil, errors.New("transport failed")
	}})
	result, err := s.Execute(context.Background(), request(s, "first"))
	if err == nil || result.ExitCode != 7 || result.Success {
		t.Fatalf("nonzero exit was lost: %+v %v", result, err)
	}
	if _, err := s.Execute(context.Background(), request(s, "second")); err == nil {
		t.Fatal("runtime error was lost")
	}
	if _, err := s.Execute(context.Background(), request(s, "third")); err == nil || calls != 2 {
		t.Fatal("runtime failure did not prevent reuse")
	}
}

func TestSessionFailedCreationRetainsCleanupHandle(t *testing.T) {
	createErr, cleanupErr := errors.New("uncertain creation"), errors.New("remove failed")
	var workspace string
	removes := 0
	backend := &fakeBackend{
		create: func(_ context.Context, opts *sandbox.CreateOptions) (string, error) {
			workspace = opts.Mounts[0].Source
			return "uncertain", createErr
		},
		remove: func(ctx context.Context, id string) error {
			removes++
			if ctx.Err() != nil || id != "uncertain" {
				t.Errorf("bad cleanup context/ID")
			}
			if removes == 1 {
				return cleanupErr
			}
			return nil
		},
	}
	s, err := NewSession(context.Background(), backend, testOptions(t))
	if s == nil || !errors.Is(err, createErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("lost uncertain handle/errors: %v %v", s, err)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("workspace removed before container: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace leaked: %v", err)
	}
	if err := s.Close(context.Background()); err != nil || removes != 2 {
		t.Fatalf("cleanup not idempotent: %v %d", err, removes)
	}
}

func TestSessionStartFailureCleansWithCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var workspace string
	removed := false
	backend := &fakeBackend{
		create: func(_ context.Context, opts *sandbox.CreateOptions) (string, error) {
			workspace = opts.Mounts[0].Source
			return "created", nil
		},
		start: func(context.Context, string) error { cancel(); return context.Canceled },
		remove: func(ctx context.Context, _ string) error {
			removed = true
			if ctx.Err() != nil {
				t.Error("cleanup inherited cancellation")
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Error("cleanup has no deadline")
			}
			return nil
		},
	}
	s, err := NewSession(ctx, backend, testOptions(t))
	if s != nil || !errors.Is(err, context.Canceled) || !removed {
		t.Fatalf("start failure cleanup wrong: %v %v %v", s, err, removed)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace leaked: %v", err)
	}
}

func TestSessionCloseCancelsRunningStepBeforeDeletingWorkspace(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	var s *Session
	backend := &fakeBackend{
		execute: func(ctx context.Context, _ string, _ []string, _ *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
			close(started)
			<-ctx.Done()
			close(finished)
			return nil, ctx.Err()
		},
		remove: func(ctx context.Context, _ string) error {
			select {
			case <-finished:
			default:
				t.Error("removed before execution finished")
			}
			if ctx.Err() != nil {
				t.Error(ctx.Err())
			}
			if _, err := os.Stat(s.Workspace()); err != nil {
				t.Error("workspace removed before backend removal")
			}
			return nil
		},
	}
	s = newTestSession(t, backend)
	done := make(chan error, 1)
	safe.Go(func() { _, err := s.Execute(context.Background(), request(s, "sleep")); done <- err })
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestSessionRejectsWorkspaceReplacementAndCleanupDoesNotFollowSymlink(t *testing.T) {
	s := newTestSession(t, &fakeBackend{execute: func(context.Context, string, []string, *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
		t.Fatal("replaced workspace executed")
		return nil, nil
	}})
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.Workspace()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, s.Workspace()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(context.Background(), request(s, "exit 0")); err == nil {
		t.Fatal("workspace replacement accepted")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("cleanup followed symlink: %q %v", data, err)
	}
}

func TestSessionSerializesConcurrentExecAndClose(t *testing.T) {
	var mu sync.Mutex
	active, maxActive := 0, 0
	s := newTestSession(t, &fakeBackend{execute: func(ctx context.Context, _ string, _ []string, opts *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		_, _ = io.WriteString(opts.Stdout, "data")
		time.Sleep(time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return &sandbox.ExecuteResult{}, nil
	}})
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		safe.Go(func() { defer wg.Done(); _, _ = s.Execute(context.Background(), request(s, "true")) })
	}
	wg.Wait()
	for range 8 {
		wg.Add(1)
		safe.Go(func() {
			defer wg.Done()
			if err := s.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if maxActive != 1 {
		t.Fatalf("steps were not serialized: %d", maxActive)
	}
}

func TestSessionUsesManagerRedaction(t *testing.T) {
	const secret = "sensitive-session-token"
	backend := &fakeBackend{execute: func(_ context.Context, _ string, _ []string, opts *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
		_, _ = fmt.Fprint(opts.Stdout, secret)
		return &sandbox.ExecuteResult{Stdout: secret}, nil
	}}
	s := newTestSession(t, backend)
	manager := executor.NewExecutorManager()
	req := request(s, "echo token")
	req.Env["secrets.API_TOKEN"] = secret
	var seen strings.Builder
	req.OutputHandler = func(_, value string) { seen.WriteString(value) }
	result, err := manager.ExecuteWithExecutor(context.Background(), req, s)
	if err != nil || strings.Contains(result.Output, secret) || strings.Contains(seen.String(), secret) {
		t.Fatalf("manager redaction bypassed: %+v %q %v", result, seen.String(), err)
	}
}

type rejectingLogPublisher struct{ err error }

func (p rejectingLogPublisher) Publish(context.Context, *logstream.BuildLogMessage) error {
	return p.err
}

func TestSessionManagerLogFailureCancelsBackend(t *testing.T) {
	spoolErr := errors.New("disk full")
	s := newTestSession(t, &fakeBackend{execute: func(ctx context.Context, _ string, _ []string, opts *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
		_, _ = io.WriteString(opts.Stdout, "command-output")
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	manager := executor.NewExecutorManager()
	manager.SetLogPublisher(rejectingLogPublisher{spoolErr})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := manager.ExecuteWithExecutor(ctx, request(s, "long-running"), s)
	if !errors.Is(err, executor.ErrBuildLogPersistence) || !errors.Is(err, spoolErr) || !errors.Is(err, context.Canceled) || result.Success {
		t.Fatalf("log rejection did not cancel/fail execution: %+v %v", result, err)
	}
}

func TestSessionTimeoutPreventsLaterExecution(t *testing.T) {
	calls := 0
	s := newTestSession(t, &fakeBackend{execute: func(ctx context.Context, _ string, _ []string, _ *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
		calls++
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	req := request(s, "sleep")
	req.Options.Timeout = 5 * time.Millisecond
	if _, err := s.Execute(context.Background(), req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout lost: %v", err)
	}
	if _, err := s.Execute(context.Background(), req); err == nil || calls != 1 {
		t.Fatal("timed-out session reused")
	}
}
