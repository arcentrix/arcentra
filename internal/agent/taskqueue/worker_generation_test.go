// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/sandboxexec"
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

// fakeGenerationRuntime 只验证普通工作器编排，不运行容器或声称具备真实持久日志
// 实际控制面、网络及容器验收由独立集成和显式 opt-in 测试覆盖
type fakeGenerationRuntime struct {
	t                                                                   *testing.T
	root                                                                string
	drain                                                               chan struct{}
	events                                                              []string
	intents                                                             []taskpayload.JobGenerationTransition
	admissions                                                          []taskpayload.JobGeneration
	sessions                                                            []*fakeGenerationSession
	allocations                                                         int
	acknowledgements                                                    int
	originalClaim                                                       string
	ready                                                               bool
	execute                                                             func(*fakeGenerationSession, *executor.ExecutionRequest) (*executor.ExecutionResult, error)
	afterAdmission                                                      func(taskpayload.JobGeneration)
	beforeAllocation                                                    func(taskpayload.JobGeneration)
	sessionStartErr, sessionSealErr, sessionCloseErr, cleanupReceiptErr error
	admissionErr                                                        error
}

func (r *fakeGenerationRuntime) Ready() bool                    { return r.ready }
func (r *fakeGenerationRuntime) HasJob(string) bool             { return len(r.intents) > 0 }
func (r *fakeGenerationRuntime) AdmissionDone() <-chan struct{} { return r.drain }
func (r *fakeGenerationRuntime) Allocate(context.Context, *taskpayload.JobRunTaskPayload, string) (offlineSession, error) {
	return nil, errors.New("legacy allocation must not handle generations")
}
func (r *fakeGenerationRuntime) RecordGenerationIntent(ctx context.Context, tr taskpayload.JobGenerationTransition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tr.Validate(); err != nil {
		return err
	}
	if !r.ready {
		return errors.New("fake restarted journal is unresolved")
	}
	r.events = append(r.events, "intent:"+tr.Operation)
	r.intents = append(r.intents, tr)
	if r.originalClaim == "" {
		r.originalClaim = tr.Identity.ClaimID
	}
	if r.originalClaim != tr.Identity.ClaimID {
		return errors.New("original claim changed")
	}
	return nil
}
func (r *fakeGenerationRuntime) RecordGenerationAdmission(_ context.Context, tr taskpayload.JobGenerationTransition, g taskpayload.JobGeneration) error {
	if r.admissionErr != nil {
		return r.admissionErr
	}
	if len(r.intents) == 0 || r.intents[len(r.intents)-1].RequestDigest != tr.RequestDigest {
		return errors.New("admission preceded intent")
	}
	r.events = append(r.events, fmt.Sprintf("admit:%d", g.Number))
	r.admissions = append(r.admissions, g)
	if r.afterAdmission != nil {
		r.afterAdmission(g)
	}
	return nil
}
func (r *fakeGenerationRuntime) AllocateGeneration(ctx context.Context, p *taskpayload.JobRunTaskPayload, claim string, g taskpayload.JobGeneration) (offlineSession, error) {
	if r.beforeAllocation != nil {
		r.beforeAllocation(g)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-r.drain:
		return nil, errors.New("fake runtime draining")
	default:
	}
	if claim != r.originalClaim || p.RetryPlanDigest != g.RetryPlanDigest || len(r.admissions) == 0 || r.admissions[len(r.admissions)-1] != g {
		return nil, errors.New("allocation changed admitted identity")
	}
	r.allocations++
	workspace := filepath.Join(r.root, fmt.Sprintf("generation-%d", g.Number))
	if err := os.Mkdir(workspace, 0700); err != nil {
		return nil, err
	}
	s := &fakeGenerationSession{runtime: r, generation: g, workspace: workspace}
	r.sessions = append(r.sessions, s)
	r.events = append(r.events, fmt.Sprintf("allocate:%d", g.Number))
	return s, nil
}
func (r *fakeGenerationRuntime) Acknowledge(_ context.Context, _ string, claim string) error {
	if claim != r.originalClaim {
		return errors.New("completion changed original claim")
	}
	for _, s := range r.sessions {
		if !s.closed || r.sessionCloseErr != nil || r.sessionSealErr != nil {
			return errors.New("cleanup remains unconfirmed")
		}
	}
	r.acknowledgements++
	return nil
}
func (r *fakeGenerationRuntime) beginDrain() {
	select {
	case <-r.drain:
	default:
		close(r.drain)
	}
}

type fakeGenerationSession struct {
	runtime                 *fakeGenerationRuntime
	generation              taskpayload.JobGeneration
	workspace               string
	started, sealed, closed bool
	executions              int
	requests                []*executor.ExecutionRequest
}

func (s *fakeGenerationSession) Name() string                               { return "explicitly-fake-generation-session" }
func (s *fakeGenerationSession) CanExecute(*executor.ExecutionRequest) bool { return true }
func (s *fakeGenerationSession) Workspace() string                          { return s.workspace }
func (s *fakeGenerationSession) Start(context.Context) error {
	s.started = true
	return s.runtime.sessionStartErr
}
func (s *fakeGenerationSession) Seal(context.Context) error {
	s.sealed = true
	return s.runtime.sessionSealErr
}
func (s *fakeGenerationSession) Close(context.Context) error {
	s.closed = true
	return s.runtime.sessionCloseErr
}
func (s *fakeGenerationSession) Execute(_ context.Context, req *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
	s.executions++
	s.requests = append(s.requests, req)
	if !s.started || s.closed || s.sealed {
		return nil, errors.New("fake session lifecycle violated")
	}
	if s.runtime.execute != nil {
		return s.runtime.execute(s, req)
	}
	return fakeGenerationResult(s, 0, nil)
}
func fakeGenerationResult(s *fakeGenerationSession, code int32, err error) (*executor.ExecutionResult, error) {
	r := executor.NewExecutionResult(s.Name())
	r.Complete(code == 0 && err == nil, code, err)
	return r, err
}
func (s *fakeGenerationSession) CleanupReceipt(context.Context) (taskpayload.JobGenerationCleanupReceipt, error) {
	g := s.generation
	if s.runtime.cleanupReceiptErr != nil {
		return taskpayload.JobGenerationCleanupReceipt{}, s.runtime.cleanupReceiptErr
	}
	if !s.closed || !s.sealed {
		return taskpayload.JobGenerationCleanupReceipt{}, errors.New("cleanup receipt preceded cleanup")
	}
	return taskpayload.JobGenerationCleanupReceipt{Version: 1, PipelineRunID: g.PipelineRunID, JobRunID: g.JobRunID, AgentID: g.AgentID, OriginalClaimID: g.OriginalClaimID, RuntimeBindingDigest: g.RuntimeBindingDigest, GenerationID: g.GenerationID, GenerationBindingDigest: g.BindingDigest, RuntimeClaimID: g.RuntimeClaimID, SessionID: g.RuntimeClaimID, Namespace: "fake-generation-test", ContainerID: "arcentra-" + g.RuntimeClaimID, SnapshotID: "arcentra-" + g.RuntimeClaimID + "-snapshot", Snapshotter: "fake", ResourceOwnerDigest: strings.Repeat("a", 64), WorkspaceID: "job-" + g.RuntimeClaimID, WorkspaceDevice: 1, WorkspaceInode: uint64(g.Number), ProcessesAbsent: true, ContainerAbsent: true, SnapshotAbsent: true, WorkspaceAbsent: true}, nil
}

// fakeGenerationClient 是直接 RPC 单元替身，严格复用已固定的不可变输入夹具
type fakeGenerationClient struct {
	*immutableInputFixtureClient
	t              *testing.T
	runtime        *fakeGenerationRuntime
	payload        *taskpayload.JobRunTaskPayload
	transitions    []taskpayload.JobGenerationTransition
	requests       []string
	generations    map[string]taskpayload.JobGeneration
	lostACKs       map[string]int
	delay          time.Duration
	inputErr       error
	mutateResponse func(*taskpayload.JobGeneration)
}

func (c *fakeGenerationClient) ReconcileJobGeneration(ctx context.Context, req *agentv1.ReconcileJobGenerationRequest, _ ...grpc.CallOption) (*agentv1.ReconcileJobGenerationResponse, error) {
	tr, err := taskpayload.ParseJobGenerationTransition(req.TransitionJson)
	if err != nil {
		return nil, err
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	claims := md.Get(executionClaimMetadataKey)
	if tr.Identity != c.identity || tr.RetryPlanDigest != c.payload.RetryPlanDigest || req.AgentId != c.identity.AgentID || len(claims) != 1 || claims[0] != c.identity.ClaimID {
		return nil, errors.New("fake generation RPC requires original dispatch and claim")
	}
	if len(c.runtime.intents) == 0 || c.runtime.intents[len(c.runtime.intents)-1].RequestDigest != tr.RequestDigest {
		return nil, errors.New("RPC preceded durable intent")
	}
	c.runtime.events = append(c.runtime.events, "rpc:"+tr.Operation)
	c.transitions = append(c.transitions, *tr)
	c.requests = append(c.requests, req.TransitionJson)
	g, exists := c.generations[tr.RequestDigest]
	if !exists {
		n := len(c.generations) + 1
		if n == 1 && tr.Operation != "admit" || n > 1 && tr.Operation != "advance" {
			return nil, errors.New("unexpected generation operation")
		}
		if n > 1 {
			if c.pinned == nil || tr.InputManifestDigest != c.pinned.ManifestDigest {
				return nil, errors.New("advance changed pinned inputs")
			}
			previous := c.runtime.sessions[n-2].generation
			if err := tr.CleanupReceipt.ValidateGeneration(previous); err != nil {
				return nil, err
			}
			if tr.PreviousGenerationID != previous.GenerationID {
				return nil, errors.New("advance changed predecessor")
			}
		}
		g = taskpayload.JobGeneration{Version: 1, PipelineID: tr.Identity.PipelineID, PipelineRunID: tr.Identity.PipelineRunID, JobRunID: tr.Identity.JobRunID, AgentID: tr.Identity.AgentID, OriginalClaimID: tr.Identity.ClaimID, RuntimeBindingDigest: tr.Identity.RuntimeBindingDigest, SourceBindingDigest: tr.Identity.SourceBindingDigest, InputManifestDigest: tr.InputManifestDigest, RetryPlanDigest: tr.RetryPlanDigest, GenerationID: id.GetUUID(), RuntimeClaimID: id.GetUUID(), Number: n, PreviousGenerationID: tr.PreviousGenerationID, ExecutionAllowed: true}
		if n == 1 {
			g.PreparationOnly = true
			g.ExecutionAllowed = true
		}
		if n > 1 && c.delay > 0 {
			g.NotBeforeUnixMilli = time.Now().Add(c.delay).UnixMilli()
		}
		g.BindingDigest, err = g.Digest()
		if err != nil {
			return nil, err
		}
		c.generations[tr.RequestDigest] = g
	}
	if c.lostACKs[tr.Operation] > 0 {
		c.lostACKs[tr.Operation]--
		return nil, status.Error(codes.Unavailable, "fake transition committed but ACK lost")
	}
	if c.mutateResponse != nil {
		c.mutateResponse(&g)
		g.BindingDigest, _ = g.Digest()
	}
	encoded, err := g.Canonical()
	if err != nil {
		return nil, err
	}
	return &agentv1.ReconcileJobGenerationResponse{GenerationJson: encoded}, nil
}
func (c *fakeGenerationClient) GetOrRecordJobInputs(ctx context.Context, req *agentv1.GetOrRecordJobInputsRequest, opts ...grpc.CallOption) (*agentv1.GetOrRecordJobInputsResponse, error) {
	if c.inputErr != nil {
		return nil, c.inputErr
	}
	return c.immutableInputFixtureClient.GetOrRecordJobInputs(ctx, req, opts...)
}
func bindFakeGenerationPlan(t *testing.T, p *taskpayload.JobRunTaskPayload) {
	t.Helper()
	plan, err := taskpayload.NewJobRetryPlan(p)
	if err != nil {
		t.Fatal(err)
	}
	p.RetryPlan = plan
	p.RetryPlanDigest, err = plan.Digest(p.PipelineID, p.PipelineRunID, p.JobRunID, p.AgentID, p.Runtime.BindingDigest)
	if err != nil {
		t.Fatal(err)
	}
}
func fakeGenerationFixture(t *testing.T, steps int) (*config.AgentConfig, *taskpayload.JobRunTaskPayload, *fakeGenerationRuntime, *fakeGenerationClient) {
	t.Helper()
	conf, p, _ := offlineWorkerFixture(t)
	conf.Agent.Sandbox.Containerd.Policy.AllowWholeJobRetry = true
	p.Retry = &pipelinev1.Retry{Mode: "whole_job", MaxAttempts: 3, Idempotent: true, AllowedExitCodes: []int32{9}}
	p.Steps = nil
	for i := 0; i < steps; i++ {
		ids := []string{id.GetUUID(), id.GetUUID(), id.GetUUID()}
		p.Steps = append(p.Steps, taskpayload.StepPayload{Name: fmt.Sprintf("step-%d", i), StepIndex: int32(i), Uses: "shell", StepRunID: ids[0], AttemptRunIDs: ids, Args: map[string]any{"run": "test-only fake command"}})
	}
	bindFakeGenerationPlan(t, p)
	r := &fakeGenerationRuntime{t: t, root: t.TempDir(), drain: make(chan struct{}), ready: true}
	c := &fakeGenerationClient{immutableInputFixtureClient: newImmutableInputFixtureClient(p, &artifactStatusClient{}), t: t, runtime: r, payload: p, generations: map[string]taskpayload.JobGeneration{}, lostACKs: map[string]int{}}
	t.Cleanup(func() {
		pendingJobReports.Delete(receiptKey(context.Background(), "job", p.JobRunID))
		for _, step := range p.Steps {
			for _, sid := range step.AttemptRunIDs {
				pendingStepReports.Delete(receiptKey(context.Background(), "step", sid))
			}
		}
		cancelledJobs.Delete(p.JobRunID)
	})
	return conf, p, r, c
}
func runFakeGeneration(conf *config.AgentConfig, p *taskpayload.JobRunTaskPayload, r *fakeGenerationRuntime, c *fakeGenerationClient) error {
	return executeJobRun(withOfflineJobRuntime(context.Background(), r), conf, &agentgrpc.ClientWrapper{AgentClient: c}, executor.NewExecutorManager(), p)
}
func failFirstFakeGeneration(s *fakeGenerationSession, _ *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
	if s.generation.Number == 1 {
		return fakeGenerationResult(s, 9, &sandboxexec.CommandExitError{Code: 9})
	}
	return fakeGenerationResult(s, 0, nil)
}

func TestContainerGenerationFakeRuntimeFreshRoundAndExactOriginalIdentity(t *testing.T) {
	conf, p, r, c := fakeGenerationFixture(t, 3)
	r.execute = func(s *fakeGenerationSession, req *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		if req.Env[taskpayload.RuntimeGenerationEnv] != fmt.Sprint(s.generation.Number) {
			t.Fatal("trusted round variable missing")
		}
		if req.StepIndex == 0 {
			if _, err := os.Stat(filepath.Join(s.workspace, "previous-round")); !os.IsNotExist(err) {
				t.Fatal("round reused old workspace")
			}
			artifactTestWrite(t, s.workspace, "previous-round", "output")
		}
		if req.StepIndex == 1 && s.generation.Number == 1 {
			return fakeGenerationResult(s, 9, &sandboxexec.CommandExitError{Code: 9})
		}
		return fakeGenerationResult(s, 0, nil)
	}
	if err := runFakeGeneration(conf, p, r, c); err != nil {
		t.Fatal(err)
	}
	if r.allocations != 2 || r.acknowledgements != 1 || len(r.intents) != 2 {
		t.Fatalf("lifecycle allocations=%d acknowledgements=%d intents=%d", r.allocations, r.acknowledgements, len(r.intents))
	}
	first, second := r.sessions[0], r.sessions[1]
	if first.workspace == second.workspace || first.generation.GenerationID == second.generation.GenerationID || first.generation.RuntimeClaimID == second.generation.RuntimeClaimID {
		t.Fatal("round identities reused")
	}
	if first.executions != 2 || second.executions != 3 {
		t.Fatalf("whole-job command counts: %d %d", first.executions, second.executions)
	}
	for i, s := range r.sessions {
		for _, req := range s.requests {
			if req.StepRunID != p.Steps[req.StepIndex].AttemptRunIDs[i] {
				t.Fatal("round did not use preallocated attempt identity")
			}
		}
	}
	if c.pinned == nil || r.intents[1].InputManifestDigest != c.pinned.ManifestDigest || r.intents[0].Identity != r.intents[1].Identity || r.intents[1].RetryPlanDigest != p.RetryPlanDigest {
		t.Fatal("retry changed immutable inputs, full plan or original claim")
	}
	if !reflect.DeepEqual(r.events, []string{"intent:admit", "rpc:admit", "admit:1", "allocate:1", "intent:advance", "rpc:advance", "admit:2", "allocate:2"}) {
		t.Fatalf("ordering: %v", r.events)
	}
	terminal := sourceTestTerminal(t, c.artifactStatusClient)
	if terminal.Status != 4 || terminal.GenerationId != second.generation.GenerationID {
		t.Fatalf("final round receipt: %+v", terminal)
	}
	var pure int
	for _, req := range c.stepReports {
		if req.RetryableShellExit {
			pure++
			if req.StepRunId != p.Steps[1].AttemptRunIDs[0] || req.ExitCode != 9 {
				t.Fatal("retry evidence selected wrong failure")
			}
		}
	}
	if pure != 1 {
		t.Fatalf("retryable failure receipts=%d", pure)
	}
	if err := taskpayload.ValidateJobRetryPlan(p); err != nil {
		t.Fatalf("round execution mutated full payload: %v", err)
	}
}

func TestContainerGenerationFakeRuntimeOnlyAllowedPureShellExitRetries(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		code                   int32
		err                    error
		continueOnError        bool
		wantRounds, wantStatus int
	}{
		{"allowed", 9, &sandboxexec.CommandExitError{Code: 9}, false, 2, 4},
		{"disallowed", 7, &sandboxexec.CommandExitError{Code: 7}, false, 1, 5},
		{"mixed_runtime", 9, errors.Join(&sandboxexec.CommandExitError{Code: 9}, errors.New("runtime IO failed")), false, 1, 5},
		{"mixed_codes", 9, errors.Join(&sandboxexec.CommandExitError{Code: 9}, &sandboxexec.CommandExitError{Code: 7}), false, 1, 5},
		{"generic_exit_text", 9, errors.New("process exited with code 9"), false, 1, 5},
		{"continue_on_error", 9, &sandboxexec.CommandExitError{Code: 9}, true, 1, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conf, p, r, c := fakeGenerationFixture(t, 1)
			p.Steps[0].ContinueOnError = tc.continueOnError
			bindFakeGenerationPlan(t, p)
			r.execute = func(s *fakeGenerationSession, _ *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
				if s.generation.Number == 1 {
					return fakeGenerationResult(s, tc.code, tc.err)
				}
				return fakeGenerationResult(s, 0, nil)
			}
			err := runFakeGeneration(conf, p, r, c)
			if tc.wantStatus == 4 && err != nil {
				t.Fatal(err)
			}
			if tc.wantStatus == 5 && err == nil {
				t.Fatal("failure accepted")
			}
			if r.allocations != tc.wantRounds || int(sourceTestTerminal(t, c.artifactStatusClient).Status) != tc.wantStatus {
				t.Fatalf("rounds=%d terminal=%+v err=%v", r.allocations, sourceTestTerminal(t, c.artifactStatusClient), err)
			}
			if tc.wantRounds == 1 {
				for _, req := range c.transitions {
					if req.Operation == "advance" {
						t.Fatal("ineligible error advanced generation")
					}
				}
			}
		})
	}
}

func TestContainerGenerationFakeRuntimePreparationAndCleanupFailClosed(t *testing.T) {
	for _, name := range []string{"input", "download", "start", "seal", "close", "cleanup_receipt"} {
		t.Run(name, func(t *testing.T) {
			conf, p, r, c := fakeGenerationFixture(t, 1)
			r.execute = failFirstFakeGeneration
			failure := errors.New("synthetic " + name + " failure")
			switch name {
			case "input":
				c.inputErr = failure
			case "download":
				p.ArtifactURIs = map[string]string{"upstream/input": "object"}
				p.ArtifactReferences = artifactTestReferences(p.ArtifactURIs, map[string][]byte{"object": []byte("fixed input")})
				bindFakeGenerationPlan(t, p)
				c.immutableInputFixtureClient = newImmutableInputFixtureClient(p, c.artifactStatusClient)
				SetStorage(&memoryArtifactStorage{download: func(context.Context, string) (io.ReadCloser, error) { return nil, failure }}, artifactTestReference())
				t.Cleanup(func() { SetStorage(nil) })
			case "start":
				r.sessionStartErr = failure
			case "seal":
				r.sessionSealErr = failure
			case "close":
				r.sessionCloseErr = failure
			case "cleanup_receipt":
				r.cleanupReceiptErr = failure
			}
			if err := runFakeGeneration(conf, p, r, c); err == nil {
				t.Fatal("failure accepted")
			}
			if r.allocations != 1 || len(c.generations) != 1 {
				t.Fatalf("failure advanced generation: %d %d", r.allocations, len(c.generations))
			}
			if name == "input" || name == "download" || name == "start" {
				if r.sessions[0].executions != 0 {
					t.Fatal("command executed after preparation failure")
				}
			}
			if got := sourceTestTerminal(t, c.artifactStatusClient).Status; got != 5 {
				t.Fatalf("failure terminal=%d", got)
			}
		})
	}
}

func TestContainerGenerationFakeRuntimeTransitionACKLossAllocatesOnce(t *testing.T) {
	for _, operation := range []string{"admit", "advance"} {
		t.Run(operation, func(t *testing.T) {
			conf, p, r, c := fakeGenerationFixture(t, 1)
			r.execute = failFirstFakeGeneration
			c.lostACKs[operation] = 1
			if err := runFakeGeneration(conf, p, r, c); err != nil {
				t.Fatal(err)
			}
			if len(c.generations) != 2 || r.allocations != 2 || len(r.admissions) != 2 {
				t.Fatalf("ACK loss allocated twice: generations=%d allocations=%d admissions=%d", len(c.generations), r.allocations, len(r.admissions))
			}
			var encoded []string
			for i, tr := range c.transitions {
				if tr.Operation == operation {
					encoded = append(encoded, c.requests[i])
				}
			}
			if len(encoded) != 2 || encoded[0] != encoded[1] {
				t.Fatal("RPC retry changed exact transition")
			}
		})
	}
}

func TestContainerGenerationFakeRuntimeFinalACKReplayDoesNotExecuteTwice(t *testing.T) {
	for _, kind := range []string{"job", "step"} {
		t.Run(kind, func(t *testing.T) {
			conf, p, r, c := fakeGenerationFixture(t, 1)
			if kind == "job" {
				c.failJobTerminals = 1
			} else {
				c.failStepID = p.Steps[0].StepRunID
				c.failStepTerminals = 2
			}
			if err := runFakeGeneration(conf, p, r, c); err == nil {
				t.Fatal("terminal ACK outage not exercised")
			}
			if r.acknowledgements != 0 {
				t.Fatal("journal acknowledged before terminal delivery")
			}
			calls := r.sessions[0].executions
			allocations := r.allocations
			intents := len(r.intents)
			err := runFakeGeneration(conf, p, r, c)
			if acknowledgeCompleted(err) != nil {
				t.Fatalf("terminal replay failed: %v", err)
			}
			if r.sessions[0].executions != calls || r.allocations != allocations || len(r.intents) != intents || r.acknowledgements != 1 {
				t.Fatal("terminal replay repeated execution or transition")
			}
			var reports []proto.Message
			if kind == "job" {
				for _, req := range c.jobReports {
					if req.Status >= 4 {
						reports = append(reports, req)
					}
				}
			} else {
				for _, req := range c.stepReports {
					if req.StepRunId == p.Steps[0].StepRunID && req.Status >= 4 {
						reports = append(reports, req)
					}
				}
			}
			if len(reports) < 2 {
				t.Fatal("terminal receipt was not retried")
			}
			for _, req := range reports[1:] {
				if !proto.Equal(reports[0], req) {
					t.Fatalf("replay changed terminal receipt: first=%v replay=%v", reports[0], req)
				}
			}
		})
	}
}

func TestContainerGenerationFakeRuntimeCancelAndDrainAdmissionGaps(t *testing.T) {
	for _, name := range []string{"cancel_delay", "drain_delay", "cancel_admission", "drain_admission", "cancel_allocate", "drain_allocate"} {
		t.Run(name, func(t *testing.T) {
			conf, p, r, c := fakeGenerationFixture(t, 1)
			r.execute = failFirstFakeGeneration
			if strings.HasSuffix(name, "delay") {
				c.delay = time.Hour
			}
			action := func(g taskpayload.JobGeneration) {
				if g.Number == 2 {
					if strings.HasPrefix(name, "cancel") {
						CancelJobRun(p.JobRunID)
					} else {
						r.beginDrain()
					}
				}
			}
			if strings.HasSuffix(name, "allocate") {
				r.beforeAllocation = action
			} else {
				r.afterAdmission = action
			}
			if err := runFakeGeneration(conf, p, r, c); err == nil {
				t.Fatal("interrupted admission succeeded")
			}
			if r.allocations != 1 || len(r.sessions) != 1 || r.sessions[0].executions != 1 {
				t.Fatalf("command admitted through stop gap: allocations=%d", r.allocations)
			}
			if len(r.admissions) != 2 || len(r.intents) != 2 {
				t.Fatal("received generation was not retained before stop")
			}
			want := int32(5)
			if strings.HasPrefix(name, "cancel") {
				want = 6
			}
			if got := sourceTestTerminal(t, c.artifactStatusClient).Status; got != want {
				t.Fatalf("terminal=%d want=%d", got, want)
			}
		})
	}
}

func TestContainerGenerationFakeRuntimeInitialRPCAmbiguityPreservesIntent(t *testing.T) {
	conf, p, r, c := fakeGenerationFixture(t, 1)
	c.lostACKs["admit"] = 3
	err := runFakeGeneration(conf, p, r, c)
	var delivery *terminalDeliveryError
	if !errors.As(err, &delivery) {
		t.Fatalf("unresolved admission did not stay pending: %v", err)
	}
	if len(r.intents) != 1 || len(c.generations) != 1 || r.allocations != 0 || r.acknowledgements != 0 || len(r.admissions) != 0 {
		t.Fatalf("ambiguous admission state: intents=%d generations=%d allocations=%d ACKs=%d admissions=%d", len(r.intents), len(c.generations), r.allocations, r.acknowledgements, len(r.admissions))
	}
	for _, req := range c.jobReports {
		if req.Status >= 4 {
			t.Fatal("uncertain first generation was finalized")
		}
	}
	// A real restart marks the durable unresolved intent unready; this fake models
	// that contract without claiming to test filesystem recovery.
	r.ready = false
	if err := runFakeGeneration(conf, p, r, c); err == nil {
		t.Fatal("unresolved restart automatically resumed")
	}
	if len(c.requests) != 3 || r.allocations != 0 {
		t.Fatal("restart retried admission or execution before reconciliation")
	}
}

func TestContainerGenerationFakeRuntimeRejectsInspectAndMismatchedResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*taskpayload.JobGeneration)
	}{
		{"inspect_only", func(g *taskpayload.JobGeneration) { g.ExecutionAllowed = false }},
		{"foreign_agent", func(g *taskpayload.JobGeneration) { g.AgentID = "other-agent" }},
		{"foreign_original_claim", func(g *taskpayload.JobGeneration) { g.OriginalClaimID = "other-claim" }},
		{"foreign_runtime_binding", func(g *taskpayload.JobGeneration) { g.RuntimeBindingDigest = "v1:" + strings.Repeat("b", 64) }},
		{"foreign_input_digest", func(g *taskpayload.JobGeneration) { g.InputManifestDigest = "v1:" + strings.Repeat("b", 64) }},
		{"foreign_full_plan", func(g *taskpayload.JobGeneration) { g.RetryPlanDigest = "v1:" + strings.Repeat("b", 64) }},
		{"wrong_ordinal", func(g *taskpayload.JobGeneration) {
			g.Number = 2
			g.PreviousGenerationID = id.GetUUID()
			g.InputManifestDigest = "v1:" + strings.Repeat("b", 64)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conf, p, r, c := fakeGenerationFixture(t, 1)
			c.mutateResponse = tc.mutate
			if err := runFakeGeneration(conf, p, r, c); err == nil {
				t.Fatal("untrusted generation response accepted")
			}
			if r.allocations != 0 || len(r.admissions) != 0 || r.acknowledgements != 0 {
				t.Fatalf("response authorized execution or journal completion: allocations=%d admissions=%d ACKs=%d", r.allocations, len(r.admissions), r.acknowledgements)
			}
			if len(r.intents) != 1 {
				t.Fatal("unresolved intent not retained")
			}
			for _, req := range c.jobReports {
				if req.Status >= 4 {
					t.Fatal("response mismatch prematurely finalized uncertain admission")
				}
			}
		})
	}
}

func TestContainerGenerationFakeRuntimeRestagesFixedInputsWithoutOldOutputs(t *testing.T) {
	conf, p, r, c := fakeGenerationFixture(t, 2)
	p.JobName = "build"
	p.ArtifactURIs = map[string]string{"upstream/input": "input-object"}
	objects := map[string][]byte{"input-object": []byte("immutable original")}
	p.ArtifactReferences = artifactTestReferences(p.ArtifactURIs, objects)
	p.Steps[1].Uses = "builtin:artifacts"
	p.Steps[1].Action = "upload"
	p.Steps[1].Args = map[string]any{"paths": []string{"dist"}}
	bindFakeGenerationPlan(t, p)
	c.immutableInputFixtureClient = newImmutableInputFixtureClient(p, c.artifactStatusClient)
	store := &memoryArtifactStorage{objects: objects}
	SetStorage(store, artifactTestReference())
	t.Cleanup(func() { SetStorage(nil) })
	r.execute = func(s *fakeGenerationSession, _ *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		if got := artifactTestRead(t, s.workspace, ".artifacts/upstream/input"); got != "immutable original" {
			t.Fatalf("input was not freshly staged: %q", got)
		}
		if s.generation.Number == 1 {
			artifactTestWrite(t, s.workspace, ".artifacts/upstream/input", "mutated by failed command")
			artifactTestWrite(t, s.workspace, "dist/abandoned", "old round")
			return fakeGenerationResult(s, 9, &sandboxexec.CommandExitError{Code: 9})
		}
		if _, err := os.Stat(filepath.Join(s.workspace, "dist/abandoned")); !os.IsNotExist(err) {
			t.Fatal("failed-round output leaked")
		}
		artifactTestWrite(t, s.workspace, "dist/final", "final round")
		return fakeGenerationResult(s, 0, nil)
	}
	if err := runFakeGeneration(conf, p, r, c); err != nil {
		t.Fatal(err)
	}
	if store.downloads != 2 || store.uploads != 1 {
		t.Fatalf("fresh input/output transfers: downloads=%d uploads=%d", store.downloads, store.uploads)
	}
	terminal := sourceTestTerminal(t, c.artifactStatusClient)
	key, ok := terminal.ArtifactUris["dist/final"]
	if len(terminal.ArtifactUris) != 1 || !ok || !strings.Contains(key, "/"+p.Steps[1].AttemptRunIDs[1]+"/") || string(store.objects[key]) != "final round" {
		t.Fatalf("final output provenance leaked a prior round: %+v", terminal)
	}
	if len(terminal.Artifacts) != 1 || terminal.Artifacts[0].StepRunId != p.Steps[1].AttemptRunIDs[1] {
		t.Fatalf("artifact metadata not owned by final admitted round: %+v", terminal.Artifacts)
	}
	if c.pinned == nil || r.intents[1].InputManifestDigest != c.pinned.ManifestDigest {
		t.Fatal("restaging replaced immutable input manifest")
	}
}

func TestContainerGenerationFakeRuntimePinnedInputDriftBlocksAdvance(t *testing.T) {
	conf, p, r, c := fakeGenerationFixture(t, 1)
	r.execute = func(s *fakeGenerationSession, _ *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		if c.pinned == nil {
			t.Fatal("command started before input pin")
		}
		c.pinned.ManifestDigest = "v1:" + strings.Repeat("b", 64)
		return fakeGenerationResult(s, 9, &sandboxexec.CommandExitError{Code: 9})
	}
	if err := runFakeGeneration(conf, p, r, c); err == nil {
		t.Fatal("changed pin accepted")
	}
	if r.allocations != 1 || len(c.transitions) != 1 {
		t.Fatal("immutable input drift reached generation advance")
	}
}

func TestContainerGenerationFakeRuntimeFailedStepACKLossFailsClosedAndReplaysExactly(t *testing.T) {
	conf, p, r, c := fakeGenerationFixture(t, 1)
	r.execute = failFirstFakeGeneration
	c.failStepID = p.Steps[0].StepRunID
	c.failStepTerminals = 2
	if err := runFakeGeneration(conf, p, r, c); err == nil {
		t.Fatal("step receipt ACK outage not exercised")
	}
	if r.allocations != 1 || len(c.transitions) != 1 || r.acknowledgements != 0 {
		t.Fatal("unacknowledged failure admitted another round")
	}
	if err := runFakeGeneration(conf, p, r, c); acknowledgeCompleted(err) != nil {
		t.Fatalf("failed receipt replay did not finish: %v", err)
	}
	if r.allocations != 1 || r.sessions[0].executions != 1 || r.acknowledgements != 1 {
		t.Fatal("failed receipt replay resumed execution")
	}
	var previous *agentv1.ReportStepRunStatusRequest
	var count int
	for _, req := range c.stepReports {
		if req.StepRunId == p.Steps[0].StepRunID && req.Status >= 4 {
			count++
			if !req.RetryableShellExit || req.GenerationId != r.admissions[0].GenerationID {
				t.Fatal("retry failure identity was lost during delivery")
			}
			if previous != nil && !proto.Equal(previous, req) {
				t.Fatal("failed terminal receipt changed on replay")
			}
			previous = req
		}
	}
	if count != 3 || sourceTestTerminal(t, c.artifactStatusClient).Status != 5 {
		t.Fatalf("failed replay terminal count=%d job=%+v", count, sourceTestTerminal(t, c.artifactStatusClient))
	}
}

func TestContainerGenerationFakeRuntimeDurableTerminalReplayFreezesGeneration(t *testing.T) {
	for _, kind := range []string{"job", "step"} {
		t.Run(kind, func(t *testing.T) {
			conf, p, r, c := fakeGenerationFixture(t, 1)
			conf.Grpc.ServerAddr = "synthetic-generation-controller"
			box, err := newTerminalOutbox(context.Background(), t.TempDir(), conf.Agent.ID, conf.Grpc.ServerAddr)
			if err != nil {
				t.Fatal(err)
			}
			defer box.close()
			ctx := withTerminalOutbox(withOfflineJobRuntime(context.Background(), r), box)
			t.Cleanup(func() {
				pendingJobReports.Delete(receiptKey(ctx, "job", p.JobRunID))
				for _, step := range p.Steps {
					for _, sid := range step.AttemptRunIDs {
						pendingStepReports.Delete(receiptKey(ctx, "step", sid))
					}
				}
			})
			wrapper := &agentgrpc.ClientWrapper{AgentClient: c}
			if kind == "job" {
				c.failJobTerminals = 1
			} else {
				c.failStepID = p.Steps[0].StepRunID
				c.failStepTerminals = 2
			}
			execute := func() error { return executeJobRun(ctx, conf, wrapper, executor.NewExecutorManager(), p) }
			if err := execute(); err == nil {
				t.Fatal("terminal delivery outage not exercised")
			}
			allocations, executions, intents := r.allocations, r.sessions[0].executions, len(r.intents)
			if err := box.replayBound(ctx, wrapper, conf); err != nil {
				t.Fatal(err)
			}
			// The background durable replay may clear the process-local claim before
			// the retained job receipt retries its runtime completion acknowledgement.
			completeExecutionClaim(ctx, "job", p.JobRunID)
			if err := execute(); acknowledgeCompleted(err) != nil {
				t.Fatalf("pending receipt did not complete: %v", err)
			}
			if allocations != r.allocations || executions != r.sessions[0].executions || intents != len(r.intents) || r.acknowledgements != 1 {
				t.Fatal("durable replay repeated command, admission or allocation")
			}
			generation := r.admissions[0]
			var baseline proto.Message
			verify := func(message proto.Message) {
				if baseline == nil {
					baseline = message
				} else if !proto.Equal(baseline, message) {
					t.Fatalf("durable terminal replay changed bytes: %v versus %v", baseline, message)
				}
			}
			if kind == "job" {
				for _, req := range c.jobReports {
					if req.Status >= 4 {
						if req.GenerationId != generation.GenerationID || req.GenerationBindingDigest != generation.BindingDigest || req.RuntimeBindingDigest != p.Runtime.BindingDigest {
							t.Fatal("durable job replay changed bound generation")
						}
						verify(req)
					}
				}
			} else {
				for _, req := range c.stepReports {
					if req.StepRunId == p.Steps[0].StepRunID && req.Status >= 4 {
						if req.GenerationId != generation.GenerationID || req.GenerationBindingDigest != generation.BindingDigest {
							t.Fatal("durable step replay changed bound generation")
						}
						verify(req)
					}
				}
			}
			box.mu.Lock()
			remaining := len(box.entries)
			box.mu.Unlock()
			if remaining != 0 {
				t.Fatalf("terminal outbox retained %d acknowledged entries", remaining)
			}
		})
	}
}
