// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/sandboxexec"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/id"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// 这些替身只测试普通工作器逻辑，不提供 containerd、内核证明或持久日志验收
// fakeStepRetryRuntime 显式覆盖活动分配，整作业分配仅在构造共享夹具时复用
type fakeStepRetryRuntime struct {
	*fakeGenerationRuntime
	session                                      *fakeStepRetrySession
	executeStep                                  func(taskpayload.StepAttemptAdmission, *executor.ExecutionRequest) (*executor.ExecutionResult, error)
	persistAdmissionErr, consumeErr, boundaryErr error
	afterAdmission                               func(taskpayload.StepAttemptAdmission)
	afterConsumed                                func(taskpayload.StepAttemptAdmission)
}

func (r *fakeStepRetryRuntime) AllocateStepRetryGeneration(ctx context.Context, p *taskpayload.JobRunTaskPayload, claim string, g taskpayload.JobGeneration) (offlineSession, error) {
	base, err := r.fakeGenerationRuntime.AllocateGeneration(ctx, p, claim, g)
	if err != nil {
		return nil, err
	}
	r.session = &fakeStepRetrySession{fakeGenerationSession: base.(*fakeGenerationSession), owner: r, nonce: id.GetUUID(), admitted: map[string]taskpayload.StepAttemptAdmission{}, observed: map[string]taskpayload.StepAttemptAdmission{}, consumed: map[string]bool{}, running: map[string]bool{}}
	return r.session, nil
}

type fakeStepRetrySession struct {
	*fakeGenerationSession
	owner             *fakeStepRetryRuntime
	nonce             string
	intents           []taskpayload.StepAttemptTransition
	admitted          map[string]taskpayload.StepAttemptAdmission
	observed          map[string]taskpayload.StepAttemptAdmission
	consumed, running map[string]bool
	paused            bool
	boundarySequence  uint64
	stepCalls         []taskpayload.StepAttemptAdmission
}

func (s *fakeStepRetrySession) Execute(context.Context, *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
	return nil, errors.New("ordinary execute bypassed step admission")
}
func (s *fakeStepRetrySession) StepAttemptSession(context.Context) (taskpayload.StepAttemptSession, error) {
	g := s.generation
	return taskpayload.StepAttemptSession{SessionID: g.RuntimeClaimID, SessionIncarnationNonce: s.nonce, Namespace: "fake-step", ContainerID: "arcentra-" + g.RuntimeClaimID, SnapshotID: "arcentra-" + g.RuntimeClaimID + "-snapshot", Snapshotter: "fake", ResourceOwnerDigest: strings.Repeat("a", 64), WorkspaceID: "job-" + g.RuntimeClaimID, WorkspaceDevice: 1, WorkspaceInode: 7}, nil
}
func (s *fakeStepRetrySession) RecordStepAttemptIntent(ctx context.Context, tr taskpayload.StepAttemptTransition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tr.Validate(); err != nil {
		return err
	}
	s.intents = append(s.intents, tr)
	s.runtime.events = append(s.runtime.events, "step-intent:"+tr.Scope.StepRunID)
	return nil
}
func (s *fakeStepRetrySession) RecordStepAttemptAdmission(_ context.Context, tr taskpayload.StepAttemptTransition, a taskpayload.StepAttemptAdmission) error {
	if s.owner.persistAdmissionErr != nil {
		return s.owner.persistAdmissionErr
	}
	if len(s.intents) == 0 || s.intents[len(s.intents)-1].RequestDigest != tr.RequestDigest {
		return errors.New("ACK precedes intent")
	}
	if err := a.ValidateTransition(tr); err != nil {
		return err
	}
	s.admitted[a.Scope.StepRunID] = a
	s.runtime.events = append(s.runtime.events, "step-admitted:"+a.Scope.StepRunID)
	if s.owner.afterAdmission != nil {
		s.owner.afterAdmission(a)
	}
	return nil
}
func (s *fakeStepRetrySession) RecordStepAttemptObservation(_ context.Context, tr taskpayload.StepAttemptTransition, a taskpayload.StepAttemptAdmission) error {
	if err := a.ValidateTransition(tr); err != nil {
		return err
	}
	s.observed[a.Scope.StepRunID] = a
	return nil
}
func (s *fakeStepRetrySession) RecordStepAttemptConsumed(ctx context.Context, a taskpayload.StepAttemptAdmission) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.owner.consumeErr != nil {
		return s.owner.consumeErr
	}
	if s.admitted[a.Scope.StepRunID] != a || s.consumed[a.Scope.StepRunID] || !s.running[a.Scope.StepRunID] {
		return errors.New("effect before durable ACK/RUNNING or consumed twice")
	}
	if err := checkStepAdmissionTime(ctx, a); err != nil {
		return err
	}
	s.consumed[a.Scope.StepRunID] = true
	s.runtime.events = append(s.runtime.events, "step-consumed:"+a.Scope.StepRunID)
	if s.owner.afterConsumed != nil {
		s.owner.afterConsumed(a)
	}
	return nil
}
func (s *fakeStepRetrySession) ExecuteStepAttempt(ctx context.Context, req *executor.ExecutionRequest, a taskpayload.StepAttemptAdmission) (*executor.ExecutionResult, error) {
	if err := s.RecordStepAttemptConsumed(ctx, a); err != nil {
		return nil, err
	}
	if !s.started || s.closed || s.sealed {
		return nil, errors.New("effect after sealed/closed or before start")
	}
	if a.Scope.Attempt > 1 && !s.paused {
		return nil, errors.New("retry without retained opaque boundary")
	}
	if a.Scope.Attempt == 1 && s.paused {
		return nil, errors.New("new logical step auto-resumed paused task")
	}
	s.paused = false
	s.stepCalls = append(s.stepCalls, a)
	s.runtime.events = append(s.runtime.events, "step-execute:"+a.Scope.StepRunID)
	var result *executor.ExecutionResult
	var err error
	if s.owner.executeStep != nil {
		result, err = s.owner.executeStep(a, req)
	} else {
		result, err = fakeGenerationResult(s.fakeGenerationSession, 0, nil)
	}
	if _, eligible := sandboxexec.RetryCommandExitCode(err, []int32{9}); eligible {
		s.paused = true
		s.boundarySequence++
	}
	return result, err
}
func (s *fakeStepRetrySession) ExecuteArtifactStepAttempt(ctx context.Context, req *executor.ExecutionRequest, a taskpayload.StepAttemptAdmission, target executor.Executor) (*executor.ExecutionResult, error) {
	if req.Step.Uses != "builtin:artifacts" || req.Step.Action != "upload" && req.Step.Action != "download" || req.Step.Action == "upload" && !s.sealed {
		return nil, errors.New("artifact lifecycle mismatch")
	}
	effectCtx := ctx
	if err := s.RecordStepAttemptConsumed(effectCtx, a); err != nil {
		return nil, err
	}
	if err := checkStepAdmissionTime(effectCtx, a); err != nil {
		return nil, err
	}
	if err := waitStepRetry(effectCtx, s.owner.AdmissionDone(), 0); err != nil {
		return nil, err
	}
	return target.Execute(effectCtx, req)
}
func (s *fakeStepRetrySession) HasPausedStepBoundary(ctx context.Context, a taskpayload.StepAttemptAdmission) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.owner.boundaryErr != nil {
		return s.owner.boundaryErr
	}
	if !s.paused || !s.consumed[a.Scope.StepRunID] {
		return errors.New("no live paused proof")
	}
	return nil
}
func (s *fakeStepRetrySession) PausedStepBoundary(ctx context.Context, a taskpayload.StepAttemptAdmission, digest string) (taskpayload.PausedStepBoundary, error) {
	if err := s.HasPausedStepBoundary(ctx, a); err != nil {
		return taskpayload.PausedStepBoundary{}, err
	}
	now := time.Now()
	b := taskpayload.PausedStepBoundary{Version: 1, Scope: a.Scope, BoundaryNonce: id.GetUUID(), BoundarySequence: s.boundarySequence, FailedAdmissionID: a.AdmissionID, FailedAdmissionBindingDigest: a.BindingDigest, FailedStepReceiptDigest: digest, CapturedUnixMilli: now.UnixMilli(), ExpiresUnixMilli: now.Add(time.Minute).UnixMilli(), FailedExecExitObserved: true, ExecReaped: true, TaskPaused: true, ExactLiveInitOnly: true, Childless: true}
	return b, b.Validate()
}

type fakeStepRetryClient struct {
	*fakeGenerationClient
	runtimeStep     *fakeStepRetryRuntime
	requests        []string
	admitted        map[string]taskpayload.StepAttemptAdmission
	ackedFailures   map[string]string
	terminalBytes   map[string][]byte
	lostStepACKs    int
	lostCASACKs     int
	mutateAdmission func(*taskpayload.StepAttemptAdmission)
	denyExecution   bool
	onRunning       func(*agentv1.ReportStepRunStatusRequest)
	rejectTerminals bool
	runningError    error
	afterCAS        func(taskpayload.StepAttemptAdmission)
	denyFor         func(taskpayload.StepAttemptIdentity) bool
}

func (c *fakeStepRetryClient) ReconcileStepAttempt(ctx context.Context, req *agentv1.ReconcileStepAttemptRequest, _ ...grpc.CallOption) (*agentv1.ReconcileStepAttemptResponse, error) {
	tr, err := taskpayload.ParseStepAttemptTransition(req.TransitionJson)
	if err != nil {
		return nil, err
	}
	s := c.runtimeStep.session
	md, _ := metadata.FromOutgoingContext(ctx)
	claims := md.Get(executionClaimMetadataKey)
	if req.AgentId != c.identity.AgentID || tr.Scope.Identity != c.identity || len(claims) != 1 || claims[0] != c.identity.ClaimID || s == nil || len(s.intents) == 0 || s.intents[len(s.intents)-1].RequestDigest != tr.RequestDigest {
		return nil, errors.New("CAS before exact intent or wrong claim")
	}
	if tr.Operation == "advance" && c.ackedFailures[tr.PreviousStepRunID] != tr.FailedStepReceiptDigest {
		return nil, errors.New("CAS before exact failed terminal ACK")
	}
	c.requests = append(c.requests, req.TransitionJson)
	a, exists := c.admitted[tr.RequestDigest]
	if !exists {
		now := time.Now()
		a = taskpayload.StepAttemptAdmission{Version: 1, Scope: tr.Scope, AdmissionID: id.GetUUID(), PreviousAdmissionID: tr.PreviousAdmissionID, RequestDigest: tr.RequestDigest, NotBeforeUnixMilli: now.UnixMilli(), ExpiresUnixMilli: now.Add(time.Minute).UnixMilli()}
		if tr.Operation == "advance" && c.delay > 0 {
			a.NotBeforeUnixMilli = now.Add(c.delay).UnixMilli()
			a.ExpiresUnixMilli = now.Add(c.delay + time.Minute).UnixMilli()
		}
		if c.mutateAdmission != nil {
			c.mutateAdmission(&a)
		}
		a.BindingDigest, _ = a.Digest()
		c.admitted[tr.RequestDigest] = a
	}
	if tr.Operation == "advance" && c.lostCASACKs > 0 {
		c.lostCASACKs--
		return nil, status.Error(codes.Unavailable, "committed CAS ACK lost")
	}
	encoded, err := a.Canonical()
	if err != nil {
		return nil, err
	}
	if c.afterCAS != nil {
		c.afterCAS(a)
	}
	allowed := !c.denyExecution
	if c.denyFor != nil && c.denyFor(a.Scope) {
		allowed = false
	}
	return &agentv1.ReconcileStepAttemptResponse{AdmissionJson: encoded, ExecutionAllowed: allowed}, nil
}
func (c *fakeStepRetryClient) ReportStepRunStatus(ctx context.Context, req *agentv1.ReportStepRunStatusRequest, _ ...grpc.CallOption) (*agentv1.ReportStepRunStatusResponse, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	claims := md.Get(executionClaimMetadataKey)
	if len(claims) != 1 || claims[0] != agentidentity.StepClaimID(c.identity.ClaimID, req.StepRunId) {
		return nil, errors.New("report changed original derived step claim")
	}
	s := c.runtimeStep.session
	if req.StepAttemptAdmissionDigest != "" {
		a, ok := s.admitted[req.StepRunId]
		if !ok && req.Status == steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED {
			a, ok = s.observed[req.StepRunId]
		}
		if !ok || req.StepAttemptAdmissionDigest != a.BindingDigest || req.GenerationId != a.Scope.GenerationID || req.GenerationBindingDigest != a.Scope.GenerationBindingDigest {
			return nil, errors.New("report before durable admission or wrong identity")
		}
		if req.Status == steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING {
			if c.runningError != nil {
				return nil, c.runningError
			}
			if err := checkStepAdmissionTime(ctx, a); err != nil {
				return nil, err
			}
			s.running[req.StepRunId] = true
			if c.onRunning != nil {
				c.onRunning(req)
			}
		}
	} else if req.Status == steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING || req.Status == steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS {
		return nil, errors.New("unadmitted attempt executed")
	}
	c.stepReports = append(c.stepReports, proto.Clone(req).(*agentv1.ReportStepRunStatusRequest))
	if req.Status >= steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS {
		data, _ := proto.MarshalOptions{Deterministic: true}.Marshal(req)
		if old := c.terminalBytes[req.StepRunId]; old != nil && !reflect.DeepEqual(old, data) {
			return nil, errors.New("terminal ACK retry changed bytes")
		}
		c.terminalBytes[req.StepRunId] = data
		if c.rejectTerminals {
			return nil, status.Error(codes.FailedPrecondition, "synthetic terminal conflict")
		}
		if req.RetryableShellExit && c.lostStepACKs > 0 {
			c.lostStepACKs--
			return nil, status.Error(codes.Unavailable, "failure receipt ACK lost")
		}
		if req.RetryableShellExit {
			digest, err := agentidentity.TerminalReceiptDigest(req, claims[0], c.payload.PipelineID, c.payload.PipelineRunID, c.payload.JobRunID)
			if err != nil {
				return nil, err
			}
			c.ackedFailures[req.StepRunId] = digest
		}
	}
	return &agentv1.ReportStepRunStatusResponse{Success: true}, nil
}
func fakeStepRetryFixture(t *testing.T, steps int) (*config.AgentConfig, *taskpayload.JobRunTaskPayload, *fakeStepRetryRuntime, *fakeStepRetryClient) {
	t.Helper()
	conf, p, base, client := fakeGenerationFixture(t, steps)
	p.Retry.Mode = "failed_step"
	conf.Agent.Sandbox.Containerd.Policy.AllowFailedStepRetry = true
	conf.Agent.Sandbox.Containerd.Policy.AllowWholeJobRetry = false
	bindFakeGenerationPlan(t, p)
	r := &fakeStepRetryRuntime{fakeGenerationRuntime: base}
	c := &fakeStepRetryClient{fakeGenerationClient: client, runtimeStep: r, admitted: map[string]taskpayload.StepAttemptAdmission{}, ackedFailures: map[string]string{}, terminalBytes: map[string][]byte{}}
	return conf, p, r, c
}
func runFakeStepRetry(ctx context.Context, conf *config.AgentConfig, p *taskpayload.JobRunTaskPayload, r *fakeStepRetryRuntime, c *fakeStepRetryClient) error {
	tlsOptions := controllerWorkerTLS(r.t)
	client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: "passthrough:///localhost:1", TLS: tlsOptions.clientTLS})
	if err != nil {
		r.t.Fatal(err)
	}
	defer client.Close()
	client.AgentClient = c
	return executeJobRun(withOfflineJobRuntime(ctx, r), conf, client, executor.NewExecutorManager(), p)
}
func fakeStepExit(code int32) (*executor.ExecutionResult, error) {
	var err error
	if code != 0 {
		err = &sandboxexec.CommandExitError{Code: code}
	}
	result := executor.NewExecutionResult("fake-live-step")
	result.Complete(code == 0, code, err)
	return result, err
}
func TestFailedStepWorkerRetainsSessionAndSuccessfulCommands(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 3)
	var calls []string
	r.executeStep = func(a taskpayload.StepAttemptAdmission, req *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		calls = append(calls, fmt.Sprintf("%d/%d", a.Scope.StepIndex, a.Scope.Attempt))
		if req.Env[taskpayload.RuntimeGenerationEnv] != "1" {
			t.Fatal("generation advanced")
		}
		marker := filepath.Join(req.Workspace, "retained")
		switch a.Scope.StepIndex {
		case 0:
			if err := os.WriteFile(marker, []byte("first"), 0600); err != nil {
				t.Fatal(err)
			}
		case 1:
			if a.Scope.Attempt == 1 {
				if err := os.WriteFile(marker, []byte("failed-partial-write"), 0600); err != nil {
					t.Fatal(err)
				}
				return fakeStepExit(9)
			}
			data, _ := os.ReadFile(marker)
			if string(data) != "failed-partial-write" {
				t.Fatal("failed state restored or workspace replaced")
			}
		}
		return fakeStepExit(0)
	}
	if err := runFakeStepRetry(context.Background(), conf, p, r, c); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"0/1", "1/1", "1/2", "2/1"}) {
		t.Fatalf("command counts: %v", calls)
	}
	if r.allocations != 1 || len(r.admissions) != 1 || c.inputCalls != 3 {
		t.Fatalf("allocation/input calls: %d/%d", r.allocations, c.inputCalls)
	}
	if !r.session.closed || r.acknowledgements != 1 {
		t.Fatal("final cleanup/ACK missing")
	}
	for _, req := range c.stepReports {
		if req.Status == steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS && req.StepAttemptAdmissionDigest == "" {
			t.Fatal("success lacks admission")
		}
	}
}
func TestFailedStepWorkerExactFailureAndCASACKReplay(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 1)
	c.lostStepACKs = 1
	c.lostCASACKs = 1
	r.executeStep = func(a taskpayload.StepAttemptAdmission, _ *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		if a.Scope.Attempt == 1 {
			return fakeStepExit(9)
		}
		return fakeStepExit(0)
	}
	if err := runFakeStepRetry(context.Background(), conf, p, r, c); err != nil {
		t.Fatal(err)
	}
	if len(r.session.stepCalls) != 2 || len(c.requests) != 3 || c.requests[1] != c.requests[2] {
		t.Fatalf("ACK loss changed requests/executions: %d/%d", len(r.session.stepCalls), len(c.requests))
	}
	if len(r.session.intents) != 2 || len(r.session.admitted) != 2 || len(r.session.consumed) != 2 {
		t.Fatal("ACK replay recreated authority")
	}
}
func TestFailedStepWorkerBoundedAttempts(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 2)
	r.executeStep = func(taskpayload.StepAttemptAdmission, *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		return fakeStepExit(9)
	}
	if err := runFakeStepRetry(context.Background(), conf, p, r, c); err == nil {
		t.Fatal("exhausted retry succeeded")
	}
	if len(r.session.stepCalls) != 3 || r.allocations != 1 {
		t.Fatal("retry bound or single generation violated")
	}
	for _, a := range r.session.stepCalls {
		if a.Scope.StepIndex != 0 {
			t.Fatal("later step ran after failed final attempt")
		}
	}
}
func TestFailedStepWorkerRefusesUnsafeContinuation(t *testing.T) {
	for _, kind := range []string{"background", "mixed", "persist", "consume", "identity", "expiry", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			conf, p, r, c := fakeStepRetryFixture(t, 2)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.executeStep = func(a taskpayload.StepAttemptAdmission, _ *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
				result, err := fakeStepExit(9)
				if kind == "mixed" {
					err = errors.Join(err, errors.New("log/reap failure"))
				}
				return result, err
			}
			switch kind {
			case "background":
				r.boundaryErr = errors.New("live or zombie descendant remains")
			case "persist":
				r.persistAdmissionErr = errors.New("fsync failed")
			case "consume":
				r.consumeErr = errors.New("consume fsync failed")
			case "identity":
				c.mutateAdmission = func(a *taskpayload.StepAttemptAdmission) { a.Scope.Session.SessionIncarnationNonce = id.GetUUID() }
			case "expiry":
				c.mutateAdmission = func(a *taskpayload.StepAttemptAdmission) {
					a.NotBeforeUnixMilli = time.Now().Add(-time.Minute).UnixMilli()
					a.ExpiresUnixMilli = time.Now().Add(-time.Second).UnixMilli()
				}
			case "cancel":
				r.afterAdmission = func(taskpayload.StepAttemptAdmission) { cancel() }
			}
			if err := runFakeStepRetry(ctx, conf, p, r, c); err == nil {
				t.Fatal("unsafe continuation succeeded")
			}
			if r.session != nil && len(r.session.stepCalls) > 1 {
				t.Fatalf("unsafe case executed successor: %d", len(r.session.stepCalls))
			}
			if r.session != nil && !r.session.closed {
				t.Fatal("unsafe session not cleaned")
			}
		})
	}
}
func TestFailedStepWorkerAdmissionDelayAndDrain(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 1)
	c.delay = time.Hour
	r.executeStep = func(taskpayload.StepAttemptAdmission, *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		return fakeStepExit(9)
	}
	r.afterAdmission = func(a taskpayload.StepAttemptAdmission) {
		if a.Scope.Attempt == 2 {
			r.beginDrain()
		}
	}
	if err := runFakeStepRetry(context.Background(), conf, p, r, c); err == nil {
		t.Fatal("drained retry succeeded")
	}
	if len(r.session.stepCalls) != 1 || len(r.session.consumed) != 1 {
		t.Fatal("resume occurred during delay/drain")
	}
}
func TestFailedStepReadinessRemainsOff(t *testing.T) {
	if (&workerQueues{}).FailedStepRetryReady() {
		t.Fatal("unaccepted runtime advertised live retry")
	}
}

func TestFailedStepWorkerDeniedDescriptorIsRetainedAndNeverExecuted(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 1)
	c.denyExecution = true
	if err := runFakeStepRetry(context.Background(), conf, p, r, c); err == nil {
		t.Fatal("denied admission succeeded")
	}
	if len(r.session.observed) != 1 || len(r.session.admitted) != 0 || len(r.session.consumed) != 0 || len(r.session.stepCalls) != 0 {
		t.Fatal("denied descriptor lost or became executable")
	}
	if !r.session.closed || r.acknowledgements != 1 {
		t.Fatal("known unstarted refusal did not finalize")
	}
}
func TestFailedStepWorkerDrainDuringRunningACKPreventsEffect(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 1)
	c.onRunning = func(*agentv1.ReportStepRunStatusRequest) { r.beginDrain() }
	if err := runFakeStepRetry(context.Background(), conf, p, r, c); err == nil {
		t.Fatal("drained admission succeeded")
	}
	if len(r.session.consumed) != 0 || len(r.session.stepCalls) != 0 {
		t.Fatal("drain during RUNNING RPC allowed effect")
	}
}

func TestFailedStepWorkerMultipleSealedUploadsAndConditionSkip(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 4)
	store := &memoryArtifactStorage{}
	SetStorage(store, artifactTestReference())
	t.Cleanup(func() { SetStorage(nil) })
	for i := 1; i < 4; i++ {
		p.Steps[i].Uses = "builtin:artifacts"
		p.Steps[i].Action = "upload"
		p.Steps[i].Args = map[string]any{"paths": []string{fmt.Sprintf("file-%d", i)}}
	}
	p.Steps[2].When = "false"
	bindFakeGenerationPlan(t, p)
	r.executeStep = func(_ taskpayload.StepAttemptAdmission, req *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		for _, i := range []int{1, 3} {
			if err := os.WriteFile(filepath.Join(req.Workspace, fmt.Sprintf("file-%d", i)), []byte("content"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return fakeStepExit(0)
	}
	if err := runFakeStepRetry(context.Background(), conf, p, r, c); err != nil {
		t.Fatal(err)
	}
	if len(r.session.stepCalls) != 1 || store.uploads != 2 || len(r.session.consumed) != 3 {
		t.Fatalf("sealed uploads/skip: shell=%d uploads=%d consumed=%d", len(r.session.stepCalls), store.uploads, len(r.session.consumed))
	}
	final := c.jobReports[len(c.jobReports)-1]
	if final.Status != 4 || len(final.Artifacts) != 2 {
		t.Fatal("sealed outputs not final winners")
	}
	for _, a := range final.Artifacts {
		if a.StepRunId != p.Steps[1].StepRunID && a.StepRunId != p.Steps[3].StepRunID {
			t.Fatal("unused/skipped attempt became artifact winner")
		}
	}
}

func TestFailedStepWorkerUnresolvedFailureACKNeverAdvances(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 2)
	c.lostStepACKs = 100
	r.executeStep = func(taskpayload.StepAttemptAdmission, *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		return fakeStepExit(9)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Millisecond)
	defer cancel()
	if err := runFakeStepRetry(ctx, conf, p, r, c); err == nil {
		t.Fatal("unresolved terminal ACK succeeded")
	}
	if len(r.session.stepCalls) != 1 || len(c.requests) != 1 || len(r.session.consumed) != 1 {
		t.Fatal("pending failure ACK allowed successor")
	}
	if len(c.jobReports) != 1 || !r.session.closed {
		t.Fatal("uncertain terminal generated a job outcome or skipped cleanup")
	}
}
func TestFailedStepWorkerUnresolvedCASACKNeverConsumes(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 1)
	c.lostCASACKs = 100
	r.executeStep = func(taskpayload.StepAttemptAdmission, *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		return fakeStepExit(9)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Millisecond)
	defer cancel()
	if err := runFakeStepRetry(ctx, conf, p, r, c); err == nil {
		t.Fatal("unresolved CAS ACK succeeded")
	}
	if len(r.session.stepCalls) != 1 || len(r.session.admitted) != 1 || len(r.session.intents) != 2 || len(r.session.consumed) != 1 {
		t.Fatal("unresolved admission allowed effect")
	}
	for _, request := range c.requests[1:] {
		if request != c.requests[1] {
			t.Fatal("CAS retransmission minted a new transition")
		}
	}
	if len(c.jobReports) != 1 || !r.session.closed {
		t.Fatal("uncertain CAS fabricated final outcome or left resources live")
	}
}

func TestFailedStepWorkerRejectedRunningClosesExactUnstartedAttempt(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 1)
	c.runningError = status.Error(codes.FailedPrecondition, "admission expired before RUNNING")
	if err := runFakeStepRetry(context.Background(), conf, p, r, c); err == nil {
		t.Fatal("rejected RUNNING succeeded")
	}
	if len(r.session.stepCalls) != 0 || len(r.session.consumed) != 0 {
		t.Fatal("rejected RUNNING executed")
	}
	found := false
	for _, req := range c.stepReports {
		if req.StepRunId == p.Steps[0].StepRunID && req.Status == steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED {
			found = req.StepAttemptAdmissionDigest != "" && !req.RetryableShellExit
		}
	}
	if !found || r.acknowledgements != 1 {
		t.Fatal("admitted unstarted attempt left pending forever")
	}
}

func TestFailedStepWorkerCancelledACKStillPersistsDescriptor(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.afterCAS = func(taskpayload.StepAttemptAdmission) { cancel() }
	if err := runFakeStepRetry(ctx, conf, p, r, c); err == nil {
		t.Fatal("cancelled admission succeeded")
	}
	if len(r.session.observed) != 1 || len(r.session.stepCalls) != 0 || len(r.session.consumed) != 0 {
		t.Fatal("cancelled ACK descriptor discarded or executed")
	}
}
func TestFailedStepWorkerArtifactExpiryAfterConsumeDoesNotUpload(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 2)
	store := &memoryArtifactStorage{}
	SetStorage(store, artifactTestReference())
	t.Cleanup(func() { SetStorage(nil) })
	p.Steps[1].Uses = "builtin:artifacts"
	p.Steps[1].Action = "upload"
	p.Steps[1].Args = map[string]any{"paths": []string{"file"}}
	bindFakeGenerationPlan(t, p)
	r.executeStep = func(_ taskpayload.StepAttemptAdmission, req *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		if err := os.WriteFile(filepath.Join(req.Workspace, "file"), []byte("content"), 0600); err != nil {
			t.Fatal(err)
		}
		return fakeStepExit(0)
	}
	c.mutateAdmission = func(a *taskpayload.StepAttemptAdmission) {
		if a.Scope.StepIndex == 1 {
			a.ExpiresUnixMilli = time.Now().Add(100 * time.Millisecond).UnixMilli()
		}
	}
	r.afterConsumed = func(a taskpayload.StepAttemptAdmission) {
		if a.Scope.StepIndex == 1 {
			time.Sleep(150 * time.Millisecond)
		}
	}
	if err := runFakeStepRetry(context.Background(), conf, p, r, c); err == nil {
		t.Fatal("expired artifact admission succeeded")
	}
	if len(r.session.consumed) != 2 || store.uploads != 0 {
		t.Fatal("artifact started after consumption crossed expiry")
	}
}
func TestFailedStepWorkerDeniedSealedUploadDoesNotPublish(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 3)
	store := &memoryArtifactStorage{}
	SetStorage(store, artifactTestReference())
	t.Cleanup(func() { SetStorage(nil) })
	for i := 1; i < 3; i++ {
		p.Steps[i].Uses = "builtin:artifacts"
		p.Steps[i].Action = "upload"
		p.Steps[i].Args = map[string]any{"paths": []string{"file"}}
	}
	bindFakeGenerationPlan(t, p)
	r.executeStep = func(_ taskpayload.StepAttemptAdmission, req *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		if err := os.WriteFile(filepath.Join(req.Workspace, "file"), []byte("content"), 0600); err != nil {
			t.Fatal(err)
		}
		return fakeStepExit(0)
	}
	c.denyFor = func(scope taskpayload.StepAttemptIdentity) bool { return scope.StepIndex == 2 }
	if err := runFakeStepRetry(context.Background(), conf, p, r, c); err == nil {
		t.Fatal("denied sealed upload succeeded")
	}
	if store.uploads != 1 || len(r.session.observed) != 1 || r.session.consumed[p.Steps[2].StepRunID] {
		t.Fatal("denied sealed upload performed effects")
	}
	final := c.jobReports[len(c.jobReports)-1]
	if len(final.Artifacts) != 0 || len(final.ArtifactUris) != 0 {
		t.Fatal("failed output promoted as winning artifact")
	}
}

func TestFailedStepWorkerQuarantinedTerminalNeverRewritten(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 1)
	conf.Grpc.ServerAddr = "controller:9090"
	c.rejectTerminals = true
	outbox, err := newTerminalOutbox(context.Background(), filepath.Join(t.TempDir(), "outbox"), conf.Agent.ID, conf.Grpc.ServerAddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outbox.close() })
	if err := runFakeStepRetry(withTerminalOutbox(context.Background(), outbox), conf, p, r, c); err == nil {
		t.Fatal("quarantined result succeeded")
	}
	if len(r.session.stepCalls) != 1 || len(c.jobReports) != 1 {
		t.Fatal("quarantine allowed another effect or guessed final result")
	}
	var original agentv1.ReportStepRunStatusRequest
	if err := proto.Unmarshal(c.terminalBytes[p.Steps[0].StepRunID], &original); err != nil {
		t.Fatal(err)
	}
	if original.Status != steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS {
		t.Fatal("quarantined original success rewritten as failed")
	}
	count := 0
	for _, req := range c.stepReports {
		if req.Status >= steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("quarantined terminal retried/replaced: %d", count)
	}
}

func TestFailedStepWorkerRejectsUnverifiedControllerBeforeAdmission(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 1)
	err := executeJobRun(withOfflineJobRuntime(context.Background(), r), conf, &agentgrpc.ClientWrapper{AgentClient: c}, executor.NewExecutorManager(), p)
	if err == nil {
		t.Fatal("unverified controller accepted")
	}
	if r.allocations != 0 || len(r.intents) != 0 || len(c.requests) != 0 {
		t.Fatal("unverified response could allocate/admit execution")
	}
}
