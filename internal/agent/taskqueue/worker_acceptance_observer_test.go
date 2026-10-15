//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/nova"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

var _ acceptanceWorkerObserver = (*workerDeliveryObserver)(nil)

type acceptanceContextValueKey struct{}
type acceptanceWorkerOnlyKey struct{}

func acceptanceObservedTask(t *testing.T, id string) *nova.Task {
	t.Helper()
	payload, err := sonic.Marshal(&taskpayload.JobRunTaskPayload{JobRunID: id, JobName: "observed"})
	if err != nil {
		t.Fatal(err)
	}
	return &nova.Task{Type: taskpayload.TaskTypeJobRun, Payload: payload}
}

func TestAcceptanceWorkerObservationWithoutAttachment(t *testing.T) {
	for _, ctx := range []context.Context{
		context.Background(),
		context.WithValue(context.Background(), acceptanceContextValueKey{}, "unrelated"),
		context.WithValue(context.Background(), acceptanceWorkerObserverKey{}, "wrong type"),
	} {
		handler := nova.HandlerFunc(func(context.Context, *nova.Task) error { return nil })
		if got := observeAcceptanceWorkerHandler(ctx, handler); reflect.ValueOf(got).Pointer() != reflect.ValueOf(handler).Pointer() {
			t.Fatal("missing attachment replaced the handler")
		}
		if observeAcceptanceWorkerHandler(ctx, nil) != nil || observeAcceptanceWorkerReplay(ctx) != ctx {
			t.Fatal("missing attachment changed nil handler or replay context")
		}
	}
}

func TestAcceptanceWorkerObservationPreservesInvocation(t *testing.T) {
	for name, wantErr := range map[string]error{
		"success":  nil,
		"plain":    errors.New("original handler failure"),
		"status":   status.Error(codes.Unavailable, "original RPC failure"),
		"delivery": &terminalDeliveryError{cause: status.Error(codes.Unavailable, "original terminal failure")},
	} {
		t.Run(name, func(t *testing.T) {
			observer := &workerDeliveryObserver{targetID: "job"}
			workerCtx := context.WithValue(context.Background(), acceptanceWorkerOnlyKey{}, "worker-only")
			workerCtx = context.WithValue(workerCtx, acceptanceWorkerObserverKey{}, observer)
			value := new(int)
			incoming := context.WithValue(context.Background(), acceptanceContextValueKey{}, value)
			incoming = metadata.NewIncomingContext(incoming, metadata.Pairs("incoming", "original"))
			incoming = metadata.NewOutgoingContext(incoming, metadata.Pairs("outgoing", "original"))
			deadline := time.Now().Add(time.Minute)
			incoming, stopDeadline := context.WithDeadline(incoming, deadline)
			defer stopDeadline()
			incoming, cancel := context.WithCancelCause(incoming)
			defer cancel(nil)
			cause := errors.New("incoming cancellation")
			task := acceptanceObservedTask(t, "job")
			originalBytes := bytes.Clone(task.Payload)
			calls := 0
			handler := observeAcceptanceWorkerHandler(workerCtx, func(ctx context.Context, got *nova.Task) error {
				calls++
				if !observer.mu.TryLock() {
					t.Fatal("observer held its lock during the actual handler")
				}
				observer.mu.Unlock()
				if got != task || !bytes.Equal(got.Payload, originalBytes) {
					t.Fatal("handler task pointer or bytes changed")
				}
				if ctx.Value(acceptanceContextValueKey{}) != value || ctx.Value(acceptanceWorkerOnlyKey{}) != nil || ctx.Value(workerDeliveryContextKey{}) != 1 || ctx.Value(workerDeliveryBackgroundKey{}) != nil {
					t.Fatal("handler context values/provenance changed")
				}
				if gotDeadline, ok := ctx.Deadline(); !ok || gotDeadline != deadline || ctx.Done() != incoming.Done() {
					t.Fatal("handler no longer derives from the incoming context")
				}
				in, _ := metadata.FromIncomingContext(ctx)
				out, _ := metadata.FromOutgoingContext(ctx)
				if !reflect.DeepEqual(in, metadata.Pairs("incoming", "original")) || !reflect.DeepEqual(out, metadata.Pairs("outgoing", "original")) {
					t.Fatal("handler RPC metadata changed")
				}
				cancel(cause)
				if ctx.Err() != context.Canceled || context.Cause(ctx) != cause {
					t.Fatal("incoming cancellation/cause was lost")
				}
				return wantErr
			})
			if gotErr := handler(incoming, task); gotErr != wantErr || calls != 1 || !bytes.Equal(task.Payload, originalBytes) {
				t.Fatalf("handler transparency lost: calls=%d error=%v", calls, gotErr)
			}
			snapshot := observer.snapshot()
			if len(snapshot.invocations) != 1 || snapshot.invocations[0].end <= snapshot.invocations[0].begin || snapshot.invocations[0].code != status.Code(wantErr) || snapshot.invocations[0].deliveryError != (name == "delivery") {
				t.Fatalf("invocation not observed exactly once: %s", snapshot.summary())
			}
			if incoming.Value(workerDeliveryContextKey{}) != nil || workerCtx.Value(workerDeliveryBackgroundKey{}) != nil {
				t.Fatal("observation mutated a source context")
			}
		})
	}
}

func TestAcceptanceWorkerObservationPassesThroughOtherTasks(t *testing.T) {
	for name, task := range map[string]*nova.Task{
		"nil":        nil,
		"other-job":  acceptanceObservedTask(t, "other"),
		"other-type": {Type: taskpayload.TaskTypeJobCancel, Payload: []byte("original")},
		"malformed":  {Type: taskpayload.TaskTypeJobRun, Payload: []byte("{")},
	} {
		t.Run(name, func(t *testing.T) {
			observer := &workerDeliveryObserver{targetID: "job"}
			workerCtx := context.WithValue(context.Background(), acceptanceWorkerObserverKey{}, observer)
			incoming := context.WithValue(context.Background(), acceptanceContextValueKey{}, "original")
			wantErr := errors.New("original passthrough error")
			calls := 0
			handler := observeAcceptanceWorkerHandler(workerCtx, func(ctx context.Context, got *nova.Task) error {
				calls++
				if ctx != incoming || got != task {
					t.Fatal("nonmatching task/context changed")
				}
				return wantErr
			})
			if handler(incoming, task) != wantErr || calls != 1 || len(observer.snapshot().invocations) != 0 {
				t.Fatal("nonmatching task was not passed through exactly once")
			}
		})
	}
}

func TestAcceptanceWorkerObservationSeparatesWorkersAndReplay(t *testing.T) {
	a, b := &workerDeliveryObserver{targetID: "job"}, &workerDeliveryObserver{targetID: "job"}
	deadline := time.Now().Add(time.Minute)
	source, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	source = context.WithValue(source, acceptanceContextValueKey{}, "original")
	source = metadata.NewOutgoingContext(source, metadata.Pairs("source", "original"))
	aCtx := context.WithValue(source, acceptanceWorkerObserverKey{}, a)
	bCtx := context.WithValue(context.Background(), acceptanceWorkerObserverKey{}, b)
	replaySource, stopReplay := context.WithCancel(aCtx)
	defer stopReplay()
	replay := observeAcceptanceWorkerReplay(replaySource)
	if replay.Value(workerDeliveryBackgroundKey{}) != true || replay.Value(workerDeliveryContextKey{}) != nil || replay.Value(acceptanceContextValueKey{}) != "original" || replay.Done() != replaySource.Done() {
		t.Fatal("periodic replay context lost source identity or acquired invocation metadata")
	}
	if got, ok := replay.Deadline(); !ok || got != deadline {
		t.Fatal("periodic replay deadline changed")
	}
	if md, _ := metadata.FromOutgoingContext(replay); !reflect.DeepEqual(md, metadata.Pairs("source", "original")) {
		t.Fatal("periodic replay metadata changed")
	}
	for _, ctx := range []context.Context{source, aCtx, bCtx, replaySource} {
		if ctx.Value(workerDeliveryBackgroundKey{}) != nil || ctx.Value(workerDeliveryContextKey{}) != nil {
			t.Fatal("observation contaminated startup, worker, or source context")
		}
	}
	task := acceptanceObservedTask(t, "job")
	process := nova.HandlerFunc(func(ctx context.Context, _ *nova.Task) error {
		if ctx.Value(workerDeliveryBackgroundKey{}) != nil {
			t.Fatal("handler inherited periodic replay metadata")
		}
		return nil
	})
	for _, ctx := range []context.Context{aCtx, bCtx, aCtx} {
		if err := observeAcceptanceWorkerHandler(ctx, process)(context.Background(), task); err != nil {
			t.Fatal(err)
		}
	}
	if len(a.snapshot().invocations) != 2 || len(b.snapshot().invocations) != 1 {
		t.Fatal("workers shared an observer")
	}
	stopReplay()
	if replay.Err() != context.Canceled || aCtx.Err() != nil || bCtx.Err() != nil {
		t.Fatal("periodic replay cancellation leaked into a worker context")
	}
	cancel()
	if aCtx.Err() != context.Canceled || bCtx.Err() != nil {
		t.Fatal("workers shared cancellation")
	}
}

type acceptanceRPCClient struct {
	agentv1.AgentServiceClient
	job  func(context.Context, *agentv1.ReportJobRunStatusRequest, ...grpc.CallOption) (*agentv1.ReportJobRunStatusResponse, error)
	step func(context.Context, *agentv1.ReportStepRunStatusRequest, ...grpc.CallOption) (*agentv1.ReportStepRunStatusResponse, error)
}

func (c *acceptanceRPCClient) ReportJobRunStatus(ctx context.Context, req *agentv1.ReportJobRunStatusRequest, opts ...grpc.CallOption) (*agentv1.ReportJobRunStatusResponse, error) {
	return c.job(ctx, req, opts...)
}

func (c *acceptanceRPCClient) ReportStepRunStatus(ctx context.Context, req *agentv1.ReportStepRunStatusRequest, opts ...grpc.CallOption) (*agentv1.ReportStepRunStatusResponse, error) {
	return c.step(ctx, req, opts...)
}

func TestAcceptanceWorkerObservationPreservesRPC(t *testing.T) {
	for _, wantErr := range []error{nil, status.Error(codes.Unavailable, "original RPC failure")} {
		observer := &workerDeliveryObserver{targetID: "job", failureID: "step"}
		ctx := observer.backgroundContext(context.Background())
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(executionClaimMetadataKey, "original-claim"))
		job := &agentv1.ReportJobRunStatusRequest{JobRunId: "job", Status: 4}
		step := &agentv1.ReportStepRunStatusRequest{StepRunId: "step", RetryableShellExit: true}
		jobResponse := &agentv1.ReportJobRunStatusResponse{Success: wantErr == nil}
		stepResponse := &agentv1.ReportStepRunStatusResponse{Success: wantErr == nil}
		option := grpc.WaitForReady(true)
		calls := 0
		check := func(got context.Context, opts []grpc.CallOption) {
			t.Helper()
			calls++
			if !observer.mu.TryLock() {
				t.Fatal("observer held its lock during the actual RPC")
			}
			observer.mu.Unlock()
			if got != ctx || len(opts) != 1 || !reflect.DeepEqual(opts[0], option) {
				t.Fatal("RPC context or options changed")
			}
		}
		underlying := &acceptanceRPCClient{
			job: func(got context.Context, req *agentv1.ReportJobRunStatusRequest, opts ...grpc.CallOption) (*agentv1.ReportJobRunStatusResponse, error) {
				check(got, opts)
				if req != job {
					t.Fatal("job RPC request pointer changed")
				}
				return jobResponse, wantErr
			},
			step: func(got context.Context, req *agentv1.ReportStepRunStatusRequest, opts ...grpc.CallOption) (*agentv1.ReportStepRunStatusResponse, error) {
				check(got, opts)
				if req != step {
					t.Fatal("step RPC request pointer changed")
				}
				return stepResponse, wantErr
			},
		}
		client := &workerDeliveryClient{AgentServiceClient: underlying, observer: observer}
		if response, err := client.ReportJobRunStatus(ctx, job, option); response != jobResponse || err != wantErr {
			t.Fatal("job RPC result changed")
		}
		if response, err := client.ReportStepRunStatus(ctx, step, option); response != stepResponse || err != wantErr {
			t.Fatal("step RPC result changed")
		}
		snapshot := observer.snapshot()
		if calls != 2 || len(snapshot.calls) != 1 || len(snapshot.stepCalls) != 1 || snapshot.calls[0].origin != "background" || snapshot.stepCalls[0].origin != "background" {
			t.Fatalf("RPC observation changed call count/provenance: %s", snapshot.summary())
		}
	}
}

func TestAcceptanceWorkerDeliveryAttachmentRequiresFlags(t *testing.T) {
	for _, flags := range [][2]string{{"", ""}, {"1", ""}, {"", "1"}, {"true", "1"}, {"1", "true"}} {
		t.Setenv("ARCENTRA_CONTAINERD_ACCEPTANCE", flags[0])
		t.Setenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", flags[1])
		queue, err := startContainerAcceptanceWorkerObserved(context.Background(), &config.AgentConfig{}, nil, nil, nil, &workerDeliveryObserver{})
		if queue != nil || err == nil || err.Error() != "explicit container acceptance flags required" {
			t.Fatalf("attachment accepted flags %v: queue=%v error=%v", flags, queue, err)
		}
	}
}

// Instrumentation coverage only: the existing fixture uses MockCluster, SQLite
// and a bare-metal worker. It is not real-container or failed-step acceptance.
func TestAcceptanceWorkerObservationStartWorkerMockKafka(t *testing.T) {
	observer := &workerDeliveryObserver{targetName: "observed"}
	options := controllerWorkerTLS(t)
	options.startWorker = func(ctx context.Context, conf *config.AgentConfig, client *agentgrpc.ClientWrapper, manager *executor.Manager) (nova.TaskQueue, error) {
		client.AgentClient = &workerDeliveryClient{AgentServiceClient: client.AgentClient, observer: observer}
		return StartWorker(context.WithValue(ctx, acceptanceWorkerObserverKey{}, observer), conf, client, manager)
	}
	f := newControllerWorkerFixture(t, false, options)
	run := f.submit(&spec.Job{Name: "observed", Steps: []*spec.Step{controllerWorkerShell("once", "printf observed")}})
	f.waitRun(run.RunID, model.PipelineStatusSuccess)
	f.wait("observed handler and terminal outbox completion", func() bool {
		snapshot := observer.snapshot()
		if len(snapshot.invocations) != 1 || snapshot.invocations[0].end <= snapshot.invocations[0].begin {
			return false
		}
		f.worker.reports.mu.Lock()
		defer f.worker.reports.mu.Unlock()
		return len(f.worker.reports.entries) == 0
	})
	before := observer.snapshot()
	if len(before.calls) == 0 || before.invocations[0].code != codes.OK {
		t.Fatalf("normal StartWorker did not observe its actual handler/RPCs: %s", before.summary())
	}
	for _, call := range before.calls {
		if call.origin != "completion" && call.origin != "background" {
			t.Fatalf("normal worker terminal RPC lacked provenance: %s", before.summary())
		}
	}
	// Stage the exact acknowledged receipt after the callback ends. Only the
	// ordinary periodic replay loop can deliver it; no synthetic handler call or
	// replay invocation is used. This deliberately adds one smoke-test replay.
	last := before.calls[len(before.calls)-1]
	request := last.message.(*agentv1.ReportJobRunStatusRequest)
	if err := f.worker.reports.put(f.ctx, terminalRecord{Kind: "job", RunID: request.JobRunId, ClaimID: last.claim, Payload: bytes.Clone(last.payload)}); err != nil {
		t.Fatal(err)
	}
	f.wait("ordinary periodic replay observation", func() bool {
		snapshot := observer.snapshot()
		return len(snapshot.calls) > len(before.calls) && snapshot.calls[len(snapshot.calls)-1].end > snapshot.calls[len(snapshot.calls)-1].begin
	})
	after := observer.snapshot()
	replayed := after.calls[len(after.calls)-1]
	if len(after.invocations) != 1 || len(after.calls) != len(before.calls)+1 || replayed.origin != "background" || replayed.invocation != 0 || replayed.code != codes.OK || !replayed.success || replayed.claim != last.claim || !bytes.Equal(replayed.payload, last.payload) {
		t.Fatalf("normal StartWorker periodic replay changed provenance/identity: %s", after.summary())
	}
	if err := f.worker.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.worker.reportsDone:
	default:
		t.Fatal("normal worker Stop returned before observed replay loop exited")
	}
}
