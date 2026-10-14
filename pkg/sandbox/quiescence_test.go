// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/safe"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/errdefs"
)

type fakeInitPin struct {
	err      error
	closed   bool
	task     *fakeTask
	childErr error
}

func (p *fakeInitPin) alive(ctx context.Context) error {
	if p.closed {
		return errors.New("pidfd closed")
	}
	if p.task != nil {
		p.task.mu.Lock()
		stopped := p.task.status == containerd.Stopped
		p.task.mu.Unlock()
		if stopped {
			return errors.New("original init exited")
		}
	}
	return errors.Join(ctx.Err(), p.err)
}
func (p *fakeInitPin) childless(ctx context.Context) error { return errors.Join(ctx.Err(), p.childErr) }
func (p *fakeInitPin) close() error                        { p.closed = true; return nil }

func newQuiescenceFixture(t *testing.T) (*taskQuiescence, *fakeContainer, *fakeInitPin) {
	t.Helper()
	backend := &fakeBackend{containers: make(map[string]*fakeContainer)}
	s := newSandbox(ContainerdConfig{Namespace: "arcentra-test", DefaultImage: "approved:test"}, backend)
	pin := &fakeInitPin{}
	q, err := newTaskQuiescence(context.Background(), s, CreateOptions{User: "1000:1000"}, func(_ context.Context, pid uint32) (pinnedInit, error) {
		if pid != 1234 {
			t.Fatalf("pinning wrong init: %d", pid)
		}
		return pin, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c := backend.lookup(q.state.ref.ID)
	pin.task = c.task
	t.Cleanup(func() {
		// Some negative tests deliberately change ownership or identity.
		c.labels = ownershipLabels(q.state.ref)
		c.task.pid = q.pid
		c.newTasks = 1
		c.task.pidsErr = nil
		if c.task.exit == nil && c.task.status == containerd.Stopped {
			c.task.status = containerd.Paused
		}
		pin.err = nil
		if c.task.process != nil {
			c.task.process.deleteErr = nil
		}
		// Test-only fake resource teardown after deliberately lost attribution;
		// production must retain the quarantined record instead.
		q.state.quiescence = nil
		_ = pin.close()
		if err := s.Close(); err != nil {
			t.Errorf("fixture cleanup: %v", err)
		}
	})
	return q, c, pin
}

func failQuiescenceExec(t *testing.T, q *taskQuiescence, c *fakeContainer) {
	t.Helper()
	c.task.process = newFakeProcess(true)
	c.task.process.code = 23
	result, err := q.execute(context.Background(), []string{"sh", "-c", "exit 23"}, nil)
	if err != nil || result.ExitCode != 23 {
		t.Fatalf("clean failed execution: %+v %v", result, err)
	}
}

func TestQuiescenceKeepsExactInitAndResumesOnlyPrivateAdmission(t *testing.T) {
	q, c, pin := newQuiescenceFixture(t)
	if strings.Join(c.base.Process.Args, " ") != "/bin/sleep 2147483647" {
		t.Fatalf("uncontrolled init: %v", c.base.Process.Args)
	}
	failQuiescenceExec(t, q, c)
	if !q.paused || q.invalid || c.task.status != containerd.Paused || len(q.state.processes) != 0 || c.task.process.deleted != 1 {
		t.Fatal("failed exec was not reaped and frozen")
	}
	if pin.closed || c.removed != 0 || len(c.task.signals) != 0 {
		t.Fatal("proof discarded the original resource")
	}
	if _, err := q.sandbox.Execute(context.Background(), q.state.ref.ID, []string{"true"}, nil); err == nil {
		t.Fatal("public Execute bypassed private admission")
	}
	c.task.process = newFakeProcess(true)
	result, err := q.execute(context.Background(), []string{"true"}, nil)
	if err != nil || result.ExitCode != 0 || q.paused || q.invalid || c.task.resumes != 1 {
		t.Fatalf("continuation did not preserve task: %+v %v", result, err)
	}
	if err := q.sandbox.RemoveOwned(context.Background(), q.state.ref); err != nil || !pin.closed {
		t.Fatalf("cleanup did not close identity pin: %v", err)
	}
}

func TestQuiescenceRejectsUnreapedFailedExecution(t *testing.T) {
	for _, deleteErr := range []error{errors.New("delete uncertain"), errdefs.ErrNotFound} {
		t.Run(deleteErr.Error(), func(t *testing.T) {
			q, c, _ := newQuiescenceFixture(t)
			c.task.process = newFakeProcess(true)
			c.task.process.code, c.task.process.deleteErr = 23, deleteErr
			_, err := q.execute(context.Background(), []string{"false"}, nil)
			if err == nil || !q.invalid || c.task.pauses != 0 || c.task.resumes != 0 {
				t.Fatalf("unconfirmed delete admitted continuation: %v", err)
			}
		})
	}
}

func TestQuiescenceRequiresObservedExit(t *testing.T) {
	q, c, _ := newQuiescenceFixture(t)
	c.task.process = newFakeProcess(true)
	c.task.process.code, c.task.process.exitErr = 23, errors.New("lost exit result")
	_, err := q.execute(context.Background(), []string{"false"}, nil)
	if err == nil || !q.invalid || c.task.pauses != 0 || c.task.resumes != 0 {
		t.Fatalf("missing exit was treated as a failed-step proof: %v", err)
	}
}

func TestQuiescenceRejectsOwnedTaskAndPIDReplacement(t *testing.T) {
	for _, which := range []string{"foreign-owner", "new-task-pid", "same-pid-reused", "stopped-pid-reused"} {
		t.Run(which, func(t *testing.T) {
			q, c, pin := newQuiescenceFixture(t)
			failQuiescenceExec(t, q, c)
			switch which {
			case "foreign-owner":
				c.labels[ownerLabel] = "other-owner"
			case "new-task-pid":
				c.task.pid++
			case "stopped-pid-reused":
				c.task.status = containerd.Stopped
				pin.err = errors.New("original process exited, stopped replacement reused its PID")
			case "same-pid-reused":
				pin.err = errors.New("original process exited, PID was reused")
			}
			_, err := q.execute(context.Background(), []string{"true"}, nil)
			if err == nil || !q.invalid || c.task.resumes != 0 || c.removed != 0 || len(c.task.signals) != 0 {
				t.Fatalf("replacement was resumed or deleted: %v", err)
			}
		})
	}
}

func TestQuiescenceFreezeRejectsForkRacingWithExit(t *testing.T) {
	for _, background := range []int{1, 5} {
		q, c, pin := newQuiescenceFixture(t)
		c.task.pauseHook = func(context.Context) error {
			c.task.status = containerd.Paused
			c.task.background = background // A fork immediately before freeze completes.
			return nil
		}
		c.task.process = newFakeProcess(true)
		c.task.process.code = 23
		_, err := q.execute(context.Background(), []string{"false"}, nil)
		if err == nil || !q.invalid || c.task.pauses != 1 || c.task.resumes != 0 || c.removed != 1 || !pin.closed {
			t.Fatalf("background processes accepted: %v", err)
		}
		if !c.task.killAll || c.task.background != 0 {
			t.Fatal("rejected task descendants were not cleaned up")
		}
	}
}

func TestQuiescencePauseResumeUncertaintyFailsClosed(t *testing.T) {
	for _, phase := range []string{"pause-error", "pause-wrong-status", "resume-error", "resume-wrong-status", "pids-unavailable"} {
		t.Run(phase, func(t *testing.T) {
			q, c, pin := newQuiescenceFixture(t)
			if strings.HasPrefix(phase, "resume") {
				failQuiescenceExec(t, q, c)
				c.task.resumeHook = func(context.Context) error {
					if phase == "resume-error" {
						c.task.status = containerd.Running // RPC side effect may already have happened.
						return errors.New("resume reply lost")
					}
					return nil // Success without Running is not sufficient.
				}
			} else {
				c.task.pauseHook = func(context.Context) error {
					if phase == "pause-wrong-status" {
						return nil
					}
					c.task.status = containerd.Paused
					if phase == "pids-unavailable" {
						c.task.pidsErr = errors.New("cannot inspect frozen cgroup")
						return nil
					}
					return errors.New("pause reply lost")
				}
			}
			c.task.process = newFakeProcess(true)
			c.task.process.code = 23
			_, err := q.execute(context.Background(), []string{"false"}, nil)
			if err == nil || !q.invalid {
				t.Fatalf("uncertainty did not invalidate proof: %v", err)
			}
			if strings.HasPrefix(phase, "resume") && c.task.process.waitCalled {
				t.Fatal("user exec started after uncertain resume")
			}
			if phase != "pids-unavailable" && (c.removed != 1 || !pin.closed) {
				t.Fatal("uncertain task was not cleaned up")
			}
			if phase == "pids-unavailable" && (c.removed != 0 || pin.closed) {
				t.Fatal("unconfirmed cleanup discarded ownership state")
			}
		})
	}
}

func TestQuiescenceCancelWinsBeforeDuringAndAfterFreeze(t *testing.T) {
	for _, phase := range []string{"before-pause", "during-pause", "after-pause", "during-resume", "explicit-stop"} {
		t.Run(phase, func(t *testing.T) {
			q, c, pin := newQuiescenceFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch phase {
			case "before-pause":
				cancel()
			case "during-pause":
				c.task.pauseHook = func(context.Context) error {
					c.task.status = containerd.Paused
					cancel()
					return nil
				}
			case "after-pause", "during-resume", "explicit-stop":
				failQuiescenceExec(t, q, c)
				if phase == "after-pause" {
					cancel()
				} else if phase == "explicit-stop" {
					q.state.cancelActive()
				} else {
					c.task.resumeHook = func(context.Context) error {
						c.task.status = containerd.Running
						cancel()
						return nil
					}
				}
			}
			c.task.process = newFakeProcess(true)
			c.task.process.code = 23
			_, err := q.execute(ctx, []string{"false"}, nil)
			if err == nil || c.removed != 1 || !pin.closed {
				t.Fatalf("canceled task was not cleaned up: %v", err)
			}
			if phase != "during-resume" && c.task.resumes != 0 {
				t.Fatal("cancel lost admission to resume")
			}
			if phase != "during-pause" && c.task.process.waitCalled {
				t.Fatal("command started after cancellation")
			}
		})
	}
}

func TestQuiescenceRestartCannotResume(t *testing.T) {
	q, c, _ := newQuiescenceFixture(t)
	failQuiescenceExec(t, q, c)
	// Simulate process death: the old in-memory permit and pidfd are gone.
	_ = q.pin.close()
	delete(q.sandbox.containers, q.state.ref.ID)
	restarted := newSandbox(q.sandbox.config, q.sandbox.backend)
	state, err := restarted.ownedState(context.Background(), q.state.ref)
	if err != nil || !state.stopped || state.quiescence != nil {
		t.Fatalf("restart restored executable proof: %+v %v", state, err)
	}
	if _, err = restarted.Execute(context.Background(), q.state.ref.ID, []string{"true"}, nil); err == nil {
		t.Fatal("restarted process executed old task")
	}
	if err = restarted.RemoveOwned(context.Background(), q.state.ref); err != nil || c.task.resumes != 0 {
		t.Fatalf("restart cleanup tried to resume: %v", err)
	}
}

func TestQuiescenceUnavailablePinFailsClosed(t *testing.T) {
	backend := &fakeBackend{containers: make(map[string]*fakeContainer)}
	s := newSandbox(ContainerdConfig{Namespace: "arcentra-test", DefaultImage: "approved:test"}, backend)
	_, err := newTaskQuiescence(context.Background(), s, CreateOptions{User: "1000:1000"}, func(context.Context, uint32) (pinnedInit, error) {
		return nil, errors.New("pidfd unavailable")
	})
	if err == nil {
		t.Fatal("missing pidfd capability silently downgraded")
	}
	for _, c := range backend.containers {
		if c.removed != 1 || c.task.resumes != 0 {
			t.Fatal("unsupported probe was not cleaned up")
		}
	}
}

func TestQuiescenceFinalSinkFailureCannotBecomeProof(t *testing.T) {
	q, c, pin := newQuiescenceFixture(t)
	c.task.process = newFakeProcess(true)
	c.task.process.code = 23
	// Output appears only while Delete drains the final runtime IO.
	c.task.process.deleteOutput = "last log line"
	_, err := q.execute(context.Background(), []string{"false"}, &ExecuteOptions{Stdout: failingWriter{}})
	if err == nil || !strings.Contains(err.Error(), "log sink unavailable") || !q.invalid || c.task.pauses != 0 || c.removed != 1 || !pin.closed {
		t.Fatalf("late log failure was mistaken for a continuable failed exit: %v", err)
	}
}

func TestQuiescenceStopCancelsBlockedRuntimeRPC(t *testing.T) {
	for _, phase := range []string{"resume", "exec", "start"} {
		t.Run(phase, func(t *testing.T) {
			q, c, pin := newQuiescenceFixture(t)
			failQuiescenceExec(t, q, c)
			entered := make(chan struct{})
			block := func(ctx context.Context) error {
				close(entered)
				<-ctx.Done()
				return ctx.Err()
			}
			c.task.process = newFakeProcess(true)
			switch phase {
			case "resume":
				c.task.resumeHook = block
			case "exec":
				c.task.execHook = block
			case "start":
				c.task.process.startContextHook = block
			}
			done := make(chan error, 1)
			safe.Go(func() { _, err := q.execute(context.Background(), []string{"true"}, nil); done <- err })
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("runtime RPC was never entered")
			}
			stopped := make(chan error, 1)
			safe.Go(func() { stopped <- q.sandbox.Stop(context.Background(), q.state.ref.ID, time.Millisecond) })
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("blocked RPC did not receive cancellation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Stop could not cancel blocked runtime RPC")
			}
			select {
			case err := <-stopped:
				if err != nil && !errdefs.IsNotFound(err) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("Stop did not finish")
			}
			if (phase != "start" && c.task.process.waitCalled) || c.removed != 1 || !pin.closed {
				t.Fatal("canceled RPC started user code or lost cleanup")
			}
			select {
			case <-c.task.process.started:
				t.Fatal("canceled Start actually ran user code")
			default:
			}
		})
	}
}

func TestQuiescenceRejectsRootInit(t *testing.T) {
	for _, user := range []string{"", "0", "0:1000", "1000:0", "named"} {
		backend := &fakeBackend{containers: make(map[string]*fakeContainer)}
		s := newSandbox(ContainerdConfig{Namespace: "arcentra-test", DefaultImage: "approved:test"}, backend)
		if _, err := newTaskQuiescence(context.Background(), s, CreateOptions{User: user}, nil); err == nil || len(backend.containers) != 0 {
			t.Fatalf("invalid init user %q reached runtime: %v", user, err)
		}
	}
}

func TestQuiescenceUncertainTaskDeleteQuarantinesDeadIdentity(t *testing.T) {
	q, c, pin := newQuiescenceFixture(t)
	failQuiescenceExec(t, q, c)
	c.task.deleteErr = errors.New("task delete reply lost")
	if err := q.sandbox.RemoveOwned(context.Background(), q.state.ref); err == nil || !q.emptyConfirmed || q.taskDeleted {
		t.Fatalf("uncertain task deletion was accepted: %v", err)
	}
	c.task.deleteErr = nil
	if err := q.sandbox.RemoveOwned(context.Background(), q.state.ref); err == nil || c.removed != 0 || pin.closed {
		t.Fatalf("dead identity authorized a new deletion attempt: %v", err)
	}
}

func TestQuiescenceStaleProbeCannotRemoveRecreatedResource(t *testing.T) {
	q, _, _ := newQuiescenceFixture(t)
	if err := q.sandbox.RemoveOwned(context.Background(), q.state.ref); err != nil {
		t.Fatal(err)
	}
	next, err := newTaskQuiescence(context.Background(), q.sandbox, CreateOptions{Resource: &q.state.ref, User: "1000:1000"}, func(context.Context, uint32) (pinnedInit, error) { return &fakeInitPin{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := q.sandbox.RemoveOwned(context.Background(), next.state.ref); err != nil {
			t.Error(err)
		}
	}()
	if _, err = q.execute(context.Background(), []string{"true"}, nil); err == nil {
		t.Fatal("stale probe ran against recreated resource")
	}
	if next.state.removed || next.invalid || next.state.stopping {
		t.Fatal("stale probe cleaned up the replacement generation")
	}
	if _, err = next.execute(context.Background(), []string{"true"}, nil); err != nil {
		t.Fatalf("replacement was damaged: %v", err)
	}
}

func TestQuiescenceMissingTaskMetadataCannotProveProcessAbsence(t *testing.T) {
	for _, initAlive := range []bool{true, false} {
		q, c, pin := newQuiescenceFixture(t)
		c.newTasks = 0 // Runtime task lookup loses metadata, not necessarily processes.
		if !initAlive {
			pin.err = errors.New("original init exited but descendants may remain")
		}
		err := q.sandbox.RemoveOwned(context.Background(), q.state.ref)
		if err == nil || c.removed != 0 || pin.closed || len(c.task.signals) != 0 {
			t.Fatalf("missing metadata discarded unconfirmed process ownership: alive=%v err=%v", initAlive, err)
		}
	}
}

func TestQuiescencePartialRemovalRequiresConfirmedWholeTaskStop(t *testing.T) {
	q, c, pin := newQuiescenceFixture(t)
	failQuiescenceExec(t, q, c)
	c.removeErr = errors.New("snapshot removal unavailable")
	if err := q.sandbox.RemoveOwned(context.Background(), q.state.ref); err == nil || !q.emptyConfirmed || !q.taskDeleted || pin.closed {
		t.Fatalf("partial removal discarded cleanup evidence: %v", err)
	}
	c.removeErr = nil
	if err := q.sandbox.RemoveOwned(context.Background(), q.state.ref); err != nil || c.removed != 1 || !pin.closed {
		t.Fatalf("confirmed stopped task was not cleaned on retry: %v", err)
	}
}

func TestQuiescenceRejectsZombieOrInvisibleChildren(t *testing.T) {
	for _, cause := range []error{errors.New("adopted zombie child remains"), errors.New("owned proc children unavailable")} {
		q, c, pin := newQuiescenceFixture(t)
		pin.childErr = cause
		c.task.process = newFakeProcess(true)
		c.task.process.code = 23
		_, err := q.execute(context.Background(), []string{"false"}, nil)
		if !errors.Is(err, cause) || !q.invalid || c.task.pauses != 1 || c.task.resumes != 0 || c.removed != 1 || !pin.closed {
			t.Fatalf("child guard did not reject and clean task: %v", err)
		}
	}
}

func TestQuiescenceConstructorCancellationCannotPinRemovedTask(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	backend := &fakeBackend{containers: make(map[string]*fakeContainer), taskStartHook: func() { close(started); <-release }}
	s := newSandbox(ContainerdConfig{Namespace: "arcentra-test", DefaultImage: "approved:test"}, backend)
	done := make(chan error, 1)
	pinCalled := false
	safe.Go(func() {
		_, err := newTaskQuiescence(context.Background(), s, CreateOptions{User: "1000:1000"}, func(context.Context, uint32) (pinnedInit, error) { pinCalled = true; return &fakeInitPin{}, nil })
		done <- err
	})
	<-started
	s.mu.RLock()
	var state *containerState
	for _, candidate := range s.containers {
		state = candidate
	}
	s.mu.RUnlock()
	state.cancelActive()
	removed := make(chan error, 1)
	safe.Go(func() { removed <- s.removeState(context.Background(), state.ref.ID, state) })
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("constructor accepted a stopping task")
		}
	case <-time.After(time.Second):
		t.Fatal("constructor blocked")
	}
	if pinCalled {
		t.Fatal("constructor pinned task after cancellation")
	}
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestQuiescenceCreateRetainsExactGenerationBeforeStart(t *testing.T) {
	backend := &fakeBackend{containers: make(map[string]*fakeContainer)}
	s := newSandbox(ContainerdConfig{Namespace: "arcentra-test", DefaultImage: "approved:test"}, backend)
	id, original, err := s.create(context.Background(), &CreateOptions{Command: quiescenceInit, User: "1000:1000"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.removeState(context.Background(), id, original); err != nil {
		t.Fatal(err)
	}
	_, replacement, err := s.create(context.Background(), &CreateOptions{Resource: &original.ref, Command: []string{"sh", "-c", "uncontrolled init"}, User: "1000:1000"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.removeState(context.Background(), id, replacement); err != nil {
			t.Error(err)
		}
	}()
	if original == replacement {
		t.Fatal("create did not return distinct generation handles")
	}
	if err = s.startState(context.Background(), original); err == nil {
		t.Fatal("old constructor started replacement's uncontrolled init")
	}
	if backend.lookup(id).newTasks != 0 {
		t.Fatal("stale generation touched replacement task")
	}
}
