// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package sandbox

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/fifo"
	"github.com/opencontainers/runtime-spec/specs-go"
)

type completionIO struct {
	done, waiting  chan struct{}
	waitOnce, end  sync.Once
	waits, cancels atomic.Int64
	cancelUnblocks bool
}

func newCompletionIO(cancelUnblocks bool) *completionIO {
	return &completionIO{done: make(chan struct{}), waiting: make(chan struct{}), cancelUnblocks: cancelUnblocks}
}
func (s *completionIO) Config() cio.Config { return cio.Config{} }
func (s *completionIO) Close() error       { return nil }
func (s *completionIO) finish()            { s.end.Do(func() { close(s.done) }) }
func (s *completionIO) Cancel() {
	s.cancels.Add(1)
	if s.cancelUnblocks {
		s.finish()
	}
}
func (s *completionIO) Wait() {
	s.waits.Add(1)
	s.waitOnce.Do(func() { close(s.waiting) })
	<-s.done
}

type completionSDKProcess struct {
	containerd.Process
	fake    *fakeProcess
	stream  cio.IO
	deletes atomic.Int64
}

func (p *completionSDKProcess) IO() cio.IO                      { return p.stream }
func (p *completionSDKProcess) Start(ctx context.Context) error { return p.fake.Start(ctx) }
func (p *completionSDKProcess) Wait(ctx context.Context) (<-chan containerd.ExitStatus, error) {
	return p.fake.Wait(ctx)
}
func (p *completionSDKProcess) Status(ctx context.Context) (containerd.Status, error) {
	return p.fake.Status(ctx)
}
func (p *completionSDKProcess) Kill(ctx context.Context, signal syscall.Signal, opts ...containerd.KillOpts) error {
	return p.fake.Kill(ctx, signal, opts...)
}
func (p *completionSDKProcess) Delete(ctx context.Context, opts ...containerd.ProcessDeleteOpts) (*containerd.ExitStatus, error) {
	p.deletes.Add(1)
	// Match the actual SDK's non-context-aware post-delete IO teardown.
	if p.stream != nil {
		p.stream.Cancel()
		p.stream.Wait()
	}
	return p.fake.Delete(ctx, opts...)
}

type completionTask struct {
	runtimeTask
	makeProcess func(context.Context, io.Writer, io.Writer) (runtimeProcess, error)
}

func (t completionTask) exec(ctx context.Context, _ string, _ *specs.Process, _ io.Reader, stdout, stderr io.Writer) (runtimeProcess, error) {
	return t.makeProcess(ctx, stdout, stderr)
}

// Establish the copy-loop waiter before injecting any short deadline. Only
// this caller is canceled; the adapter's single IO waiter remains in flight.
func primeCompletionWait(t *testing.T, process runtimeProcess, waiting <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	safe.Go(func() { done <- process.waitOutput(ctx) })
	select {
	case <-waiting:
	case err := <-done:
		t.Fatalf("output wait returned before its copy loop started: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("output copy loop did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("priming caller did not observe cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("priming output wait ignored cancellation")
	}
}

// Inject deadline expiry at the observed-exit drain boundary, not while the
// test is still scheduling task/exec creation and exit delivery.
type completionExpiredDrain struct{ runtimeProcess }

func (p completionExpiredDrain) waitOutput(ctx context.Context) error {
	expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	return p.runtimeProcess.waitOutput(expired)
}

func TestRuntimeOutputCompletionCancellationAndSingleWaiter(t *testing.T) {
	stream := newCompletionIO(false)
	defer stream.finish()
	p := &containerdProcess{Process: &completionSDKProcess{stream: stream, fake: newFakeProcess(true)}}
	primeCompletionWait(t, p, stream.waiting)
	for range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		if err := p.waitOutput(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("missing bounded drain error: %v", err)
		}
		cancel()
	}
	if stream.waits.Load() != 1 || stream.cancels.Load() != 0 {
		t.Fatal("natural drain duplicated a waiter or canceled unread output")
	}
	stream.finish()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.waitOutput(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ready output revived canceled execution: %v", err)
	}
	if err := p.waitOutput(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeDeleteRetainsBlockedOutputUntilRetry(t *testing.T) {
	stream := newCompletionIO(false)
	defer stream.finish()
	sdk := &completionSDKProcess{stream: stream, fake: newFakeProcess(true)}
	p := &containerdProcess{Process: sdk}
	primeCompletionWait(t, p, stream.waiting)
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		_, err := p.Delete(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || sdk.deletes.Load() != 0 {
			t.Fatalf("blocked output entered SDK delete: %v", err)
		}
	}
	if stream.waits.Load() != 1 {
		t.Fatal("cleanup retry accumulated waiters")
	}
	stream.finish()
	if _, err := p.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sdk.deletes.Load() != 1 {
		t.Fatal("cleanup did not delete exactly once after release")
	}
}

type completionSDKTask struct {
	containerd.Task
	stream  cio.IO
	deletes int
}

func (t *completionSDKTask) IO() cio.IO { return t.stream }
func (t *completionSDKTask) Delete(context.Context, ...containerd.ProcessDeleteOpts) (*containerd.ExitStatus, error) {
	t.deletes++
	t.stream.Cancel()
	t.stream.Wait()
	return containerd.NewExitStatus(0, time.Now(), nil), nil
}
func TestRuntimeTaskDeleteAlsoBoundsSDKIOWait(t *testing.T) {
	stream := newCompletionIO(false)
	defer stream.finish()
	sdk := &completionSDKTask{stream: stream}
	task := &containerdTask{Task: sdk}
	primeCompletionWait(t, task, stream.waiting)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := task.Delete(ctx); !errors.Is(err, context.DeadlineExceeded) || sdk.deletes != 0 {
		t.Fatalf("task cleanup bypassed IO wait: %v", err)
	}
	stream.finish()
	if _, err := task.Delete(context.Background()); err != nil || sdk.deletes != 1 {
		t.Fatalf("task cleanup retry: %v", err)
	}
}

type completionObservedIO struct {
	cio.IO
	waiting chan struct{}
	once    sync.Once
}

func (s *completionObservedIO) Wait() { s.once.Do(func() { close(s.waiting) }); s.IO.Wait() }

// Actual SDK FIFO readers, without a daemon: exit is delivered before either
// output stream finishes. Both outputs must reach capture and sink before Delete.
func TestExecuteDrainsActualFIFOTailAfterObservedExit(t *testing.T) {
	sb, backend, id := newTestSandbox(t)
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	state, _ := sb.getContainer(id)
	base := newFakeProcess(true)
	backend.lookup(id).task.process = base
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	waiting := make(chan struct{})
	producerDone := make(chan error, 1)
	sink := &runtimeIOGatedWriter{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(sink.release) }) }
	defer release()
	var sdk *completionSDKProcess
	state.task = completionTask{runtimeTask: state.task, makeProcess: func(_ context.Context, stdout, stderr io.Writer) (runtimeProcess, error) {
		stream, err := newRuntimeIO(ctx, runtimeIOTestRoot(t), runtimeIOTestRef(), nil, stdout, stderr)("completion-exec")
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { release(); stream.Cancel(); stream.Wait(); _ = stream.Close() })
		sdk = &completionSDKProcess{fake: base, stream: &completionObservedIO{IO: stream, waiting: waiting}}
		safe.Go(func() {
			<-waiting
			out, err := fifo.OpenFifo(ctx, stream.Config().Stdout, syscall.O_WRONLY, 0600)
			if err != nil {
				producerDone <- err
				return
			}
			defer out.Close()
			errout, err := fifo.OpenFifo(ctx, stream.Config().Stderr, syscall.O_WRONLY, 0600)
			if err != nil {
				producerDone <- err
				return
			}
			defer errout.Close()
			_, err = io.WriteString(out, "first\n")
			if err != nil {
				producerDone <- err
				return
			}
			select {
			case <-sink.entered:
			case <-ctx.Done():
				producerDone <- ctx.Err()
				return
			}
			_, err = io.WriteString(out, "tail-after-exit\n")
			if err == nil {
				_, err = io.WriteString(errout, "stderr-tail-after-exit\n")
			}
			producerDone <- errors.Join(err, out.Close(), errout.Close())
		})
		return &containerdProcess{Process: sdk}, nil
	}}
	type outcome struct {
		result *ExecuteResult
		err    error
	}
	done := make(chan outcome, 1)
	safe.Go(func() {
		result, err := sb.Execute(ctx, id, []string{"true"}, &ExecuteOptions{Stdout: sink})
		done <- outcome{result, err}
	})
	select {
	case err := <-producerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case result := <-done:
		t.Fatalf("exit discarded pending tail: %+v", result)
	default:
	}
	if sdk.deletes.Load() != 0 {
		t.Fatal("deleted before output completion")
	}
	release()
	select {
	case got := <-done:
		if got.err != nil || got.result.ExitCode != 0 || got.result.Stdout != "first\ntail-after-exit\n" || got.result.Stderr != "stderr-tail-after-exit\n" || sink.output.String() != got.result.Stdout {
			t.Fatalf("lost completed output: %+v %v", got.result, got.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if sdk.deletes.Load() != 1 {
		t.Fatal("successful output was not reaped once")
	}
}

func TestExecuteMissingOutputEOFFailsClosedAndCleansUp(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "cancel"}[canceled], func(t *testing.T) {
			sb, backend, id := newTestSandbox(t)
			if err := sb.Start(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			state, _ := sb.getContainer(id)
			stream := newCompletionIO(true)
			base := newFakeProcess(true)
			backend.lookup(id).task.process = base
			sdk := &completionSDKProcess{fake: base, stream: stream}
			process := &containerdProcess{Process: sdk}
			var execution runtimeProcess = process
			if !canceled {
				primeCompletionWait(t, process, stream.waiting)
				execution = completionExpiredDrain{runtimeProcess: process}
			}
			state.task = completionTask{runtimeTask: state.task, makeProcess: func(context.Context, io.Writer, io.Writer) (runtimeProcess, error) {
				return execution, nil
			}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				safe.Go(func() { <-stream.waiting; cancel() })
			}
			result, err := sb.Execute(ctx, id, []string{"true"}, nil)
			want := context.DeadlineExceeded
			if canceled {
				want = context.Canceled
			}
			if !errors.Is(err, want) || !strings.Contains(err.Error(), "drain exec output") || result.ExitCode != 0 {
				t.Fatalf("missing EOF accepted as success: %+v %v", result, err)
			}
			if !state.stopped || len(state.processes) != 0 || sdk.deletes.Load() != 1 || !backend.lookup(id).task.killAll {
				t.Fatal("failed drain did not stop/reap exact execution")
			}
			if _, err := sb.Execute(context.Background(), id, []string{"true"}, nil); err == nil {
				t.Fatal("failed drain revived execution")
			}
			if err := sb.Remove(context.Background(), id); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLiveFailedStepWithMissingOutputCannotAdvance(t *testing.T) {
	live, c, _ := newLiveFixture(t, true)
	stream := newCompletionIO(true)
	base := newFakeProcess(true)
	base.code = 23
	c.task.process = base
	sdk := &completionSDKProcess{fake: base, stream: stream}
	process := &containerdProcess{Process: sdk}
	primeCompletionWait(t, process, stream.waiting)
	live.state.task = completionTask{runtimeTask: live.state.task, makeProcess: func(context.Context, io.Writer, io.Writer) (runtimeProcess, error) {
		return completionExpiredDrain{runtimeProcess: process}, nil
	}}
	result, boundary, err := live.ExecuteStep(context.Background(), liveStep(0, 1), liveCommand, nil)
	if !errors.Is(err, context.DeadlineExceeded) || result.ExitCode != 23 || boundary != nil || live.pending != nil || !live.proof.invalid || c.task.pauses != 0 {
		t.Fatalf("failed drain created a retry capability: %+v %v %v", result, boundary, err)
	}
	if _, _, err := live.ExecuteStep(context.Background(), liveStep(1, 1), liveCommand, nil); err == nil {
		t.Fatal("failed drain advanced the next step")
	}
	if _, _, err := live.ResumeStep(context.Background(), boundary, liveStep(0, 2), liveCommand, nil); err == nil {
		t.Fatal("failed drain admitted retry")
	}
}

func TestOutputErrorSnapshotDoesNotWaitForBlockedSink(t *testing.T) {
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	failure := errors.New("sink failed")
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	stream := &outputStream{capture: &boundedBuffer{}, sink: completionWriter(func(p []byte) (int, error) { close(entered); <-release; return 0, failure })}
	safe.Go(func() { _, _ = stream.Write([]byte("captured-before-block")); close(finished) })
	<-entered
	snapshot := make(chan error, 1)
	safe.Go(func() { snapshot <- stream.Error() })
	select {
	case err := <-snapshot:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("error snapshot blocked on user sink")
	}
	if got := stream.capture.String(); got != "captured-before-block" {
		t.Fatalf("capture unavailable: %q", got)
	}
	unblock()
	<-finished
	if !errors.Is(stream.Error(), failure) {
		t.Fatal("completed sink failure lost")
	}
	if _, err := stream.Write([]byte("tail")); err != nil || !errors.Is(stream.Error(), failure) {
		t.Fatal("sink failure did not remain sticky")
	}
}

type completionWriter func([]byte) (int, error)

func (w completionWriter) Write(p []byte) (int, error) { return w(p) }

// The real cleanup timeout stays unchanged. This fixture supplies a shorter
// deadline to the same production Delete wrapper to exercise retained evidence
// without making every unit/race repetition wait twenty seconds.
type completionShortCleanup struct {
	runtimeProcess
	shortDeadline bool
}

func (p *completionShortCleanup) Delete(ctx context.Context, opts ...containerd.ProcessDeleteOpts) (*containerd.ExitStatus, error) {
	if !p.shortDeadline {
		return p.runtimeProcess.Delete(ctx, opts...)
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	return p.runtimeProcess.Delete(bounded, opts...)
}

type completionBlockedIO struct {
	*completionIO
	writer io.Writer
}

func (s *completionBlockedIO) Wait() {
	s.waits.Add(1)
	s.waitOnce.Do(func() { close(s.waiting); _, _ = s.writer.Write([]byte("pending-output")); s.finish() })
	<-s.done
}

func TestExecuteBlockedSinkRetainsResourcesAndFailure(t *testing.T) {
	sb, backend, id := newTestSandbox(t)
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	state, _ := sb.getContainer(id)
	base := newFakeProcess(true)
	container := backend.lookup(id)
	container.task.process = base
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	failure := errors.New("late output failure")
	var stream *completionBlockedIO
	var sdk *completionSDKProcess
	var process *completionShortCleanup
	state.task = completionTask{runtimeTask: state.task, makeProcess: func(_ context.Context, stdout, _ io.Writer) (runtimeProcess, error) {
		stream = &completionBlockedIO{completionIO: newCompletionIO(false), writer: stdout}
		sdk = &completionSDKProcess{fake: base, stream: stream}
		process = &completionShortCleanup{runtimeProcess: &containerdProcess{Process: sdk}, shortDeadline: true}
		return process, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	safe.Go(func() { <-entered; cancel() })
	result, err := sb.Execute(ctx, id, []string{"true"}, &ExecuteOptions{Stdout: completionWriter(func(p []byte) (int, error) { close(entered); <-release; return 0, failure })})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) || result.ExitCode != 0 || result.Stdout != "pending-output" {
		t.Fatalf("blocked sink lost failure or capture: %+v %v", result, err)
	}
	if sdk.deletes.Load() != 0 || len(state.processes) != 1 || !state.stopped || container.removed != 0 {
		t.Fatal("blocked sink lost cleanup evidence")
	}
	if removeErr := sb.Remove(context.Background(), id); !errors.Is(removeErr, context.DeadlineExceeded) || len(state.processes) != 1 || container.removed != 0 {
		t.Fatalf("blocked cleanup falsely succeeded: %v", removeErr)
	}
	if _, nextErr := sb.Execute(context.Background(), id, []string{"true"}, nil); nextErr == nil {
		t.Fatal("blocked output allowed another command")
	}
	unblock()
	process.shortDeadline = false
	if removeErr := sb.Remove(context.Background(), id); removeErr != nil {
		t.Fatalf("released exact-owned cleanup could not retry: %v", removeErr)
	}
	if sdk.deletes.Load() != 1 || len(state.processes) != 0 || container.removed != 1 {
		t.Fatal("released cleanup did not reap exactly once")
	}
	if !errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) || result.ExitCode != 0 {
		t.Fatal("late writer release altered the earlier failed result")
	}
}

type completionAbsentBackend struct {
	runtimeBackend
	container runtimeContainer
}

func (b completionAbsentBackend) inspectOwned(context.Context, ResourceRef) (runtimeContainer, ResourceState, error) {
	return b.container, ResourceState{}, nil
}

func TestRemoveRetainsLocalOutputWhenRuntimeTaskIsAbsent(t *testing.T) {
	for _, allAbsent := range []bool{false, true} {
		t.Run(map[bool]string{false: "task-absent", true: "all-runtime-metadata-absent"}[allAbsent], func(t *testing.T) {
			sb, backend, id := newTestSandbox(t)
			state, _ := sb.getContainer(id)
			container := backend.lookup(id)
			if allAbsent {
				sb.backend = completionAbsentBackend{runtimeBackend: sb.backend, container: container}
			}
			stream := newCompletionIO(false)
			defer stream.finish()
			sdk := &completionSDKProcess{fake: newFakeProcess(true), stream: stream}
			sdk.fake.deleteErr = errdefs.ErrNotFound
			process := &completionShortCleanup{runtimeProcess: &containerdProcess{Process: sdk}, shortDeadline: true}
			primeCompletionWait(t, process, stream.waiting)
			state.processes["retained-exec"] = process
			if err := sb.Remove(context.Background(), id); !errors.Is(err, context.DeadlineExceeded) || state.removed || container.removed != 0 || len(state.processes) != 1 || sdk.deletes.Load() != 0 {
				t.Fatalf("runtime absence discarded live local IO: %v", err)
			}
			stream.finish()
			process.shortDeadline = false
			if err := sb.Remove(context.Background(), id); err != nil || !state.removed || container.removed != 1 || len(state.processes) != 0 || sdk.deletes.Load() != 1 {
				t.Fatalf("released absent task cleanup: %v", err)
			}
		})
	}
}

func TestRemoveRetainsInitOutputWhenAllRuntimeMetadataIsAbsent(t *testing.T) {
	sb, backend, id := newTestSandbox(t)
	state, _ := sb.getContainer(id)
	container := backend.lookup(id)
	sb.backend = completionAbsentBackend{runtimeBackend: sb.backend, container: container}
	stream := newCompletionIO(false)
	defer stream.finish()
	sdk := &completionSDKTask{stream: stream}
	state.task = &containerdTask{Task: sdk}
	primeCompletionWait(t, state.task, stream.waiting)
	// Exercise the ordinary shared removal implementation with a short budget.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := sb.removeStateLocked(ctx, id, state); !errors.Is(err, context.DeadlineExceeded) || state.task == nil || state.removed || container.removed != 0 || sdk.deletes != 0 {
		t.Fatalf("runtime absence discarded init IO: %v", err)
	}
	stream.finish()
	if err := sb.Remove(context.Background(), id); err != nil || !state.removed || state.task != nil || sdk.deletes != 1 || container.removed != 1 {
		t.Fatalf("released absent init cleanup: %v", err)
	}
}

type completionDeadlineProcess struct {
	runtimeProcess
	deadline time.Time
}

func (p *completionDeadlineProcess) waitOutput(ctx context.Context) error {
	var ok bool
	p.deadline, ok = ctx.Deadline()
	if !ok {
		return errors.New("output drain has no bound")
	}
	return p.runtimeProcess.waitOutput(ctx)
}
func TestExecuteOutputDrainUsesExistingCleanupCap(t *testing.T) {
	sb, _, id := newTestSandbox(t)
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	state, _ := sb.getContainer(id)
	base := newFakeProcess(true)
	process := &completionDeadlineProcess{runtimeProcess: base}
	state.task = completionTask{runtimeTask: state.task, makeProcess: func(context.Context, io.Writer, io.Writer) (runtimeProcess, error) { return process, nil }}
	before := time.Now()
	if _, err := sb.Execute(context.Background(), id, []string{"true"}, nil); err != nil {
		t.Fatal(err)
	}
	if process.deadline.Before(before.Add(cleanupTimeout)) || process.deadline.After(time.Now().Add(cleanupTimeout)) {
		t.Fatalf("drain extended or omitted the existing cleanup cap: %s", process.deadline)
	}
}
