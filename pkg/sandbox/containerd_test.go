// Copyright 2025 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
)

type fakeBackend struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	closed     int
	createHook func()
	createErr  error
	cleanupErr error
}

func (b *fakeBackend) create(ctx context.Context, id, _ string, options []oci.SpecOpts, labels map[string]string) (runtimeContainer, error) {
	if b.createHook != nil {
		b.createHook()
	}
	if namespace, ok := namespaces.Namespace(ctx); !ok || namespace != "arcentra-test" {
		return nil, fmt.Errorf("missing namespace")
	}
	spec, err := oci.GenerateSpec(ctx, nil, &containers.Container{ID: id}, options...)
	if err != nil {
		return nil, err
	}
	if labels["io.arcentra.sandbox"] != "true" {
		return nil, fmt.Errorf("missing ownership label")
	}
	container := &fakeContainer{base: spec, removeErr: b.cleanupErr, task: &fakeTask{fakeProcess: newFakeProcess(false), pid: 1234}}
	b.mu.Lock()
	b.containers[id] = container
	b.mu.Unlock()
	return container, b.createErr
}
func (b *fakeBackend) close() error { b.mu.Lock(); defer b.mu.Unlock(); b.closed++; return nil }
func (b *fakeBackend) lookup(id string) *fakeContainer {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.containers[id]
}

type fakeContainer struct {
	base       *specs.Spec
	task       *fakeTask
	removeErr  error
	removed    int
	newTasks   int
	newTaskErr error
}

func (c *fakeContainer) spec(context.Context) (*specs.Spec, error) { return c.base, nil }
func (c *fakeContainer) newTask(_ context.Context, stdout, stderr io.Writer) (runtimeTask, error) {
	c.newTasks++
	c.task.stdout, c.task.stderr = stdout, stderr
	if c.newTaskErr != nil {
		return nil, c.newTaskErr
	}
	return c.task, nil
}
func (c *fakeContainer) loadTask(context.Context) (runtimeTask, error) {
	if c.newTasks == 0 || c.task.deleted > 0 {
		return nil, errdefs.ErrNotFound
	}
	return c.task, nil
}
func (c *fakeContainer) remove(ctx context.Context) error {
	if ctx.Err() != nil {
		return fmt.Errorf("cleanup inherited cancellation: %w", ctx.Err())
	}
	if c.removeErr != nil {
		return c.removeErr
	}
	c.removed++
	return nil
}

type fakeProcess struct {
	mu                  sync.Mutex
	status              containerd.ProcessStatus
	exits               []chan containerd.ExitStatus
	exit                *containerd.ExitStatus
	completeOnStart     bool
	waitCalled          bool
	startErr            error
	waitErr             error
	exitErr             error
	deleteErr           error
	deleted             int
	code                uint32
	stdout, stderr      io.Writer
	output, errorOutput string
	deleteOutput        string
	started             chan struct{}
}

func newFakeProcess(complete bool) *fakeProcess {
	return &fakeProcess{status: containerd.Created, completeOnStart: complete, started: make(chan struct{})}
}
func (p *fakeProcess) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p.startErr != nil {
		return p.startErr
	}
	if p.completeOnStart && !p.waitCalled {
		return fmt.Errorf("Wait must precede Start")
	}
	p.status = containerd.Running
	close(p.started)
	if p.stdout != nil {
		_, _ = io.WriteString(p.stdout, p.output)
	}
	if p.stderr != nil {
		_, _ = io.WriteString(p.stderr, p.errorOutput)
	}
	if p.completeOnStart {
		p.finishLocked(p.code, p.exitErr)
	}
	return nil
}
func (p *fakeProcess) Wait(ctx context.Context) (<-chan containerd.ExitStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if p.waitErr != nil {
		return nil, p.waitErr
	}
	p.waitCalled = true
	ch := make(chan containerd.ExitStatus, 1)
	if p.exit != nil {
		ch <- *p.exit
		close(ch)
	} else {
		p.exits = append(p.exits, ch)
	}
	return ch, nil
}
func (p *fakeProcess) finishLocked(code uint32, err error) {
	if p.exit != nil {
		return
	}
	p.status = containerd.Stopped
	p.exit = containerd.NewExitStatus(code, time.Now(), err)
	for _, ch := range p.exits {
		ch <- *p.exit
		close(ch)
	}
	p.exits = nil
}
func (p *fakeProcess) Kill(ctx context.Context, _ syscall.Signal, _ ...containerd.KillOpts) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	p.finishLocked(137, nil)
	return nil
}
func (p *fakeProcess) Delete(ctx context.Context, _ ...containerd.ProcessDeleteOpts) (*containerd.ExitStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("cleanup inherited cancellation: %w", ctx.Err())
	}
	if p.deleteErr != nil {
		return nil, p.deleteErr
	}
	if p.status == containerd.Running {
		return nil, fmt.Errorf("deleting running process")
	}
	if p.stdout != nil && p.deleteOutput != "" {
		_, _ = io.WriteString(p.stdout, p.deleteOutput)
		p.deleteOutput = ""
	}
	p.deleted++
	return containerd.NewExitStatus(0, time.Now(), nil), nil
}
func (p *fakeProcess) Status(ctx context.Context) (containerd.Status, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx.Err() != nil {
		return containerd.Status{}, ctx.Err()
	}
	return containerd.Status{Status: p.status}, nil
}

type fakeTask struct {
	*fakeProcess
	pid        uint32
	process    *fakeProcess
	execSpec   *specs.Process
	execErr    error
	signals    []syscall.Signal
	ignoreTerm bool
	killAll    bool
}

func (t *fakeTask) Pid() uint32 { return t.pid }
func (t *fakeTask) Delete(ctx context.Context, opts ...containerd.ProcessDeleteOpts) (*containerd.ExitStatus, error) {
	t.mu.Lock()
	createdWithPID := t.status == containerd.Created && t.pid != 0
	t.mu.Unlock()
	if createdWithPID {
		return nil, fmt.Errorf("created task with init PID must be killed before deletion")
	}
	return t.fakeProcess.Delete(ctx, opts...)
}

func (t *fakeTask) exec(_ context.Context, _ string, spec *specs.Process, _ io.Reader, stdout, stderr io.Writer) (runtimeProcess, error) {
	if t.execErr != nil {
		return nil, t.execErr
	}
	t.execSpec = spec
	if t.process == nil {
		t.process = newFakeProcess(true)
		t.process.output = "requested command output"
		t.process.errorOutput = "warning"
	}
	t.process.stdout, t.process.stderr = stdout, stderr
	return t.process, nil
}
func (t *fakeTask) Kill(ctx context.Context, signal syscall.Signal, opts ...containerd.KillOpts) error {
	var info containerd.KillInfo
	for _, opt := range opts {
		if err := opt(ctx, &info); err != nil {
			return err
		}
	}
	t.mu.Lock()
	t.signals = append(t.signals, signal)
	t.killAll = info.All
	ignored := t.ignoreTerm && signal == syscall.SIGTERM
	t.mu.Unlock()
	if ignored {
		return nil
	}
	if t.process != nil {
		if err := t.process.Kill(ctx, signal); err != nil {
			return err
		}
	}
	return t.fakeProcess.Kill(ctx, signal)
}

func newTestSandbox(t *testing.T) (*Containerd, *fakeBackend, string) {
	t.Helper()
	cfg, err := normalizeConfig(&ContainerdConfig{Namespace: "arcentra-test", DefaultImage: "test:local"})
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{containers: make(map[string]*fakeContainer)}
	sandbox := newSandbox(cfg, backend)
	id, err := sandbox.Create(context.Background(), &CreateOptions{Command: []string{"sleep", "3600"}, Env: map[string]string{"BASE": "from-image", "OVERRIDE": "old"}, WorkingDir: "/"})
	if err != nil {
		t.Fatal(err)
	}
	return sandbox, backend, id
}

func TestExecuteUsesActualCommandAndOptions(t *testing.T) {
	sb, backend, id := newTestSandbox(t)
	if _, err := sb.Execute(context.Background(), id, []string{"true"}, nil); err == nil {
		t.Fatal("Execute before Start succeeded")
	}
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	result, err := sb.Execute(context.Background(), id, []string{"sh", "-c", "printf requested"}, &ExecuteOptions{Env: map[string]string{"OVERRIDE": "new", "STEP": "yes"}, WorkingDir: "/tmp", User: "1000:1001", Stdout: &streamed})
	if err != nil {
		t.Fatal(err)
	}
	container := backend.lookup(id)
	process := container.task.execSpec
	if strings.Join(process.Args, " ") != "sh -c printf requested" || process.Cwd != "/tmp" || process.User.UID != 1000 || process.User.GID != 1001 {
		t.Fatalf("wrong process: %+v", process)
	}
	if strings.Join(process.Env, ",") != "BASE=from-image,OVERRIDE=new,STEP=yes" {
		t.Fatalf("wrong environment: %v", process.Env)
	}
	if container.newTasks != 1 || result.Stdout != "requested command output" || result.Stderr != "warning" || streamed.String() != result.Stdout || result.ExitCode != 0 {
		t.Fatalf("wrong execution result: %+v, new tasks: %d", result, container.newTasks)
	}
	if container.base.Process.Args[0] != "sleep" || container.base.Process.Env[1] != "OVERRIDE=old" {
		t.Fatal("base process spec mutated")
	}
	logs, err := sb.GetLogs(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(logs)
	_ = logs.Close()
	if string(content) != result.Stdout+result.Stderr {
		t.Fatalf("wrong logs: %q", content)
	}
	if err = sb.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.closed != 1 || container.removed != 1 {
		t.Fatal("Close did not clean resources")
	}
	if err = sb.Close(); err != nil || backend.closed != 1 {
		t.Fatal("Close is not idempotent")
	}
}

func TestExecuteNonzeroExitAndWaitFailure(t *testing.T) {
	for _, waitFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(waitFailure), func(t *testing.T) {
			sb, b, id := newTestSandbox(t)
			task := b.lookup(id).task
			task.process = newFakeProcess(true)
			task.process.code = 17
			if waitFailure {
				task.process.exitErr = errors.New("lost exit status")
			}
			if err := sb.Start(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			result, err := sb.Execute(context.Background(), id, []string{"false"}, nil)
			if waitFailure {
				if err == nil || !strings.Contains(err.Error(), "lost exit status") || !task.killAll {
					t.Fatalf("wait error swallowed: %v", err)
				}
			} else if err != nil || result.ExitCode != 17 {
				t.Fatalf("nonzero exit changed: %+v %v", result, err)
			}
			if err := sb.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExecuteTimeoutKillsAllWithFreshContext(t *testing.T) {
	sb, b, id := newTestSandbox(t)
	task := b.lookup(id).task
	task.process = newFakeProcess(false)
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	result, err := sb.Execute(context.Background(), id, []string{"sleep", "300"}, &ExecuteOptions{Timeout: 10 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || result.ExitCode != -1 {
		t.Fatalf("wrong timeout result: %+v %v", result, err)
	}
	if !task.killAll || task.process.deleted != 1 || len(task.signals) != 1 || task.signals[0] != syscall.SIGKILL {
		t.Fatalf("incomplete cancellation: %+v", task)
	}
	if _, err := sb.Execute(context.Background(), id, []string{"true"}, nil); err == nil {
		t.Fatal("timed-out container reused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sb.Remove(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := sb.Remove(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestStopActuallyWaitsAndEscalates(t *testing.T) {
	sb, b, id := newTestSandbox(t)
	task := b.lookup(id).task
	task.ignoreTerm = true
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := sb.Stop(context.Background(), id, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 20*time.Millisecond || len(task.signals) != 2 || task.signals[0] != syscall.SIGTERM || task.signals[1] != syscall.SIGKILL || !task.killAll {
		t.Fatalf("Stop did not wait/escalate: %v", task.signals)
	}
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupFailureIsRetainedAndRetried(t *testing.T) {
	sb, b, id := newTestSandbox(t)
	container := b.lookup(id)
	container.removeErr = errors.New("snapshot busy")
	if err := sb.Close(); err == nil || !strings.Contains(err.Error(), "snapshot busy") {
		t.Fatalf("cleanup failure swallowed: %v", err)
	}
	if _, err := sb.getContainer(id); err != nil || b.closed != 0 {
		t.Fatal("failed cleanup was forgotten")
	}
	if _, err := sb.Create(context.Background(), &CreateOptions{Command: []string{"true"}}); err == nil {
		t.Fatal("Create accepted during Close")
	}
	container.removeErr = nil
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
	if b.closed != 1 {
		t.Fatal("backend not closed after retry")
	}
}

func TestStartFailureCleansCreatedTask(t *testing.T) {
	sb, b, id := newTestSandbox(t)
	task := b.lookup(id).task
	task.startErr = errors.New("start failed")
	if err := sb.Start(context.Background(), id); err == nil {
		t.Fatal("start error swallowed")
	}
	if task.deleted != 1 || len(task.signals) != 1 || task.signals[0] != syscall.SIGKILL {
		t.Fatal("failed Start did not kill and delete created init task")
	}
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExecCreateFailureStopsContainer(t *testing.T) {
	sb, b, id := newTestSandbox(t)
	task := b.lookup(id).task
	task.execErr = errors.New("uncertain exec RPC")
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Execute(context.Background(), id, []string{"true"}, nil); err == nil || !task.killAll {
		t.Fatalf("uncertain exec did not stop container: %v", err)
	}
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentContainerMap(t *testing.T) {
	sb, _, id := newTestSandbox(t)
	if err := sb.Remove(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		safe.Go(func() {
			defer wg.Done()
			id, err := sb.Create(context.Background(), &CreateOptions{Command: []string{"sleep", "60"}})
			if err != nil {
				t.Error(err)
				return
			}
			if err = sb.Start(context.Background(), id); err != nil {
				t.Error(err)
			}
			if err = sb.Remove(context.Background(), id); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBoundedOutputAndUnsupportedLogs(t *testing.T) {
	buffer := &boundedBuffer{}
	input := strings.Repeat("x", maxCapturedBytes+100)
	if n, err := io.WriteString(buffer, input); err != nil || n != len(input) || len(buffer.String()) != maxCapturedBytes {
		t.Fatal("output cap broken")
	}
	sb, _, id := newTestSandbox(t)
	if _, err := sb.GetLogs(context.Background(), id, &LogOptions{Follow: true}); err == nil {
		t.Fatal("unsupported log option silently ignored")
	}
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStopInterruptsRunningExec(t *testing.T) {
	sb, b, id := newTestSandbox(t)
	task := b.lookup(id).task
	task.process = newFakeProcess(false)
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	safe.Go(func() { _, err := sb.Execute(context.Background(), id, []string{"sleep", "300"}, nil); done <- err })
	select {
	case <-task.process.started:
	case <-time.After(time.Second):
		t.Fatal("exec did not start")
	}
	if err := sb.Stop(context.Background(), id, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("exec was not canceled: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop left Execute blocked")
	}
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFailedProcessReapBlocksReuseAndRetries(t *testing.T) {
	sb, b, id := newTestSandbox(t)
	task := b.lookup(id).task
	task.process = newFakeProcess(true)
	task.process.deleteErr = errors.New("delete process failed")
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Execute(context.Background(), id, []string{"true"}, nil); err == nil {
		t.Fatal("process deletion error swallowed")
	}
	if _, err := sb.Execute(context.Background(), id, []string{"true"}, nil); err == nil {
		t.Fatal("container with unreaped exec process reused")
	}
	if err := sb.Close(); err == nil || b.closed != 0 {
		t.Fatal("Close discarded failed process cleanup")
	}
	task.process.deleteErr = nil
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseWaitsForInflightCreation(t *testing.T) {
	cfg, _ := normalizeConfig(&ContainerdConfig{Namespace: "arcentra-test", DefaultImage: "test:local"})
	entered, release := make(chan struct{}), make(chan struct{})
	backend := &fakeBackend{containers: make(map[string]*fakeContainer), createHook: func() { close(entered); <-release }}
	sb := newSandbox(cfg, backend)
	created, closed := make(chan error, 1), make(chan error, 1)
	safe.Go(func() {
		_, err := sb.Create(context.Background(), &CreateOptions{Command: []string{"sleep", "60"}})
		created <- err
	})
	<-entered
	safe.Go(func() { closed <- sb.Close() })
	select {
	case err := <-closed:
		t.Fatalf("Close returned before Create settled: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	if err := <-created; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if len(sb.containers) != 0 || backend.closed != 1 {
		t.Fatal("in-flight creation leaked across Close")
	}
}

func TestUncertainTaskCreationReconcilesOnRemove(t *testing.T) {
	sb, b, id := newTestSandbox(t)
	container := b.lookup(id)
	container.newTaskErr = context.DeadlineExceeded
	if err := sb.Start(context.Background(), id); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("create task error not preserved: %v", err)
	}
	if err := sb.Remove(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if container.task.deleted != 1 || len(container.task.signals) != 1 || container.removed != 1 {
		t.Fatal("uncertain init task was not recovered, killed and removed")
	}
}

func TestUncertainContainerCreationRetainsRecoveryID(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(fmt.Sprint(cleanupFails), func(t *testing.T) {
			cfg, _ := normalizeConfig(&ContainerdConfig{Namespace: "arcentra-test", DefaultImage: "test:local"})
			backend := &fakeBackend{containers: make(map[string]*fakeContainer), createErr: context.DeadlineExceeded}
			if cleanupFails {
				backend.cleanupErr = errors.New("cleanup temporarily denied")
			}
			sb := newSandbox(cfg, backend)
			id, err := sb.Create(context.Background(), &CreateOptions{Command: []string{"sleep", "60"}})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("uncertain create error lost: %v", err)
			}
			if cleanupFails {
				if id == "" || len(sb.containers) != 1 {
					t.Fatal("failed cleanup forgot recoverable ID")
				}
				backend.lookup(id).removeErr = nil
			} else if id != "" || len(sb.containers) != 0 {
				t.Fatal("successfully cleaned uncertain creation retained")
			}
			if err := sb.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("log sink unavailable") }
func TestOutputSinkFailureIsReportedWithoutLosingCapture(t *testing.T) {
	sb, _, id := newTestSandbox(t)
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	result, err := sb.Execute(context.Background(), id, []string{"true"}, &ExecuteOptions{Stdout: failingWriter{}})
	if err == nil || !strings.Contains(err.Error(), "log sink unavailable") || result.Stdout != "requested command output" {
		t.Fatalf("sink error or captured output lost: %+v %v", result, err)
	}
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStartRejectsExitedInit(t *testing.T) {
	sb, b, id := newTestSandbox(t)
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	task := b.lookup(id).task
	if err := task.Kill(context.Background(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := sb.Start(context.Background(), id); err == nil {
		t.Fatal("Start reported dead init as running")
	}
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLateOutputFailureDuringDeleteIsReported(t *testing.T) {
	sb, b, id := newTestSandbox(t)
	task := b.lookup(id).task
	task.process = newFakeProcess(true)
	task.process.deleteOutput = "late-output"
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	result, err := sb.Execute(context.Background(), id, []string{"true"}, &ExecuteOptions{Stdout: failingWriter{}})
	if err == nil || !strings.Contains(err.Error(), "log sink unavailable") || result.Stdout != "late-output" {
		t.Fatalf("late sink error or output lost: %+v %v", result, err)
	}
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
}
