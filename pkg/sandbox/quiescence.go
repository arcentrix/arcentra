// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
)

// The original private capability probe remains separate from the default-off
// live-session adapter. Neither path has a durable capability: a new process
// can clean up, but cannot reconstruct the pinned init or resume the task.
// The approved image must provide this non-forking, single-process init.
var quiescenceInit = []string{"/bin/sleep", "2147483647"}

type pinnedInit interface {
	alive(context.Context) error
	childless(context.Context) error
	close() error
}

type taskQuiescence struct {
	sandbox        *Containerd
	state          *containerState
	pid            uint32
	pin            pinnedInit
	paused         bool
	invalid        bool
	emptyConfirmed bool
	taskDeleted    bool
}

// A private constructor deliberately keeps the experiment unavailable to
// production callers. Tests supply an injectable kernel identity boundary.
func newTaskQuiescence(ctx context.Context, s *Containerd, opts CreateOptions, pin func(context.Context, uint32) (pinnedInit, error)) (_ *taskQuiescence, resultErr error) {
	user, userErr := numericUser(opts.User)
	if userErr != nil || user.UID == 0 || user.GID == 0 {
		return nil, fmt.Errorf("quiescence probe requires an explicit nonzero numeric UID and GID")
	}
	opts.Command, opts.Args = slices.Clone(quiescenceInit), nil
	id, state, err := s.create(ctx, &opts)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, s.removeState(ctx, id, state))
		}
	}()
	if err = s.startState(ctx, state); err != nil {
		return nil, err
	}
	ctx = namespaces.WithNamespace(ctx, s.namespace)
	if err = state.acquire(ctx); err != nil {
		return nil, err
	}
	defer state.release()
	return initializeQuiescence(ctx, s, state, pin)
}

// Called with the exact state lifecycle gate held, after controlled init starts.
func initializeQuiescence(ctx context.Context, s *Containerd, state *containerState, pin func(context.Context, uint32) (pinnedInit, error)) (*taskQuiescence, error) {
	state.mu.Lock()
	stopping := state.stopping
	state.mu.Unlock()
	if state.removed || state.stopped || stopping || state.task == nil {
		return nil, fmt.Errorf("quiescence initialization was canceled or removed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q := &taskQuiescence{sandbox: s, state: state, pid: state.task.Pid()}
	if q.pid == 0 {
		return nil, fmt.Errorf("init task has no kernel PID")
	}
	if pin == nil {
		return nil, fmt.Errorf("kernel init pin is required")
	}
	var err error
	q.pin, err = pin(ctx, q.pid)
	if err != nil || q.pin == nil {
		return nil, fmt.Errorf("pin exact init process: %w", errors.Join(err, fmt.Errorf("kernel init pin unavailable")))
	}
	state.quiescence = q
	if err = q.verify(ctx, containerd.Running, true); err != nil {
		q.invalid = true
		return nil, err
	}
	if err = q.admit(ctx); err != nil {
		q.invalid = true
		return nil, err
	}
	return q, nil
}

func (q *taskQuiescence) execute(ctx context.Context, cmd []string, opts *ExecuteOptions) (*ExecuteResult, error) {
	result, err := q.sandbox.execute(ctx, q.state.ref.ID, cmd, opts, q, nil)
	if err != nil {
		// Execute has released the lifecycle gate. Removal uses a separate,
		// bounded cleanup context and verifies ownership again, even on cancel.
		err = errors.Join(err, q.sandbox.removeState(ctx, q.state.ref.ID, q.state))
	}
	return result, err
}

// All fields below are accessed under the container lifecycle gate. A fresh
// runtime handle is essential: Task.Pid alone is cached by the SDK.
func (q *taskQuiescence) verify(ctx context.Context, expected containerd.ProcessStatus, onlyInit bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	container, resources, err := q.sandbox.backend.inspectOwned(ctx, q.state.ref)
	if err != nil {
		return fmt.Errorf("verify quiescence ownership: %w", err)
	}
	if !resources.ContainerExists || !resources.SnapshotExists || !resources.TaskExists {
		return fmt.Errorf("quiescence requires the original container, snapshot and task")
	}
	fresh, err := container.loadTask(ctx)
	if err != nil {
		return fmt.Errorf("reload exact quiescence task: %w", err)
	}
	if fresh.Pid() != q.pid || q.state.task.Pid() != q.pid {
		return fmt.Errorf("quiescence init task identity changed")
	}
	if err = q.pin.alive(ctx); err != nil {
		return fmt.Errorf("quiescence pinned init is no longer alive: %w", err)
	}
	status, err := fresh.Status(ctx)
	if err != nil {
		return fmt.Errorf("inspect quiescence task status: %w", err)
	}
	if status.Status != expected {
		return fmt.Errorf("quiescence task is %s, expected %s", status.Status, expected)
	}
	if onlyInit {
		pids, err := fresh.Pids(ctx)
		if err != nil {
			return fmt.Errorf("inspect frozen task processes: %w", err)
		}
		// No process-name or count-only whitelist. The complete runtime list
		// must contain exactly the kernel-pinned init. Zombie checks follow.
		if len(pids) != 1 || pids[0].Pid != q.pid {
			return fmt.Errorf("quiescence task contains processes other than its pinned init")
		}
		// cgroup.procs omits zombies. Check only the pinned init's kernel
		// child lists as well, including adopted descendants of reaped execs.
		if err = q.pin.childless(ctx); err != nil {
			return err
		}
		status, err = fresh.Status(ctx)
		if err != nil || status.Status != expected {
			return errors.Join(fmt.Errorf("task changed during descendant verification"), err)
		}
		if err = q.pin.alive(ctx); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (q *taskQuiescence) beforeExecute(ctx context.Context, admission *liveExecution) error {
	if q.invalid {
		return fmt.Errorf("quiescence proof is permanently invalid")
	}
	if q.state.liveSession != nil {
		if err := q.state.liveSession.admitStep(admission); err != nil {
			q.invalid = true
			return err
		}
	}
	if !q.paused {
		return q.verify(ctx, containerd.Running, false)
	}
	if err := q.verify(ctx, containerd.Paused, true); err != nil {
		q.invalid = true
		return err
	}
	// Stop/Remove cancellation and Resume share one admission boundary.
	// Cancellation already admitted here wins. Cancellation during the RPC
	// is checked again before any continuation exec may be created.
	if err := q.admitExecution(ctx, admission); err != nil {
		q.invalid = true
		return err
	}
	if err := q.state.task.Resume(ctx); err != nil {
		q.invalid = true
		return fmt.Errorf("resume exact quiescence task: %w", normalizeExecutionContextError(err))
	}
	q.paused = false
	if err := q.verify(ctx, containerd.Running, true); err != nil {
		q.invalid = true
		return err
	}
	return nil
}

// Admission is the local linearization point, not the duration of the RPC.
// Never hold the cancellation mutex across daemon IO: Stop must be able to
// cancel an already-admitted Resume/Exec/Start whose remote effect is uncertain.
func (q *taskQuiescence) admit(ctx context.Context) error {
	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if q.state.stopping {
		return fmt.Errorf("quiescence task is stopping")
	}
	return nil
}

func (q *taskQuiescence) admitExecution(ctx context.Context, admission *liveExecution) error {
	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if q.state.stopping {
		return fmt.Errorf("quiescence task is stopping")
	}
	if q.state.liveSession != nil {
		if admission == nil || admission.session != q.state.liveSession {
			return fmt.Errorf("live execution admission is missing")
		}
		return checkLiveStepTime(admission.step)
	}
	return nil
}

func (q *taskQuiescence) afterExecute(ctx context.Context, result *ExecuteResult, cause error, exitObserved, execDeleted bool) error {
	cause = errors.Join(cause, ctx.Err())
	if cause != nil {
		q.invalid = true
		return cause
	}
	// This hook runs after Wait observed the failed exit and process.Delete
	// confirmed reaping. A failed delete always invalidates the whole probe.
	if !exitObserved || !execDeleted || result.ExitCode < 0 || len(q.state.processes) != 0 {
		q.invalid = true
		return fmt.Errorf("failed execution has not been fully reaped")
	}
	if result.ExitCode == 0 {
		return nil
	}
	if err := q.verify(ctx, containerd.Running, false); err != nil {
		q.invalid = true
		return err
	}
	// Pause freezes the whole owned task before inspecting descendants. A
	// background fork racing with exit cannot be mistaken for a clean task.
	q.paused = true // Failure may mean the daemon paused but lost its reply.
	if err := q.state.task.Pause(ctx); err != nil {
		q.invalid = true
		return fmt.Errorf("pause exact quiescence task: %w", normalizeExecutionContextError(err))
	}
	if err := q.verify(ctx, containerd.Paused, true); err != nil {
		q.invalid = true
		return err
	}
	return nil
}

// Cleanup admission rejects observed replacements even when labels match.
// A live identity is required until this attempt has confirmed task deletion.
func (q *taskQuiescence) verifyCleanupTask(ctx context.Context, container runtimeContainer, resources ResourceState) error {
	q.invalid = true
	if err := ctx.Err(); err != nil {
		return err
	}
	if !resources.TaskExists {
		if err := q.pin.alive(ctx); err == nil {
			return fmt.Errorf("task metadata vanished while pinned init is still alive")
		}
		if !q.taskDeleted || !q.emptyConfirmed {
			return fmt.Errorf("task metadata vanished without confirmed owned-process cleanup")
		}
		return nil
	}
	fresh, err := container.loadTask(ctx)
	if err != nil {
		return err
	}
	if fresh.Pid() != q.pid {
		return fmt.Errorf("refuse cleanup of replaced quiescence task")
	}
	if err = q.pin.alive(ctx); err != nil {
		// The SDK has no task-generation CAS. Neither a stopped numeric PID
		// nor its asynchronous Wait response attributes a replacement safely.
		return fmt.Errorf("cannot attribute stopped quiescence task; retain cleanup evidence: %w", err)
	}
	return nil
}

// The private experiment assumes an exclusive, cooperating runtime owner.
// containerd RPCs do not accept a pidfd/task-generation precondition; a local
// gate cannot fence another privileged daemon client between Stop and Delete.
// Cleanup retries after loss of the live pin deliberately remain quarantined.
func stopStateTask(ctx context.Context, state *containerState, grace time.Duration) error {
	if state.quiescence == nil {
		return stopTask(ctx, state.task, grace)
	}
	err := stopTask(ctx, state.task, grace)
	if err == nil {
		state.quiescence.emptyConfirmed = true
	}
	return err
}
