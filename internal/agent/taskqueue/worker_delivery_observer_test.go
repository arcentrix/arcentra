// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Test-only transparent observation: no handler result, RPC result or scheduling
// is changed. Background replay remains enabled and is explicitly attributed.
type workerDeliveryContextKey struct{}
type workerDeliveryBackgroundKey struct{}
type workerDeliveryInvocation struct {
	task          nova.Task
	manual        bool
	begin, end    int
	code          codes.Code
	deliveryError bool
}
type workerDeliveryRPC struct {
	origin     string
	invocation int
	begin, end int
	claim      string
	payload    []byte
	message    proto.Message
	code       codes.Code
	success    bool
}
type workerDeliverySnapshot struct {
	original                                    []nova.Task
	invocations                                 []workerDeliveryInvocation
	calls                                       []workerDeliveryRPC
	stepCalls                                   []workerDeliveryRPC
	advanceBoundary                             int
	advanceInvocation                           int
	denialBoundary, denialEnd, denialInvocation int
	denialCode                                  codes.Code
	denialRequest                               retryapproval.Request
}
type workerDeliveryObserver struct {
	mu                   sync.Mutex
	targetName, targetID string
	failureIndex         int
	failureID            string
	clock                int
	manual               bool
	state                workerDeliverySnapshot
}

func (o *workerDeliveryObserver) matches(task *nova.Task) bool {
	if task == nil || task.Type != taskpayload.TaskTypeJobRun {
		return false
	}
	var p taskpayload.JobRunTaskPayload
	if sonic.Unmarshal(task.Payload, &p) != nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.targetID != "" && p.JobRunID != o.targetID || o.targetID == "" && (o.targetName == "" || p.JobName != o.targetName) {
		return false
	}
	o.targetID = p.JobRunID
	if o.failureID == "" && o.failureIndex >= 0 && o.failureIndex < len(p.Steps) {
		ids := stepAttemptIDs(p.Steps[o.failureIndex])
		if len(ids) > 0 {
			o.failureID = ids[0]
		}
	}
	return true
}
func (o *workerDeliveryObserver) original(task *nova.Task) {
	if !o.matches(task) {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.state.original = append(o.state.original, nova.Task{Type: task.Type, Payload: bytes.Clone(task.Payload)})
}
func (o *workerDeliveryObserver) wrap(handler nova.IHandler) nova.HandlerFunc {
	return func(ctx context.Context, task *nova.Task) error {
		if !o.matches(task) {
			return handler.ProcessTask(ctx, task)
		}
		o.mu.Lock()
		o.clock++
		n := len(o.state.invocations)
		o.state.invocations = append(o.state.invocations, workerDeliveryInvocation{task: nova.Task{Type: task.Type, Payload: bytes.Clone(task.Payload)}, manual: o.manual, begin: o.clock})
		o.mu.Unlock()
		err := handler.ProcessTask(context.WithValue(ctx, workerDeliveryContextKey{}, n+1), task)
		var delivery *terminalDeliveryError
		o.mu.Lock()
		o.clock++
		o.state.invocations[n].end = o.clock
		o.state.invocations[n].code = status.Code(err)
		o.state.invocations[n].deliveryError = errors.As(err, &delivery)
		o.mu.Unlock()
		return err
	}
}
func (o *workerDeliveryObserver) backgroundContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, workerDeliveryBackgroundKey{}, true)
}
func (o *workerDeliveryObserver) snapshot() workerDeliverySnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	return workerDeliverySnapshot{original: append([]nova.Task(nil), o.state.original...), invocations: append([]workerDeliveryInvocation(nil), o.state.invocations...), calls: append([]workerDeliveryRPC(nil), o.state.calls...), stepCalls: append([]workerDeliveryRPC(nil), o.state.stepCalls...), advanceBoundary: o.state.advanceBoundary, advanceInvocation: o.state.advanceInvocation, denialBoundary: o.state.denialBoundary, denialEnd: o.state.denialEnd, denialInvocation: o.state.denialInvocation, denialCode: o.state.denialCode, denialRequest: o.state.denialRequest}
}
func (o *workerDeliveryObserver) markManual() { o.mu.Lock(); o.manual = true; o.mu.Unlock() }

type workerDeliveryQueue struct {
	nova.TaskQueue
	observer *workerDeliveryObserver
}

func (q *workerDeliveryQueue) Enqueue(task *nova.Task, opts ...nova.Option) (*nova.Result, error) {
	result, err := q.TaskQueue.Enqueue(task, opts...)
	if err == nil {
		q.observer.original(task)
	}
	return result, err
}

type workerDeliveryClient struct {
	agentv1.AgentServiceClient
	observer *workerDeliveryObserver
}

func (c *workerDeliveryClient) ReportJobRunStatus(ctx context.Context, request *agentv1.ReportJobRunStatusRequest, opts ...grpc.CallOption) (*agentv1.ReportJobRunStatusResponse, error) {
	o := c.observer
	o.mu.Lock()
	target := o.targetID
	o.mu.Unlock()
	if request.Status < 4 || target == "" || request.JobRunId != target {
		return c.AgentServiceClient.ReportJobRunStatus(ctx, request, opts...)
	}
	invocation, _ := ctx.Value(workerDeliveryContextKey{}).(int)
	background, _ := ctx.Value(workerDeliveryBackgroundKey{}).(bool)
	local, completion := ctx.Value(receiptJobClaimContextKey{}).(receiptJobClaim)
	origin := "unattributed"
	if invocation > 0 && !background {
		origin = "preflight"
		if completion && local.runID == target && local.claimID != "" {
			origin = "completion"
		}
	} else if invocation == 0 && background && !completion {
		origin = "background"
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	claim := ""
	if claims := md.Get(executionClaimMetadataKey); len(claims) == 1 {
		claim = claims[0]
	}
	payload, _ := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	o.mu.Lock()
	o.clock++
	n := len(o.state.calls)
	o.state.calls = append(o.state.calls, workerDeliveryRPC{origin: origin, invocation: invocation, begin: o.clock, claim: claim, payload: payload, message: proto.Clone(request).(*agentv1.ReportJobRunStatusRequest)})
	o.mu.Unlock()
	response, err := c.AgentServiceClient.ReportJobRunStatus(ctx, request, opts...)
	o.mu.Lock()
	o.clock++
	o.state.calls[n].end = o.clock
	o.state.calls[n].code = status.Code(err)
	o.state.calls[n].success = err == nil && response != nil && response.Success
	o.mu.Unlock()
	return response, err
}

func (c *workerDeliveryClient) ReportStepRunStatus(ctx context.Context, request *agentv1.ReportStepRunStatusRequest, opts ...grpc.CallOption) (*agentv1.ReportStepRunStatusResponse, error) {
	o := c.observer
	o.mu.Lock()
	target, failedStep := o.targetID, o.failureID
	o.mu.Unlock()
	if !request.RetryableShellExit || failedStep == "" || request.StepRunId != failedStep {
		return c.AgentServiceClient.ReportStepRunStatus(ctx, request, opts...)
	}
	invocation, _ := ctx.Value(workerDeliveryContextKey{}).(int)
	background, _ := ctx.Value(workerDeliveryBackgroundKey{}).(bool)
	attempt := stepAttemptFromContext(ctx)
	origin := "unattributed"
	if invocation == 1 && !background && attempt != nil && attempt.admission.Scope.Identity.JobRunID == target && attempt.admission.Scope.StepRunID == failedStep && attempt.admission.BindingDigest == request.StepAttemptAdmissionDigest {
		origin = "step-completion"
	} else if invocation == 0 && background && attempt == nil {
		origin = "background"
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	claim := ""
	if claims := md.Get(executionClaimMetadataKey); len(claims) == 1 {
		claim = claims[0]
	}
	payload, _ := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	o.mu.Lock()
	o.clock++
	n := len(o.state.stepCalls)
	o.state.stepCalls = append(o.state.stepCalls, workerDeliveryRPC{origin: origin, invocation: invocation, begin: o.clock, claim: claim, payload: payload, message: proto.Clone(request)})
	o.mu.Unlock()
	response, err := c.AgentServiceClient.ReportStepRunStatus(ctx, request, opts...)
	o.mu.Lock()
	o.clock++
	o.state.stepCalls[n].end = o.clock
	o.state.stepCalls[n].code = status.Code(err)
	o.state.stepCalls[n].success = err == nil && response != nil && response.Success
	o.mu.Unlock()
	return response, err
}
func (c *workerDeliveryClient) ReconcileStepAttempt(ctx context.Context, request *agentv1.ReconcileStepAttemptRequest, opts ...grpc.CallOption) (*agentv1.ReconcileStepAttemptResponse, error) {
	transition, err := taskpayload.ParseStepAttemptTransition(request.TransitionJson)
	if err == nil && transition.Operation == "advance" {
		o := c.observer
		o.mu.Lock()
		if transition.Scope.Identity.JobRunID == o.targetID && o.state.advanceBoundary == 0 {
			o.clock++
			o.state.advanceBoundary = o.clock
			o.state.advanceInvocation, _ = ctx.Value(workerDeliveryContextKey{}).(int)
		}
		o.mu.Unlock()
	}
	return c.AgentServiceClient.ReconcileStepAttempt(ctx, request, opts...)
}

func (c *workerDeliveryClient) ReconcileJobRetryApproval(ctx context.Context, request *agentv1.ReconcileJobRetryApprovalRequest, opts ...grpc.CallOption) (*agentv1.ReconcileJobRetryApprovalResponse, error) {
	parsed, parseErr := retryapproval.ParseRequest(request.RequestJson)
	o := c.observer
	o.mu.Lock()
	target := o.targetID
	o.mu.Unlock()
	if parseErr != nil || parsed.Operation != "admit" || parsed.Identity.JobRunID != target {
		return c.AgentServiceClient.ReconcileJobRetryApproval(ctx, request, opts...)
	}
	o.mu.Lock()
	o.clock++
	begin := o.clock
	o.mu.Unlock()
	response, err := c.AgentServiceClient.ReconcileJobRetryApproval(ctx, request, opts...)
	if status.Code(err) == codes.FailedPrecondition {
		o.mu.Lock()
		o.clock++
		if o.state.denialBoundary == 0 {
			o.state.denialBoundary = begin
			o.state.denialEnd = o.clock
			o.state.denialInvocation, _ = ctx.Value(workerDeliveryContextKey{}).(int)
			o.state.denialCode = status.Code(err)
			o.state.denialRequest = *parsed
		}
		o.mu.Unlock()
	}
	return response, err
}

func (s workerDeliverySnapshot) validateStep(receipts []controllerWorkerReceipt, transition taskpayload.StepAttemptTransition) error {
	return s.validateStepBeforeBoundary(receipts, transition, s.advanceBoundary, s.advanceInvocation)
}

// validateDeniedStep 仅验证已持久失败与随后拒绝边界，不合成新的步骤准入权限
func (s workerDeliverySnapshot) validateDeniedStep(receipts []controllerWorkerReceipt, admission taskpayload.StepAttemptAdmission, digest string, injectedLosses int) error {
	if injectedLosses != 1 || s.advanceBoundary != 0 || s.denialCode != codes.FailedPrecondition || s.denialEnd <= s.denialBoundary || len(s.original) != 1 || len(s.invocations) != 1 || s.invocations[0].manual || s.invocations[0].end <= s.denialEnd {
		return fmt.Errorf("denied step lacks one exact loss, original handler or completed denial boundary")
	}
	request := s.denialRequest
	if request.Operation != "admit" || request.Identity != admission.Scope.Identity || request.GenerationID != admission.Scope.GenerationID || request.GenerationBindingDigest != admission.Scope.GenerationBindingDigest || request.InputManifestDigest != admission.Scope.InputManifestDigest || request.RetryPlanDigest != admission.Scope.RetryPlanDigest {
		return fmt.Errorf("denial changed original generation/input authority request")
	}
	if !bytes.Equal(s.original[0].Payload, s.invocations[0].task.Payload) || s.original[0].Type != s.invocations[0].task.Type {
		return fmt.Errorf("denied replay changed queued task")
	}
	for _, call := range s.stepCalls {
		message, ok := call.message.(*agentv1.ReportStepRunStatusRequest)
		if !ok || int32(message.Status) != 5 || message.AgentId != admission.Scope.Identity.AgentID || message.GenerationId != admission.Scope.GenerationID || message.GenerationBindingDigest != admission.Scope.GenerationBindingDigest {
			return fmt.Errorf("denied receipt changed failed status/generation/agent")
		}
	}
	binding := taskpayload.StepAttemptTransition{Scope: admission.Scope, PreviousStepRunID: admission.Scope.StepRunID, PreviousStepClaimID: admission.Scope.StepClaimID, PreviousAdmissionBindingDigest: admission.BindingDigest, FailedStepReceiptDigest: digest}
	return s.validateStepBeforeBoundary(receipts, binding, s.denialBoundary, s.denialInvocation)
}

func (s workerDeliverySnapshot) validateStepBeforeBoundary(receipts []controllerWorkerReceipt, transition taskpayload.StepAttemptTransition, boundary, invocation int) error {
	calls := s.stepCalls
	if len(calls) < 2 || len(calls) != len(receipts) || boundary == 0 || invocation != 1 || len(s.invocations) == 0 {
		return fmt.Errorf("failed-step RPC/commit/execution boundary missing")
	}
	if s.invocations[0].end != 0 && boundary >= s.invocations[0].end {
		return fmt.Errorf("execution boundary escaped the original handler lifetime")
	}
	first := calls[0]
	maxTail := 2
	if first.origin == "step-completion" {
		maxTail = 3
	} else if first.origin != "background" {
		return fmt.Errorf("unattributed first failed-step ACK loss")
	}
	if len(calls)-1 > maxTail {
		return fmt.Errorf("failed-step stage/direct history has unexplained extra RPCs")
	}
	for i, call := range calls {
		receipt := receipts[i]
		message, ok := call.message.(*agentv1.ReportStepRunStatusRequest)
		if !ok || message.StepRunId != transition.PreviousStepRunID || message.StepAttemptAdmissionDigest != transition.PreviousAdmissionBindingDigest || !message.RetryableShellExit || call.claim != transition.PreviousStepClaimID || receipt.claim != call.claim || !bytes.Equal(call.payload, receipt.payload) || !proto.Equal(call.message, receipt.message) || !bytes.Equal(call.payload, first.payload) {
			return fmt.Errorf("failed-step replay changed frozen identity/admission/bytes")
		}
		if call.begin <= s.invocations[0].begin || call.end <= call.begin || call.end >= boundary || i > 0 && call.begin <= calls[i-1].end {
			return fmt.Errorf("failed-step ACK did not complete before the fresh execution-authority RPC")
		}
		if call.origin == "background" {
			if call.invocation != 0 {
				return fmt.Errorf("failed-step background replay inherited handler context")
			}
		} else if call.origin != "step-completion" || call.invocation != 1 {
			return fmt.Errorf("failed-step RPC lacks exact original admitted handler")
		}
		if i == 0 {
			if call.code != codes.Unavailable || call.success {
				return fmt.Errorf("failed-step first committed ACK was not lost once")
			}
		} else {
			if call.code != codes.OK || !call.success || i < len(calls)-1 && call.origin != "background" {
				return fmt.Errorf("failed-step success tail differs from finite stage/direct history")
			}
		}
		digest := sha256.New()
		for _, value := range []string{"arcentra-terminal-report-v1", string(call.message.ProtoReflect().Descriptor().FullName()), call.claim, transition.Scope.Identity.PipelineID, transition.Scope.Identity.PipelineRunID, transition.Scope.Identity.JobRunID} {
			_, _ = digest.Write(binary.BigEndian.AppendUint64(nil, uint64(len(value))))
			_, _ = digest.Write([]byte(value))
		}
		_, _ = digest.Write(call.payload)
		if hex.EncodeToString(digest.Sum(nil)) != transition.FailedStepReceiptDigest {
			return fmt.Errorf("failed-step exact replay digest differs from acknowledged transition")
		}
	}
	return nil
}

func (s workerDeliverySnapshot) summary() string {
	var out strings.Builder
	fmt.Fprintf(&out, "enqueues=%d callbacks=%d RPCs=%d", len(s.original), len(s.invocations), len(s.calls))
	fmt.Fprintf(&out, " failedStepRPCs=%d advanceBoundary=%d denialBoundary=%d denialEnd=%d denialCode=%s", len(s.stepCalls), s.advanceBoundary, s.denialBoundary, s.denialEnd, s.denialCode)
	for i, invocation := range s.invocations {
		if i >= 4 {
			break
		}
		fmt.Fprintf(&out, " callback%d(manual=%t,code=%s,deliveryError=%t,done=%t)", i+1, invocation.manual, invocation.code, invocation.deliveryError, invocation.end > invocation.begin)
	}
	for i, call := range s.calls {
		if i >= 6 {
			break
		}
		fmt.Fprintf(&out, " RPC%d(%s,callback=%d,code=%s,success=%t)", i+1, call.origin, call.invocation, call.code, call.success)
	}
	for i, call := range s.stepCalls {
		if i >= 5 {
			break
		}
		fmt.Fprintf(&out, " stepRPC%d(%s,callback=%d,code=%s,success=%t)", i+1, call.origin, call.invocation, call.code, call.success)
	}
	return out.String()
}

// validate follows the finite stage/direct-put histories. It does not turn an
// arbitrary number of same-payload messages into an acceptable replay history.
func (s workerDeliverySnapshot) validate(receipts []controllerWorkerReceipt, manual bool) (bool, error) {
	if len(s.original) != 1 || len(s.calls) < 2 || len(s.calls) != len(receipts) {
		return false, fmt.Errorf("delivery ledger/enqueue mismatch: enqueues=%d RPCs=%d commits=%d", len(s.original), len(s.calls), len(receipts))
	}
	first := s.calls[0]
	automatic := first.origin == "completion" && first.invocation == 1
	if !automatic && (first.origin != "background" || first.invocation != 0) {
		return false, fmt.Errorf("lost ACK has unexplained origin")
	}
	originalCount := 1
	if automatic {
		originalCount = 2
	}
	wanted := originalCount
	if manual {
		wanted++
	}
	if len(s.invocations) != wanted {
		return false, fmt.Errorf("actual handler invocations=%d want=%d", len(s.invocations), wanted)
	}
	for i, invocation := range s.invocations {
		if i > 0 && invocation.begin <= s.invocations[i-1].end {
			return false, fmt.Errorf("actual handler invocation order overlapped or moved backward")
		}
		if invocation.manual && invocation.begin <= s.calls[len(s.calls)-1].end {
			return false, fmt.Errorf("manual duplicate preceded original terminal completion")
		}
		if invocation.end <= invocation.begin || invocation.task.Type != s.original[0].Type || !bytes.Equal(invocation.task.Payload, s.original[0].Payload) || invocation.manual != (i >= originalCount) {
			return false, fmt.Errorf("handler identity/phase/completion changed")
		}
		if i == 0 && automatic {
			if !invocation.deliveryError || invocation.code != codes.Unavailable {
				return false, fmt.Errorf("lost local ACK did not return terminalDeliveryError")
			}
		} else if invocation.deliveryError || invocation.code != codes.OK {
			return false, fmt.Errorf("handler did not finish successfully")
		}
	}
	for i, call := range s.calls {
		receipt := receipts[i]
		if call.end <= call.begin || i > 0 && call.begin <= s.calls[i-1].end || call.claim == "" || call.claim != receipt.claim || !bytes.Equal(call.payload, receipt.payload) || !proto.Equal(call.message, receipt.message) || !bytes.Equal(call.payload, first.payload) || call.claim != first.claim {
			return false, fmt.Errorf("RPC/commit ledger changed original receipt or serialized order")
		}
		if i == 0 {
			if call.code != codes.Unavailable || call.success {
				return false, fmt.Errorf("first committed ACK loss was not observed exactly once")
			}
		} else if call.code != codes.OK || !call.success {
			return false, fmt.Errorf("replayed committed ACK was not successful")
		}
		if call.origin == "background" {
			if call.invocation != 0 {
				return false, fmt.Errorf("background inherited a task invocation")
			}
		} else {
			if call.invocation < 1 || call.invocation > originalCount {
				return false, fmt.Errorf("terminal RPC came from manual/unknown invocation")
			}
			invocation := s.invocations[call.invocation-1]
			if call.begin <= invocation.begin || call.end >= invocation.end {
				return false, fmt.Errorf("RPC escaped its actual handler boundary")
			}
		}
	}
	if first.begin <= s.invocations[0].begin || first.end >= s.invocations[0].end {
		return false, fmt.Errorf("original ACK loss escaped initial handler lifetime")
	}
	tail := s.calls[1:]
	completionInvocation := 1
	if automatic {
		if len(tail) < 2 {
			return false, fmt.Errorf("automatic redelivery lost durable replay or pending completion")
		}
		recovery := tail[0]
		if !(recovery.origin == "background" && recovery.invocation == 0 || recovery.origin == "preflight" && recovery.invocation == 2) {
			return false, fmt.Errorf("old durable receipt was not recovered before pending completion")
		}
		tail = tail[1:]
		completionInvocation = 2
	}
	if len(tail) < 1 || len(tail) > 2 {
		return false, fmt.Errorf("stage/direct completion has unexplained RPC count=%d", len(tail))
	}
	for i, call := range tail {
		invocation := s.invocations[completionInvocation-1]
		if call.begin <= invocation.begin || call.end >= invocation.end || !(call.origin == "background" && call.invocation == 0 || call.origin == "completion" && call.invocation == completionInvocation) || len(tail) == 2 && i == 0 && call.origin != "background" {
			return false, fmt.Errorf("stage/direct completion RPC order or source changed")
		}
	}
	return automatic, nil
}

func TestFailedStepDeliveryObserverFiniteHistories(t *testing.T) {
	build := func(automatic bool, recovery, completion []string) (workerDeliverySnapshot, []controllerWorkerReceipt) {
		task := nova.Task{Type: taskpayload.TaskTypeJobRun, Payload: []byte("synthetic original task")}
		request := &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "job", Status: 4, StartTime: 1, EndTime: 2}
		payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		s := workerDeliverySnapshot{original: []nova.Task{task}, invocations: []workerDeliveryInvocation{{task: task, begin: 1}}}
		tick := 1
		add := func(origin string, invocation int, code codes.Code) {
			tick++
			begin := tick
			tick++
			s.calls = append(s.calls, workerDeliveryRPC{origin: origin, invocation: invocation, begin: begin, end: tick, claim: "original-claim", payload: payload, message: request, code: code, success: code == codes.OK})
		}
		if automatic {
			add("completion", 1, codes.Unavailable)
			tick++
			s.invocations[0].end, s.invocations[0].code, s.invocations[0].deliveryError = tick, codes.Unavailable, true
			tick++
			s.invocations = append(s.invocations, workerDeliveryInvocation{task: task, begin: tick})
			add(recovery[0], map[bool]int{true: 0, false: 2}[recovery[0] == "background"], codes.OK)
		} else {
			add("background", 0, codes.Unavailable)
		}
		current := len(s.invocations)
		for _, origin := range completion {
			invocation := current
			if origin == "background" {
				invocation = 0
			}
			add(origin, invocation, codes.OK)
		}
		tick++
		s.invocations[current-1].end = tick
		var receipts []controllerWorkerReceipt
		for _, call := range s.calls {
			receipts = append(receipts, controllerWorkerReceipt{claim: call.claim, payload: call.payload, message: call.message})
		}
		return s, receipts
	}
	for _, automatic := range []bool{false, true} {
		for _, recovery := range []string{"background", "preflight"} {
			for _, tail := range [][]string{{"completion"}, {"background"}, {"background", "completion"}, {"background", "background"}} {
				name := fmt.Sprintf("automatic=%t/recovery=%s/tail=%v", automatic, recovery, tail)
				t.Run(name, func(t *testing.T) {
					s, receipts := build(automatic, []string{recovery}, tail)
					if got, err := s.validate(receipts, false); err != nil || got != automatic {
						t.Fatalf("valid finite history rejected: %v", err)
					}
					s.invocations = append(s.invocations, workerDeliveryInvocation{task: s.original[0], manual: true, begin: 100, end: 101})
					if got, err := s.validate(receipts, true); err != nil || got != automatic {
						t.Fatalf("separate manual delivery rejected: %v", err)
					}
				})
			}
		}
	}
	for name, mutate := range map[string]func(*workerDeliverySnapshot, *[]controllerWorkerReceipt){
		"extra-enqueue": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt) {
			s.original = append(s.original, s.original[0])
		},
		"overlapping-handlers": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt) {
			s.invocations[1].begin = s.invocations[0].end
		},
		"missing-handler": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt) { s.invocations = s.invocations[:1] },
		"changed-task": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt) {
			s.invocations[1].task.Payload = []byte("different")
		},
		"wrong-claim": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt) { s.calls[1].claim = "different" },
		"wrong-bytes": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt) {
			s.calls[1].payload = []byte("different")
		},
		"wrong-message": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt) {
			s.calls[1].message = &agentv1.ReportJobRunStatusRequest{Status: 5}
		},
		"unattributed": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt) { s.calls[1].origin = "unattributed" },
		"second-loss": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt) {
			s.calls[1].code, s.calls[1].success = codes.Unavailable, false
		},
		"missing-commit":             func(_ *workerDeliverySnapshot, r *[]controllerWorkerReceipt) { *r = (*r)[:2] },
		"late-RPC":                   func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt) { s.calls[2].end = 999 },
		"wrong-preflight-invocation": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt) { s.calls[1].invocation = 1 },
		"failed-retry":               func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt) { s.invocations[1].code = codes.Unknown },
		"duplicate-local-completion": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt) { s.calls[1].origin = "completion" },
	} {
		t.Run(name, func(t *testing.T) {
			s, receipts := build(true, []string{"preflight"}, []string{"completion"})
			mutate(&s, &receipts)
			if _, err := s.validate(receipts, false); err == nil {
				t.Fatal("invalid delivery history accepted")
			}
		})
	}
	t.Run("extra-background-RPC", func(t *testing.T) {
		s, receipts := build(true, []string{"preflight"}, []string{"background", "background", "completion"})
		if _, err := s.validate(receipts, false); err == nil {
			t.Fatal("unbounded source-attributed receipts accepted")
		}
	})
	t.Run("reversed-stage-direct-order", func(t *testing.T) {
		s, receipts := build(false, nil, []string{"completion", "background"})
		if _, err := s.validate(receipts, false); err == nil {
			t.Fatal("unexplained completion order accepted")
		}
	})
}

func TestFailedStepDeliveryObserverAcknowledgedBeforeAdvance(t *testing.T) {
	build := func(first string, tail []string) (workerDeliverySnapshot, []controllerWorkerReceipt, taskpayload.StepAttemptTransition) {
		request := &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "failed-step", Status: 5, ExitCode: 9, RetryableShellExit: true, StepAttemptAdmissionDigest: "admission"}
		payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		tr := taskpayload.StepAttemptTransition{PreviousStepRunID: "failed-step", PreviousStepClaimID: "original-step-claim", PreviousAdmissionBindingDigest: "admission", Scope: taskpayload.StepAttemptIdentity{Identity: taskpayload.JobInputIdentity{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job"}}}
		digest := sha256.New()
		for _, value := range []string{"arcentra-terminal-report-v1", string(request.ProtoReflect().Descriptor().FullName()), tr.PreviousStepClaimID, "pipeline", "run", "job"} {
			_, _ = digest.Write(binary.BigEndian.AppendUint64(nil, uint64(len(value))))
			_, _ = digest.Write([]byte(value))
		}
		_, _ = digest.Write(payload)
		tr.FailedStepReceiptDigest = hex.EncodeToString(digest.Sum(nil))
		s := workerDeliverySnapshot{invocations: []workerDeliveryInvocation{{begin: 1, end: 100}}, advanceInvocation: 1}
		var receipts []controllerWorkerReceipt
		for i, origin := range append([]string{first}, tail...) {
			invocation := 1
			if origin == "background" {
				invocation = 0
			}
			call := workerDeliveryRPC{origin: origin, invocation: invocation, begin: i*2 + 2, end: i*2 + 3, claim: tr.PreviousStepClaimID, payload: payload, message: request, success: i > 0}
			if i == 0 {
				call.code = codes.Unavailable
			}
			s.stepCalls = append(s.stepCalls, call)
			receipts = append(receipts, controllerWorkerReceipt{claim: call.claim, payload: call.payload, message: call.message})
		}
		s.advanceBoundary = len(s.stepCalls)*2 + 2
		return s, receipts, tr
	}
	for _, first := range []string{"background", "step-completion"} {
		tails := [][]string{{"background"}, {"step-completion"}, {"background", "background"}, {"background", "step-completion"}}
		if first == "step-completion" {
			tails = append(tails, []string{"background", "background", "background"}, []string{"background", "background", "step-completion"})
		}
		for _, tail := range tails {
			t.Run(fmt.Sprintf("%s/%v", first, tail), func(t *testing.T) {
				s, receipts, tr := build(first, tail)
				if err := s.validateStep(receipts, tr); err != nil {
					t.Fatalf("legal admitted-step ACK history rejected: %v", err)
				}
				s.invocations[0].end = 0 // The first advance is observed inside the still-running handler.
				if err := s.validateStep(receipts, tr); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
	for name, mutate := range map[string]func(*workerDeliverySnapshot, *[]controllerWorkerReceipt, *taskpayload.StepAttemptTransition){
		"cross-claim": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptTransition) {
			s.stepCalls[1].claim = "foreign"
		},
		"cross-admission": func(_ *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, tr *taskpayload.StepAttemptTransition) {
			tr.PreviousAdmissionBindingDigest = "foreign"
		},
		"cross-digest": func(_ *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, tr *taskpayload.StepAttemptTransition) {
			tr.FailedStepReceiptDigest = "foreign"
		},
		"late-ACK": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptTransition) {
			s.stepCalls[1].end = s.advanceBoundary
		},
		"not-ACKed": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptTransition) {
			s.stepCalls[1].success = false
		},
		"second-loss": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptTransition) {
			s.stepCalls[1].code = codes.Unavailable
		},
		"wrong-handler": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptTransition) {
			s.stepCalls[1].invocation = 2
		},
		"wrong-advance-handler": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptTransition) {
			s.advanceInvocation = 2
		},
		"outside-handler": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptTransition) {
			s.invocations[0].end = s.advanceBoundary
		},
		"missing-commit": func(_ *workerDeliverySnapshot, r *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptTransition) {
			*r = (*r)[:1]
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, receipts, tr := build("step-completion", []string{"step-completion"})
			mutate(&s, &receipts, &tr)
			if err := s.validateStep(receipts, tr); err == nil {
				t.Fatal("unproven failed-step ACK history accepted")
			}
		})
	}
	for _, bad := range []struct {
		first string
		tail  []string
	}{
		{"background", []string{"background", "background", "step-completion"}},
		{"step-completion", []string{"background", "background", "background", "step-completion"}},
		{"step-completion", []string{"step-completion", "background"}},
	} {
		s, receipts, tr := build(bad.first, bad.tail)
		if err := s.validateStep(receipts, tr); err == nil {
			t.Fatal("extra or out-of-order failed-step sends accepted")
		}
	}
}

// TestFailedStepApprovalDeliveryBeforeDenial 验证拒绝前每个历史回执的有限来源与持久摘要
func TestFailedStepApprovalDeliveryBeforeDenial(t *testing.T) {
	build := func(first string, tail []string) (workerDeliverySnapshot, []controllerWorkerReceipt, taskpayload.StepAttemptAdmission, string) {
		admission := taskpayload.StepAttemptAdmission{Scope: taskpayload.StepAttemptIdentity{Identity: taskpayload.JobInputIdentity{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent"}, StepRunID: "failed", StepClaimID: "claim", GenerationID: "generation", GenerationBindingDigest: "generation-binding", InputManifestDigest: "inputs", RetryPlanDigest: "plan"}, BindingDigest: "admission"}
		message := &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "failed", Status: 5, ExitCode: 23, RetryableShellExit: true, StepAttemptAdmissionDigest: "admission", GenerationId: "generation", GenerationBindingDigest: "generation-binding"}
		payload, _ := proto.MarshalOptions{Deterministic: true}.Marshal(message)
		digest := sha256.New()
		for _, value := range []string{"arcentra-terminal-report-v1", string(message.ProtoReflect().Descriptor().FullName()), "claim", "pipeline", "run", "job"} {
			digest.Write(binary.BigEndian.AppendUint64(nil, uint64(len(value))))
			digest.Write([]byte(value))
		}
		digest.Write(payload)
		task := nova.Task{Type: taskpayload.TaskTypeJobRun, Payload: []byte("frozen-dispatch")}
		s := workerDeliverySnapshot{original: []nova.Task{task}, invocations: []workerDeliveryInvocation{{task: task, begin: 1, end: 100}}, denialBoundary: 90, denialEnd: 91, denialInvocation: 1, denialCode: codes.FailedPrecondition, denialRequest: retryapproval.Request{Operation: "admit", Identity: admission.Scope.Identity, GenerationID: "generation", GenerationBindingDigest: "generation-binding", InputManifestDigest: "inputs", RetryPlanDigest: "plan"}}
		var receipts []controllerWorkerReceipt
		for i, origin := range append([]string{first}, tail...) {
			invocation := 1
			if origin == "background" {
				invocation = 0
			}
			call := workerDeliveryRPC{origin: origin, invocation: invocation, begin: 2 + i*2, end: 3 + i*2, claim: "claim", payload: append([]byte(nil), payload...), message: proto.Clone(message), success: i > 0}
			if i == 0 {
				call.code = codes.Unavailable
			}
			s.stepCalls = append(s.stepCalls, call)
			receipts = append(receipts, controllerWorkerReceipt{claim: call.claim, payload: append([]byte(nil), payload...), message: proto.Clone(message)})
		}
		return s, receipts, admission, hex.EncodeToString(digest.Sum(nil))
	}
	for _, history := range [][]string{{"step-completion", "step-completion"}, {"step-completion", "background", "step-completion"}, {"step-completion", "background", "background", "step-completion"}, {"background", "background", "step-completion"}} {
		t.Run(strings.Join(history, "-"), func(t *testing.T) {
			s, r, a, d := build(history[0], history[1:])
			if err := s.validateDeniedStep(r, a, d, 1); err != nil {
				t.Fatal(err)
			}
		})
	}
	for name, mutate := range map[string]func(*workerDeliverySnapshot, *[]controllerWorkerReceipt, *taskpayload.StepAttemptAdmission, *string, *int){
		"bytes": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptAdmission, _ *string, _ *int) {
			s.stepCalls[1].payload = []byte("changed")
		},
		"claim": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptAdmission, _ *string, _ *int) {
			s.stepCalls[1].claim = "other"
		},
		"digest": func(_ *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptAdmission, d *string, _ *int) {
			*d = "other"
		},
		"receipt": func(_ *workerDeliverySnapshot, r *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptAdmission, _ *string, _ *int) {
			(*r)[1].payload = []byte("changed")
		},
		"unknown-origin": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptAdmission, _ *string, _ *int) {
			s.stepCalls[1].origin = "unattributed"
		},
		"second-loss": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptAdmission, _ *string, _ *int) {
			s.stepCalls[1].code = codes.Unavailable
		},
		"extra-injection": func(_ *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptAdmission, _ *string, n *int) {
			*n = 2
		},
		"wrong-denial": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptAdmission, _ *string, _ *int) {
			s.denialRequest.InputManifestDigest = "other"
		},
		"after-denial": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptAdmission, _ *string, _ *int) {
			s.stepCalls[1].end = s.denialBoundary
		},
		"successor": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptAdmission, _ *string, _ *int) {
			s.advanceBoundary = 80
		},
		"second-handler": func(s *workerDeliverySnapshot, _ *[]controllerWorkerReceipt, _ *taskpayload.StepAttemptAdmission, _ *string, _ *int) {
			s.invocations = append(s.invocations, s.invocations[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, r, a, d := build("step-completion", []string{"step-completion"})
			losses := 1
			mutate(&s, &r, &a, &d, &losses)
			if s.validateDeniedStep(r, a, d, losses) == nil {
				t.Fatal("unproven revoked receipt accepted")
			}
		})
	}
	s, r, a, d := build("step-completion", []string{"background", "background", "background", "step-completion"})
	if s.validateDeniedStep(r, a, d, 1) == nil {
		t.Fatal("unexplained extra RPC accepted")
	}
}
