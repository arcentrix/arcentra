// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandbox

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/bytedance/sonic"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/errdefs"
	"github.com/google/uuid"
)

func newLiveFixture(t *testing.T, start bool) (*liveSession, *fakeContainer, *fakeInitPin) {
	t.Helper()
	backend := &fakeBackend{containers: make(map[string]*fakeContainer)}
	sb := newSandbox(ContainerdConfig{Namespace: "arcentra-test", DefaultImage: "approved:test", EnableLiveStepRetry: true}, backend)
	ref, err := sb.PrepareResource(context.Background(), "arcentra-"+uuid.NewString(), "exact-owner")
	if err != nil {
		t.Fatal(err)
	}
	pin := &fakeInitPin{}
	value, err := sb.createLiveSession(context.Background(), &CreateOptions{Resource: &ref, User: "1000:1001", Command: []string{"untrusted-init"}}, LiveSessionIdentity{SessionID: uuid.NewString(), ExecutionClaimID: "original-claim"}, func(_ context.Context, pid uint32) (pinnedInit, error) {
		if pid != 1234 {
			t.Fatalf("wrong PID: %d", pid)
		}
		return pin, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	live := value.(*liveSession)
	c := backend.lookup(ref.ID)
	pin.task = c.task
	t.Cleanup(func() {
		// Negative cases intentionally replace ownership/pins; restore fake-only
		// attribution for teardown, never provide this recovery in production.
		c.labels = ownershipLabels(ref)
		c.task.pid = 1234
		c.task.pidsErr = nil
		c.task.deleteErr = nil
		c.task.pauseHook = nil
		c.task.resumeHook = nil
		c.task.execHook = nil
		c.task.stubbornChildren = false
		if c.task.exit == nil && c.task.status == containerd.Stopped {
			c.task.status = containerd.Paused
		}
		if c.task.process != nil {
			c.task.process.deleteErr = nil
		}
		live.state.quiescence = nil
		_ = pin.close()
		if err := sb.Close(); err != nil {
			t.Errorf("fixture cleanup: %v", err)
		}
	})
	if start {
		if err := live.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return live, c, pin
}
func liveStep(index, attempt int32) StepIdentity {
	id := fmt.Sprintf("step-%d-attempt-%d", index, attempt)
	digest := sha256.Sum256([]byte(id))
	step := StepIdentity{StepRunID: id, StepIndex: index, Attempt: attempt, AdmissionDigest: fmt.Sprintf("v1:%x", digest), NotBeforeUnixMilli: time.Now().Add(-time.Second).UnixMilli(), ExpiresUnixMilli: time.Now().Add(time.Minute).UnixMilli()}
	if attempt > 1 {
		step.PreviousStepRunID = fmt.Sprintf("step-%d-attempt-%d", index, attempt-1)
	}
	return step
}

var liveCommand = []string{"sh", "-ec", "unchanged command"}

func failLiveStep(t *testing.T, live *liveSession, c *fakeContainer) *FailedStepBoundary {
	t.Helper()
	c.task.process = newFakeProcess(true)
	c.task.process.code = 23
	out, b, err := live.ExecuteStep(context.Background(), liveStep(0, 1), liveCommand, nil)
	if err != nil || out.ExitCode != 23 || b == nil || c.task.status != containerd.Paused {
		t.Fatalf("failed boundary: %+v %v %v", out, b, err)
	}
	return b
}

func TestLiveSessionDefaultOffAndDurableCreateOnly(t *testing.T) {
	backend := &fakeBackend{containers: make(map[string]*fakeContainer)}
	sb := newSandbox(ContainerdConfig{Namespace: "arcentra-test", DefaultImage: "approved:test"}, backend)
	if _, err := sb.CreateLiveSession(context.Background(), &CreateOptions{}, LiveSessionIdentity{}); err == nil || len(backend.containers) != 0 {
		t.Fatal("default gate created a resource")
	}
	live, c, _ := newLiveFixture(t, false)
	if c.newTasks != 0 || live.started || strings.Join(c.base.Process.Args, " ") != "/bin/sleep 2147483647" {
		t.Fatal("creation started a task or kept caller init")
	}
	if err := live.sandbox.Start(context.Background(), live.Resource().ID); err == nil || c.newTasks != 0 {
		t.Fatal("ordinary Start bypassed live handle")
	}
	if err := live.Start(context.Background()); err != nil || c.newTasks != 1 {
		t.Fatalf("separate Start: %v", err)
	}
	if _, err := live.sandbox.Execute(context.Background(), live.Resource().ID, []string{"true"}, nil); err == nil {
		t.Fatal("ordinary Execute bypassed proof")
	}
}

func TestLiveSessionRequiresDurableIdentityAndNumericUser(t *testing.T) {
	for _, which := range []string{"resource", "nonce", "claim", "root", "named-user"} {
		t.Run(which, func(t *testing.T) {
			live, _, _ := newLiveFixture(t, false)
			opts := &CreateOptions{Resource: &live.resource, User: "1000:1001"}
			identity := live.identity
			switch which {
			case "resource":
				opts.Resource = nil
			case "nonce":
				identity.SessionID = "saved-json-id"
			case "claim":
				identity.ExecutionClaimID = ""
			case "root":
				opts.User = "0:1001"
			case "named-user":
				opts.User = "nobody"
			}
			if _, err := live.sandbox.CreateLiveSession(context.Background(), opts, identity); err == nil {
				t.Fatal("invalid live input accepted")
			}
		})
	}
}

func TestLiveSessionBoundaryObservationAndExactSuccessor(t *testing.T) {
	live, c, pin := newLiveFixture(t, true)
	boundary := failLiveStep(t, live, c)
	if boundary.FailedStep().StepRunID != "step-0-attempt-1" || boundary.ExitCode() != 23 || pin.closed {
		t.Fatal("incorrect boundary observation")
	}
	if err := live.CheckBoundary(context.Background(), boundary); err != nil || c.task.resumes != 0 {
		t.Fatalf("observation resumed: %v", err)
	}
	if _, err := sonic.Marshal(boundary); err == nil {
		t.Fatal("opaque boundary serialized")
	}
	if err := sonic.Unmarshal([]byte(`{}`), new(FailedStepBoundary)); err == nil {
		t.Fatal("opaque boundary reconstructed")
	}
	c.task.process = newFakeProcess(true)
	out, next, err := live.ResumeStep(context.Background(), boundary, liveStep(0, 2), liveCommand, nil)
	if err != nil || out.ExitCode != 0 || next != nil || c.task.resumes != 1 || !boundary.consumed || pin.closed {
		t.Fatalf("resume failed: %+v %v", out, err)
	}
	c.task.process = newFakeProcess(true)
	if _, next, err = live.ExecuteStep(context.Background(), liveStep(3, 1), []string{"next logical step"}, nil); err != nil || next != nil {
		t.Fatalf("skipped logical indices failed: %v", err)
	}
	// Reusing an old consumed handle must not enter Resume or execute again.
	if _, _, err = live.ResumeStep(context.Background(), boundary, liveStep(0, 2), liveCommand, nil); err == nil || c.task.resumes != 1 {
		t.Fatal("consumed permit admitted again")
	}
}

func TestLiveSessionRejectsForgedOrMismatchedAdmission(t *testing.T) {
	for _, which := range []string{"zero-boundary", "copied-boundary", "wrong-index", "skipped-attempt", "wrong-previous", "reused-id", "same-digest", "missing-digest", "bad-digest", "expired", "not-before", "command", "environment", "user", "directory", "timeout", "tty", "stdin", "ordinary-step"} {
		t.Run(which, func(t *testing.T) {
			live, c, _ := newLiveFixture(t, true)
			b := failLiveStep(t, live, c)
			step := liveStep(0, 2)
			cmd := slicesOf(liveCommand)
			var opts *ExecuteOptions
			switch which {
			case "zero-boundary":
				b = &FailedStepBoundary{}
			case "copied-boundary":
				copy := *b
				b = &copy
			case "wrong-index":
				step.StepIndex++
			case "skipped-attempt":
				step.Attempt++
			case "wrong-previous":
				step.PreviousStepRunID = "other"
			case "reused-id":
				step.StepRunID = b.failed.StepRunID
			case "same-digest":
				step.AdmissionDigest = b.failed.AdmissionDigest
			case "missing-digest":
				step.AdmissionDigest = ""
			case "bad-digest":
				step.AdmissionDigest = "v1:" + strings.Repeat("z", 64)
			case "expired":
				step.ExpiresUnixMilli = time.Now().Add(-time.Millisecond).UnixMilli()
			case "not-before":
				step.NotBeforeUnixMilli = time.Now().Add(time.Second).UnixMilli()
			case "command":
				cmd = []string{"different"}
			case "environment":
				opts = &ExecuteOptions{Env: map[string]string{"NEW": "value"}}
			case "user":
				opts = &ExecuteOptions{User: "1002:1003"}
			case "directory":
				opts = &ExecuteOptions{WorkingDir: "/other"}
			case "timeout":
				opts = &ExecuteOptions{Timeout: time.Second}
			case "tty":
				opts = &ExecuteOptions{TTY: true}
			case "stdin":
				opts = &ExecuteOptions{Stdin: strings.NewReader("input")}
			}
			c.task.process = newFakeProcess(true)
			var err error
			if which == "ordinary-step" {
				_, _, err = live.ExecuteStep(context.Background(), liveStep(1, 1), cmd, nil)
			} else {
				_, _, err = live.ResumeStep(context.Background(), b, step, cmd, opts)
			}
			if err == nil || c.task.resumes != 0 || c.task.process.waitCalled || c.removed != 1 {
				t.Fatalf("invalid admission reached user code or lacked cleanup: %v", err)
			}
		})
	}
}
func slicesOf(values []string) []string { return append([]string(nil), values...) }

func TestLiveSessionBoundedRepeatedFailures(t *testing.T) {
	live, c, _ := newLiveFixture(t, true)
	b := failLiveStep(t, live, c)
	for attempt := int32(2); attempt <= 5; attempt++ {
		previous := b
		c.task.process = newFakeProcess(true)
		c.task.process.code = 23
		_, next, err := live.ResumeStep(context.Background(), b, liveStep(0, attempt), liveCommand, nil)
		if err != nil || next == nil || next == previous || !previous.consumed {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		b = next
	}
	c.task.process = newFakeProcess(true)
	if _, _, err := live.ResumeStep(context.Background(), b, liveStep(0, 6), liveCommand, nil); err == nil || c.task.resumes != 4 || c.task.process.waitCalled {
		t.Fatal("attempt limit bypassed")
	}
}

func TestLiveSessionPauseResumeUncertaintyAndDescendants(t *testing.T) {
	for _, which := range []string{"pause-ack", "pause-status", "resume-ack", "resume-status", "background", "zombie", "unreaped", "lost-exit", "late-output"} {
		t.Run(which, func(t *testing.T) {
			live, c, pin := newLiveFixture(t, true)
			var b *FailedStepBoundary
			if strings.HasPrefix(which, "resume") {
				b = failLiveStep(t, live, c)
			}
			c.task.process = newFakeProcess(true)
			c.task.process.code = 23
			var opts *ExecuteOptions
			switch which {
			case "pause-ack":
				c.task.pauseHook = func(context.Context) error { c.task.status = containerd.Paused; return errors.New("lost pause ACK") }
			case "pause-status":
				c.task.pauseHook = func(context.Context) error { return nil }
			case "resume-ack":
				c.task.resumeHook = func(context.Context) error { c.task.status = containerd.Running; return errors.New("lost resume ACK") }
			case "resume-status":
				c.task.resumeHook = func(context.Context) error { return nil }
			case "background":
				c.task.pauseHook = func(context.Context) error { c.task.status = containerd.Paused; c.task.background = 1; return nil }
			case "zombie":
				pin.childErr = errors.New("adopted zombie")
			case "unreaped":
				c.task.process.deleteErr = errdefs.ErrNotFound
			case "lost-exit":
				c.task.process.exitErr = errors.New("exit result missing")
			case "late-output":
				c.task.process.deleteOutput = "last log"
				opts = &ExecuteOptions{Stdout: failingWriter{}}
			}
			var next *FailedStepBoundary
			var err error
			if b != nil {
				_, next, err = live.ResumeStep(context.Background(), b, liveStep(0, 2), liveCommand, opts)
			} else {
				_, next, err = live.ExecuteStep(context.Background(), liveStep(0, 1), liveCommand, opts)
			}
			if err == nil || next != nil || !live.proof.invalid || c.removed != 1 || !pin.closed {
				t.Fatalf("uncertain or populated task retained continuation: %v", err)
			}
			if b != nil && (!b.consumed || c.task.process.waitCalled) {
				t.Fatal("uncertain resume preserved permit or started command")
			}
		})
	}
}

func TestLiveSessionRevalidatesTaskResourceAndFrozenProcesses(t *testing.T) {
	for _, which := range []string{"owner", "pid", "pid-reused", "resource", "background", "zombie"} {
		t.Run(which, func(t *testing.T) {
			live, c, pin := newLiveFixture(t, true)
			b := failLiveStep(t, live, c)
			switch which {
			case "owner":
				c.labels[ownerLabel] = "other-owner"
			case "pid":
				c.task.pid++
			case "pid-reused":
				pin.err = errors.New("old kernel process exited")
			case "resource":
				live.state.ref.Owner = "replaced"
			case "background":
				c.task.background = 1
			case "zombie":
				pin.childErr = errors.New("zombie after observation")
			}
			c.task.process = newFakeProcess(true)
			if _, _, err := live.ResumeStep(context.Background(), b, liveStep(0, 2), liveCommand, nil); err == nil || c.task.resumes != 0 || c.task.process.waitCalled {
				t.Fatal("replacement entered Resume")
			}
			if which != "background" && which != "zombie" && (c.removed != 0 || len(c.task.signals) != 0) {
				t.Fatal("replacement was destructively cleaned")
			}
			live.state.ref = live.resource
		})
	}
}

func TestLiveSessionFreshExpiryBeforeEachEffect(t *testing.T) {
	for _, phase := range []string{"resume", "exec", "start"} {
		t.Run(phase, func(t *testing.T) {
			live, c, _ := newLiveFixture(t, true)
			b := failLiveStep(t, live, c)
			step := liveStep(0, 2)
			step.ExpiresUnixMilli = time.Now().Add(35 * time.Millisecond).UnixMilli()
			waitExpiry := func() { time.Sleep(time.Until(time.UnixMilli(step.ExpiresUnixMilli)) + 3*time.Millisecond) }
			c.task.process = newFakeProcess(true)
			switch phase {
			case "resume":
				waitExpiry()
			case "exec":
				c.task.resumeHook = func(context.Context) error { c.task.status = containerd.Running; waitExpiry(); return nil }
			case "start":
				c.task.execHook = func(context.Context) error { waitExpiry(); return nil }
			}
			if _, _, err := live.ResumeStep(context.Background(), b, step, liveCommand, nil); err == nil {
				t.Fatal("expired admission executed")
			}
			select {
			case <-c.task.process.started:
				t.Fatal("command started after admission expiry")
			default:
			}
			if phase == "resume" && c.task.resumes != 0 {
				t.Fatal("expired admission resumed")
			}
			if phase == "exec" && c.task.process.waitCalled {
				t.Fatal("expired admission created exec")
			}
		})
	}
}

func TestLiveSessionAdmissionExpiryDoesNotTruncateStartedCommand(t *testing.T) {
	live, c, _ := newLiveFixture(t, true)
	step := liveStep(0, 1)
	step.ExpiresUnixMilli = time.Now().Add(35 * time.Millisecond).UnixMilli()
	c.task.process = newFakeProcess(true)
	c.task.process.startContextHook = func(ctx context.Context) error {
		time.Sleep(time.Until(time.UnixMilli(step.ExpiresUnixMilli)) + 5*time.Millisecond)
		return ctx.Err()
	}
	out, b, err := live.ExecuteStep(context.Background(), step, liveCommand, nil)
	if err != nil || out.ExitCode != 0 || b != nil {
		t.Fatalf("admission expiry became command timeout: %+v %v", out, err)
	}
}

func TestLiveSessionCancelWinsBeforeAndDuringResume(t *testing.T) {
	for _, phase := range []string{"before", "during", "stopping"} {
		t.Run(phase, func(t *testing.T) {
			live, c, pin := newLiveFixture(t, true)
			b := failLiveStep(t, live, c)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.task.process = newFakeProcess(true)
			switch phase {
			case "before":
				cancel()
			case "stopping":
				live.state.cancelActive()
			case "during":
				c.task.resumeHook = func(context.Context) error { c.task.status = containerd.Running; cancel(); return nil }
			}
			if _, _, err := live.ResumeStep(ctx, b, liveStep(0, 2), liveCommand, nil); err == nil || c.task.process.waitCalled || c.removed != 1 || !pin.closed {
				t.Fatalf("cancel did not fence continuation: %v", err)
			}
			if phase != "during" && c.task.resumes != 0 {
				t.Fatal("cancel lost resume admission")
			}
		})
	}
}

func TestLiveSessionStopCancelsBlockedEffect(t *testing.T) {
	for _, phase := range []string{"resume", "exec", "start"} {
		t.Run(phase, func(t *testing.T) {
			live, c, _ := newLiveFixture(t, true)
			b := failLiveStep(t, live, c)
			entered := make(chan struct{})
			done := make(chan error, 1)
			block := func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }
			c.task.process = newFakeProcess(true)
			switch phase {
			case "resume":
				c.task.resumeHook = block
			case "exec":
				c.task.execHook = block
			case "start":
				c.task.process.startContextHook = block
			}
			safe.Go(func() {
				_, _, err := live.ResumeStep(context.Background(), b, liveStep(0, 2), liveCommand, nil)
				done <- err
			})
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("effect did not start")
			}
			stopped := make(chan error, 1)
			safe.Go(func() { stopped <- live.sandbox.Stop(context.Background(), live.resource.ID, time.Millisecond) })
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("effect not canceled: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Stop blocked on admission mutex")
			}
			select {
			case err := <-stopped:
				if err != nil && !errdefs.IsNotFound(err) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("Stop never finished")
			}
			select {
			case <-c.task.process.started:
				t.Fatal("canceled effect ran user code")
			default:
			}
		})
	}
}

func TestLiveSessionInitialAndSuccessfulStepCannotReplay(t *testing.T) {
	for _, which := range []string{"initial-retry", "replay-success", "backward-index"} {
		t.Run(which, func(t *testing.T) {
			live, c, _ := newLiveFixture(t, true)
			step := liveStep(2, 1)
			if which == "initial-retry" {
				step = liveStep(2, 2)
			} else {
				c.task.process = newFakeProcess(true)
				if _, _, err := live.ExecuteStep(context.Background(), step, liveCommand, nil); err != nil {
					t.Fatal(err)
				}
				if which == "backward-index" {
					step = liveStep(1, 1)
				}
			}
			c.task.process = newFakeProcess(true)
			if _, _, err := live.ExecuteStep(context.Background(), step, liveCommand, nil); err == nil || c.task.process.waitCalled {
				t.Fatal("logical attempt replayed")
			}
		})
	}
}

// Deterministically cancel at acquire's post-send check, rather than rely on
// scheduling a nanosecond-wide race in a regression test.
type cancelOnSecondErr struct {
	context.Context
	cancel context.CancelFunc
	calls  int
}

func (c *cancelOnSecondErr) Err() error {
	c.calls++
	if c.calls == 2 {
		c.cancel()
	}
	return c.Context.Err()
}
func TestLiveSessionAcquireCancellationDoesNotLeakGate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	live := &liveSession{gate: make(chan struct{}, 1)}
	racing := &cancelOnSecondErr{Context: ctx, cancel: cancel}
	if err := live.acquire(racing); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-acquire cancellation not observed: %v", err)
	}
	next, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := live.acquire(next); err != nil {
		t.Fatalf("canceled acquire leaked gate: %v", err)
	}
	live.release()
}

func TestLiveSessionBoundaryCannotCrossSessionOrReconstructAfterRestart(t *testing.T) {
	first, c, _ := newLiveFixture(t, true)
	b := failLiveStep(t, first, c)
	second, d, _ := newLiveFixture(t, true)
	_ = failLiveStep(t, second, d)
	d.task.process = newFakeProcess(true)
	if _, _, err := second.ResumeStep(context.Background(), b, liveStep(0, 2), liveCommand, nil); err == nil || d.task.resumes != 0 {
		t.Fatal("boundary crossed sessions")
	}
	if err := first.CheckBoundary(context.Background(), b); err != nil {
		t.Fatalf("foreign attempt harmed original boundary: %v", err)
	}
	// A new sandbox process has no live handle; ownedState is cleanup-only.
	restarted := newSandbox(first.sandbox.config, first.sandbox.backend)
	state, err := restarted.ownedState(context.Background(), first.Resource())
	if err != nil || !state.stopped || state.liveSession != nil || state.quiescence != nil {
		t.Fatalf("restart restored live authority: %v", err)
	}
	if _, err := restarted.Execute(context.Background(), first.Resource().ID, liveCommand, nil); err == nil {
		t.Fatal("restart resumed from scalar resource")
	}
}

func TestLiveSessionCheckBoundaryRejectsLaterZombie(t *testing.T) {
	live, c, pin := newLiveFixture(t, true)
	b := failLiveStep(t, live, c)
	pin.childErr = errors.New("new adopted zombie")
	if err := live.CheckBoundary(context.Background(), b); err == nil || c.task.resumes != 0 || c.removed != 1 || !pin.closed {
		t.Fatalf("boundary audit did not reject/clean zombie: %v", err)
	}
}

func TestLiveSessionStartCannotAdoptRecreatedResource(t *testing.T) {
	live, c, _ := newLiveFixture(t, false)
	if err := live.sandbox.Remove(context.Background(), live.resource.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := live.sandbox.Create(context.Background(), &CreateOptions{Resource: &live.resource, User: "1000:1001", Command: []string{"uncontrolled"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = live.Start(context.Background()); err == nil {
		t.Fatal("old handle started replacement")
	}
	replacementState, err := live.sandbox.getContainer(replacement)
	if err != nil || replacementState.stopping || replacementState.removed || replacementState.task != nil || c.newTasks != 0 {
		t.Fatalf("stale handle touched replacement: %v", err)
	}
}
