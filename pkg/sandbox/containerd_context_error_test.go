// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/containerd/containerd"
	"github.com/opencontainers/runtime-spec/specs-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type executionSpecErrorContainer struct {
	runtimeContainer
	err    error
	cancel context.CancelFunc
}

func (c executionSpecErrorContainer) spec(context.Context) (*specs.Spec, error) {
	if c.cancel != nil {
		c.cancel()
	}
	return nil, c.err
}

func TestExecuteClassifiesContextFailuresAtRuntimeBoundaries(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"context-deadline", context.DeadlineExceeded, context.DeadlineExceeded},
		{"context-canceled", context.Canceled, context.Canceled},
		{"wrapped-context-deadline", fmt.Errorf("transport: %w", context.DeadlineExceeded), context.DeadlineExceeded},
		{"wrapped-context-canceled", fmt.Errorf("transport: %w", context.Canceled), context.Canceled},
		{"rpc-deadline", status.Error(codes.DeadlineExceeded, "RST_STREAM CANCEL"), context.DeadlineExceeded},
		{"rpc-canceled", status.Error(codes.Canceled, "request canceled"), context.Canceled},
		{"wrapped-rpc-deadline", fmt.Errorf("transport: %w", status.Error(codes.DeadlineExceeded, "deadline reached")), context.DeadlineExceeded},
		{"wrapped-rpc-canceled", fmt.Errorf("transport: %w", status.Error(codes.Canceled, "request canceled")), context.Canceled},
		{"rpc-unavailable", status.Error(codes.Unavailable, "transport unavailable"), nil},
		{"rpc-permission-denied", status.Error(codes.PermissionDenied, "context deadline exceeded"), nil},
		{"rpc-internal", status.Error(codes.Internal, "context deadline exceeded"), nil},
		{"rpc-unknown", status.Error(codes.Unknown, "context canceled"), nil},
		{"rpc-resource-exhausted", status.Error(codes.ResourceExhausted, "busy"), nil},
		{"ordinary-error", errors.New("context deadline exceeded; request canceled"), nil},
	}
	for _, boundary := range []string{"spec", "exec", "wait", "start", "exit"} {
		for _, test := range cases {
			t.Run(boundary+"/"+test.name, func(t *testing.T) {
				sb, backend, id := newTestSandbox(t)
				container := backend.lookup(id)
				task := container.task
				task.process = newFakeProcess(true)
				if err := sb.Start(context.Background(), id); err != nil {
					t.Fatal(err)
				}
				state, err := sb.getContainer(id)
				if err != nil {
					t.Fatal(err)
				}
				switch boundary {
				case "spec":
					state.container = executionSpecErrorContainer{runtimeContainer: state.container, err: test.err}
				case "exec":
					task.execErr = test.err
				case "wait":
					task.process.waitErr = test.err
				case "start":
					task.process.startErr = test.err
				case "exit":
					task.process.exitErr = test.err
				}
				// An RPC can report its context failure while the local execution
				// context is still live. No timer or scheduling race is needed.
				result, err := sb.Execute(context.Background(), id, []string{"true"}, &ExecuteOptions{Timeout: time.Hour})
				if !errors.Is(err, test.err) {
					t.Fatalf("original runtime error lost: %v", err)
				}
				if status.Code(err) != status.Code(test.err) {
					t.Fatalf("original RPC status lost: %v", err)
				}
				for _, classification := range []error{context.DeadlineExceeded, context.Canceled} {
					if errors.Is(err, classification) != (test.want == classification) {
						t.Fatalf("incorrect %v classification for %v: %v", classification, test.err, err)
					}
				}
				if result.ExitCode != -1 {
					t.Fatalf("runtime failure reported an exit code: %+v", result)
				}
				if task.killAll != (boundary != "spec") || state.stopped != (boundary != "spec") {
					t.Fatalf("normalization changed lifecycle cleanup: killAll=%v stopped=%v", task.killAll, state.stopped)
				}
				if boundary == "wait" || boundary == "start" || boundary == "exit" {
					if task.process.deleted != 1 || len(state.processes) != 0 {
						t.Fatal("failed execution was not reaped")
					}
				}
				if err := sb.Remove(context.Background(), id); err != nil {
					t.Fatalf("failed execution was not removable: %v", err)
				}
			})
		}
	}
}

func TestExecuteCleanupRPCFailureDoesNotReclassifySuccessfulExecution(t *testing.T) {
	for _, code := range []codes.Code{codes.DeadlineExceeded, codes.Canceled, codes.Unavailable} {
		t.Run(code.String(), func(t *testing.T) {
			sb, backend, id := newTestSandbox(t)
			task := backend.lookup(id).task
			task.process = newFakeProcess(true)
			cleanupErr := status.Error(code, "delete process failed")
			task.process.deleteErr = cleanupErr
			if err := sb.Start(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			result, err := sb.Execute(context.Background(), id, []string{"true"}, nil)
			if !errors.Is(err, cleanupErr) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || result.ExitCode != 0 {
				t.Fatalf("cleanup reclassified execution or lost its error: %+v %v", result, err)
			}
			state, stateErr := sb.getContainer(id)
			if stateErr != nil || len(state.processes) != 1 || task.killAll {
				t.Fatalf("cleanup failure lost its retry state: %v", stateErr)
			}
			task.process.deleteErr = nil
			if err := sb.Remove(context.Background(), id); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type executionCancelOnDeleteProcess struct {
	runtimeProcess
	cancel context.CancelFunc
}

func (p executionCancelOnDeleteProcess) Delete(ctx context.Context, opts ...containerd.ProcessDeleteOpts) (*containerd.ExitStatus, error) {
	p.cancel()
	return p.runtimeProcess.Delete(ctx, opts...)
}

type executionCancelOnDeleteTask struct {
	runtimeTask
	cancel context.CancelFunc
}

func (task executionCancelOnDeleteTask) exec(ctx context.Context, id string, spec *specs.Process, stdin io.Reader, stdout, stderr io.Writer) (runtimeProcess, error) {
	process, err := task.runtimeTask.exec(ctx, id, spec, stdin, stdout, stderr)
	if err != nil {
		return nil, err
	}
	return executionCancelOnDeleteProcess{runtimeProcess: process, cancel: task.cancel}, nil
}

func TestExecuteLateCancellationDoesNotReclassifySuccessfulExecution(t *testing.T) {
	sb, _, id := newTestSandbox(t)
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state, _ := sb.getContainer(id)
	state.task = executionCancelOnDeleteTask{runtimeTask: state.task, cancel: cancel}
	result, err := sb.Execute(ctx, id, []string{"true"}, nil)
	if err != nil || result.ExitCode != 0 || ctx.Err() != context.Canceled {
		t.Fatalf("late cleanup cancellation reclassified success: %+v %v", result, err)
	}
	if err := sb.Remove(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteUnrelatedRPCFailureIsNotReclassifiedByCanceledContext(t *testing.T) {
	sb, _, id := newTestSandbox(t)
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rpcErr := status.Error(codes.Internal, "context deadline exceeded")
	state, _ := sb.getContainer(id)
	state.container = executionSpecErrorContainer{runtimeContainer: state.container, err: rpcErr, cancel: cancel}
	_, err := sb.Execute(ctx, id, []string{"true"}, nil)
	if !errors.Is(err, rpcErr) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unrelated RPC failure was reclassified: %v", err)
	}
	if err := sb.Remove(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteContextRPCAndCleanupFailuresRetainEveryCauseAndRetryState(t *testing.T) {
	for _, test := range []struct {
		code codes.Code
		want error
	}{
		{codes.DeadlineExceeded, context.DeadlineExceeded},
		{codes.Canceled, context.Canceled},
	} {
		t.Run(test.code.String(), func(t *testing.T) {
			sb, backend, id := newTestSandbox(t)
			container := backend.lookup(id)
			task := container.task
			task.process = newFakeProcess(true)
			rpcErr := status.Error(test.code, "exec exit unavailable")
			stopErr := errors.New("quiescence verification failed")
			deleteErr := errors.New("process deletion failed")
			task.process.exitErr = rpcErr
			task.process.deleteErr = deleteErr
			task.pidsErr = stopErr
			if err := sb.Start(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			result, err := sb.Execute(context.Background(), id, []string{"true"}, nil)
			for _, cause := range []error{test.want, rpcErr, stopErr, deleteErr} {
				if !errors.Is(err, cause) {
					t.Fatalf("joined failure lost %v: %v", cause, err)
				}
			}
			if result.ExitCode != -1 || !task.killAll {
				t.Fatalf("failure changed execution cleanup: %+v %v", result, err)
			}
			state, _ := sb.getContainer(id)
			if !state.stopped || len(state.processes) != 1 || task.process.deleted != 0 {
				t.Fatal("cleanup failure lost unreaped process state")
			}
			if _, err := sb.Execute(context.Background(), id, []string{"true"}, nil); err == nil {
				t.Fatal("uncertain execution container was reused")
			}
			if err := sb.Remove(context.Background(), id); !errors.Is(err, stopErr) {
				t.Fatalf("remove lost pending cleanup failure: %v", err)
			}
			task.pidsErr = nil
			task.process.deleteErr = nil
			if err := sb.Remove(context.Background(), id); err != nil {
				t.Fatalf("exact resource cleanup could not retry: %v", err)
			}
			if container.removed != 1 || task.process.deleted != 1 || len(state.processes) != 0 {
				t.Fatal("cleanup retry did not reap exact resources")
			}
		})
	}
}
