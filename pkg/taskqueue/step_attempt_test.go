package taskqueue

import (
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func testStepScope() StepAttemptIdentity {
	runtime := uuid.NewString()
	return StepAttemptIdentity{Identity: JobInputIdentity{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", ClaimID: "original", RuntimeBindingDigest: "v1:" + strings.Repeat("a", 64)}, GenerationID: uuid.NewString(), GenerationBindingDigest: "v1:" + strings.Repeat("b", 64), RuntimeClaimID: runtime, InputManifestDigest: "v1:" + strings.Repeat("c", 64), RetryPlanDigest: "v1:" + strings.Repeat("d", 64), Session: StepAttemptSession{SessionID: runtime, SessionIncarnationNonce: uuid.NewString(), Namespace: "fixture", ContainerID: "arcentra-" + runtime, SnapshotID: "arcentra-" + runtime + "-snapshot", Snapshotter: "overlayfs", ResourceOwnerDigest: strings.Repeat("e", 64), WorkspaceID: "job-" + runtime, WorkspaceDevice: 1, WorkspaceInode: 2}, LogicalStepRunID: "step-1", StepRunID: "step-1", StepName: "shell", StepIndex: 0, DefinitionDigest: "v1:" + strings.Repeat("f", 64), Attempt: 1, StepClaimID: StepAttemptClaimID("original", "step-1")}
}
func testStepTransition(t *testing.T) StepAttemptTransition {
	t.Helper()
	request := StepAttemptTransition{Version: 1, Operation: "admit", Scope: testStepScope()}
	request.RequestDigest, _ = request.Digest()
	return request
}
func testStepAdmission(t *testing.T, request StepAttemptTransition) StepAttemptAdmission {
	t.Helper()
	a := StepAttemptAdmission{Version: 1, Scope: request.Scope, AdmissionID: uuid.NewString(), RequestDigest: request.RequestDigest, PreviousAdmissionID: request.PreviousAdmissionID, NotBeforeUnixMilli: time.Now().UnixMilli(), ExpiresUnixMilli: time.Now().Add(StepAttemptAdmissionLifetime).UnixMilli()}
	a.BindingDigest, _ = a.Digest()
	return a
}
func testStepAdvance(t *testing.T) (StepAttemptTransition, StepAttemptAdmission) {
	t.Helper()
	first := testStepTransition(t)
	a := testStepAdmission(t, first)
	now := time.Now()
	b := &PausedStepBoundary{Version: 1, Scope: a.Scope, BoundaryNonce: uuid.NewString(), BoundarySequence: 1, FailedAdmissionID: a.AdmissionID, FailedAdmissionBindingDigest: a.BindingDigest, FailedStepReceiptDigest: strings.Repeat("a", 64), CapturedUnixMilli: now.UnixMilli(), ExpiresUnixMilli: now.Add(PausedStepBoundaryLifetime).UnixMilli(), FailedExecExitObserved: true, ExecReaped: true, TaskPaused: true, ExactLiveInitOnly: true, Childless: true}
	next := first
	next.Operation = "advance"
	next.Scope.Attempt = 2
	next.Scope.StepRunID = "step-2"
	next.Scope.StepClaimID = StepAttemptClaimID("original", "step-2")
	next.PreviousStepRunID = a.Scope.StepRunID
	next.PreviousStepClaimID = a.Scope.StepClaimID
	next.PreviousAdmissionID = a.AdmissionID
	next.PreviousAdmissionBindingDigest = a.BindingDigest
	next.FailedStepReceiptDigest = b.FailedStepReceiptDigest
	next.Boundary = b
	next.RequestDigest, _ = next.Digest()
	return next, a
}
func TestStepAttemptCanonicalRoundTripAndBinding(t *testing.T) {
	initial := testStepTransition(t)
	next, _ := testStepAdvance(t)
	for _, request := range []StepAttemptTransition{initial, next} {
		encoded, err := request.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseStepAttemptTransition(encoded)
		if err != nil || parsed.RequestDigest != request.RequestDigest {
			t.Fatalf("request roundtrip: %v", err)
		}
		admission := testStepAdmission(t, request)
		if err := admission.ValidateTransition(request); err != nil {
			t.Fatal(err)
		}
		body, err := admission.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseStepAttemptAdmission(body)
		if err != nil || *got != admission {
			t.Fatalf("admission roundtrip: %v", err)
		}
		for _, bad := range []string{encoded + " ", strings.Replace(encoded, "\"version\":1", "\"unknown\":true,\"version\":1", 1), strings.Replace(encoded, "\"version\":1", "\"version\":1,\"version\":1", 1)} {
			if _, err := ParseStepAttemptTransition(bad); err == nil {
				t.Fatal("noncanonical request accepted")
			}
		}
		changed := request
		changed.Scope.Identity.AgentID = "other"
		changed.RequestDigest, _ = changed.Digest()
		if admission.ValidateTransition(changed) == nil {
			t.Fatal("foreign agent transition accepted")
		}
		changed = request
		changed.Operation = "inspect"
		changed.PreviousStepRunID = ""
		changed.PreviousStepClaimID = ""
		changed.PreviousAdmissionID = ""
		changed.PreviousAdmissionBindingDigest = ""
		changed.FailedStepReceiptDigest = ""
		changed.Boundary = nil
		changed.RequestDigest, _ = changed.Digest()
		if admission.ValidateTransition(changed) == nil {
			t.Fatal("inspection became executable admission")
		}
	}
}
func TestStepAttemptRejectsMixedBoundaryAndAuthority(t *testing.T) {
	tests := map[string]func(*StepAttemptTransition){
		"version": func(r *StepAttemptTransition) { r.Version = 2 }, "claim": func(r *StepAttemptTransition) { r.Scope.StepClaimID = r.Scope.RuntimeClaimID }, "session": func(r *StepAttemptTransition) { r.Scope.Session.SessionID = uuid.NewString() }, "incarnation": func(r *StepAttemptTransition) { r.Scope.Session.SessionIncarnationNonce = "secret" }, "plan": func(r *StepAttemptTransition) { r.Scope.RetryPlanDigest = "bad" }, "input": func(r *StepAttemptTransition) { r.Scope.InputManifestDigest = "" }, "definition": func(r *StepAttemptTransition) { r.Scope.DefinitionDigest = "" }, "workspace": func(r *StepAttemptTransition) { r.Scope.Session.WorkspaceInode = 0 }, "attempt": func(r *StepAttemptTransition) { r.Scope.Attempt = 6 }, "predecessor": func(r *StepAttemptTransition) { r.PreviousStepRunID = "different" }, "failure": func(r *StepAttemptTransition) { r.FailedStepReceiptDigest = strings.Repeat("b", 64) }, "boundaryNonce": func(r *StepAttemptTransition) { r.Boundary.BoundaryNonce = "" }, "boundarySequence": func(r *StepAttemptTransition) { r.Boundary.BoundarySequence = 0 }, "liveInit": func(r *StepAttemptTransition) { r.Boundary.ExactLiveInitOnly = false }, "childless": func(r *StepAttemptTransition) { r.Boundary.Childless = false }, "reaped": func(r *StepAttemptTransition) { r.Boundary.ExecReaped = false }, "paused": func(r *StepAttemptTransition) { r.Boundary.TaskPaused = false }, "observed": func(r *StepAttemptTransition) { r.Boundary.FailedExecExitObserved = false }, "unbounded": func(r *StepAttemptTransition) {
			r.Boundary.ExpiresUnixMilli = r.Boundary.CapturedUnixMilli + PausedStepBoundaryLifetime.Milliseconds() + 1
		},
	}
	for name, modify := range tests {
		t.Run(name, func(t *testing.T) {
			r, _ := testStepAdvance(t)
			modify(&r)
			r.RequestDigest, _ = r.Digest()
			if r.Validate() == nil {
				t.Fatal("invalid step authority accepted")
			}
		})
	}
}
func TestFailedStepPlanVersionIsolation(t *testing.T) {
	runtime := &RuntimeRequest{Type: RuntimeContainerdOfflineV1, Image: "fixture/image@sha256:" + strings.Repeat("a", 64), Resources: RuntimeResources{CPU: "500m", Memory: "64Mi"}}
	runtime.BindingDigest, _ = RuntimeBindingDigest("pipeline", "run", "job", "agent", runtime)
	payload := &JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", Runtime: runtime, Retry: &pipelinev1.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}, Steps: []StepPayload{{Name: "shell", StepRunID: "step-1", AttemptRunIDs: []string{"step-1", "step-2"}, Uses: "shell"}}}
	whole, err := NewJobRetryPlan(payload)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := whole.Canonical()
	digest, _ := whole.Digest("pipeline", "run", "job", "agent", runtime.BindingDigest)
	payload.Retry.Mode = "failed_step"
	failed, err := NewJobRetryPlan(payload)
	if err != nil {
		t.Fatal(err)
	}
	if whole.Version != 1 || failed.Version != 2 {
		t.Fatal("retry mode versions collapsed")
	}
	fd, _ := failed.Digest("pipeline", "run", "job", "agent", runtime.BindingDigest)
	if fd == digest || failed.DefinitionDigest == whole.DefinitionDigest {
		t.Fatal("failed_step reused whole_job domains")
	}
	after, _ := whole.Canonical()
	if before != after {
		t.Fatal("original whole_job plan mutated")
	}
	failed.Version = 1
	if _, err := failed.Canonical(); err == nil {
		t.Fatal("failed_step promoted v1 plan")
	}
	whole.Version = 2
	if _, err := whole.Canonical(); err == nil {
		t.Fatal("whole_job promoted v2 plan")
	}
	payload.Steps[0].ContinueOnError = true
	if _, err := NewJobRetryPlan(payload); err == nil {
		t.Fatal("unsupported continueOnError accepted")
	}
}

// 这些固定字节由合并基线中的版本一构造器独立计算并逐字比较，不重新解释旧计划
func TestWholeJobV1CanonicalGoldenSurvivesFailedStepAddition(t *testing.T) {
	p, _ := generationContractFixture(t)
	const want = `{"version":1,"policy":{"max_attempts":2,"delay":"1s","mode":"whole_job","idempotent":true,"allowed_exit_codes":[9]},"definitionDigest":"v1:97b1adc4aef0b601adfb2d6db02faff68a5c38376f5ef613fa23ed081f5b6565","steps":[{"name":"shell","stepIndex":0,"uses":"shell","action":"","continueOnError":false,"attemptRunIds":["step-1","step-2"],"definitionDigest":"v1:3500c468c9895174a20e2e7c7fef40073f58e3f8add8f813dfb65a040f68e4bd"}]}`
	actual, err := p.RetryPlan.Canonical()
	if err != nil || actual != want {
		t.Fatal("whole_job version-one canonical bytes changed", err)
	}
	if p.RetryPlanDigest != "v1:701feda8db01fbcb5d9ff7464dd7abbaa4850dfbb32210bc3283c82f01c4668a" {
		t.Fatal("whole_job version-one digest semantics changed")
	}
}
