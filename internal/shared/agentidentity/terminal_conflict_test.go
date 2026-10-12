package agentidentity

import (
	"strings"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func terminalConflictFixture(t *testing.T) (*agentv1.ReportStepRunStatusRequest, TerminalConflictInfo) {
	t.Helper()
	request := &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "step", Status: steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS, ExitCode: 0, EndTime: 2200000000, Metrics: map[string]string{"workspace": "/work", "large": "9007199254740993"}}
	digest, err := TerminalPayloadDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	return request, TerminalConflictInfo{Kind: "step", AgentID: "agent", RunID: "step", ClaimID: "original", ClaimSource: "execution_claim", PayloadDigest: digest, PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", Attempt: 2, StepIndex: 1, PreviousStepRunID: "first-attempt", AuthoritativeStatus: 6}
}

func TestTerminalConflictBindsExactWireIdentity(t *testing.T) {
	request, info := terminalConflictFixture(t)
	err := TerminalConflictError(info)
	if got, ok := ParseTerminalConflict(err, request, "original"); !ok || got != info {
		t.Fatalf("lost exact conflict: %+v %v", got, err)
	}
	for _, mutate := range []func(*agentv1.ReportStepRunStatusRequest){
		func(r *agentv1.ReportStepRunStatusRequest) { r.AgentId = "other" },
		func(r *agentv1.ReportStepRunStatusRequest) { r.StepRunId = "other" },
		func(r *agentv1.ReportStepRunStatusRequest) { r.Status = steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED },
		func(r *agentv1.ReportStepRunStatusRequest) { r.ExitCode = 1 },
		func(r *agentv1.ReportStepRunStatusRequest) { r.StartTime = 1 },
		func(r *agentv1.ReportStepRunStatusRequest) { r.EndTime++ },
		func(r *agentv1.ReportStepRunStatusRequest) { r.ErrorMessage = "changed" },
		func(r *agentv1.ReportStepRunStatusRequest) { r.Metrics["large"] = "9007199254740992" },
		func(r *agentv1.ReportStepRunStatusRequest) { r.ProtoReflect().SetUnknown([]byte{0x98, 0x06, 0x01}) },
	} {
		changed := proto.Clone(request).(*agentv1.ReportStepRunStatusRequest)
		mutate(changed)
		if _, ok := ParseTerminalConflict(err, changed, "original"); ok {
			t.Fatal("changed payload accepted")
		}
	}
	for _, claim := range []string{"", "other", " original", "original "} {
		if _, ok := ParseTerminalConflict(err, request, claim); ok {
			t.Fatal("wrong claim accepted")
		}
	}
	for _, req := range []proto.Message{nil, (*agentv1.ReportStepRunStatusRequest)(nil), &emptypb.Empty{}, &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "step", Status: 4}} {
		if _, ok := ParseTerminalConflict(err, req, "original"); ok {
			t.Fatal("wrong type accepted")
		}
	}
}

func TestTerminalConflictRejectsUnrelatedMalformedAndAmbiguousDetails(t *testing.T) {
	request, info := terminalConflictFixture(t)
	good := terminalConflictMetadata(info)
	for _, key := range []string{"version", "nonRetryable", "kind", "agentId", "runId", "claimId", "claimSource", "payloadDigest", "pipelineId", "pipelineRunId", "jobRunId", "attempt", "stepIndex", "previousStepRunId", "authoritativeStatus", "receiptDigest"} {
		t.Run("missing_"+key, func(t *testing.T) {
			m := terminalConflictMetadata(info)
			delete(m, key)
			s, e := status.New(codes.FailedPrecondition, "ignored").WithDetails(&errdetails.ErrorInfo{Domain: terminalConflictDomain, Reason: terminalConflictReason, Metadata: m})
			if e != nil {
				t.Fatal(e)
			}
			if _, ok := ParseTerminalConflict(s.Err(), request, "original"); ok {
				t.Fatal("missing evidence accepted")
			}
		})
	}
	for _, test := range []struct{ key, value string }{{"version", "2"}, {"nonRetryable", "false"}, {"kind", "job"}, {"agentId", "other"}, {"runId", "other"}, {"claimId", "other"}, {"claimSource", "unknown"}, {"claimSource", "terminal_parent_claim"}, {"payloadDigest", strings.Repeat("a", 64)}, {"pipelineId", ""}, {"pipelineId", "https://user:password@host"}, {"pipelineRunId", "line\nbreak"}, {"pipelineRunId", ""}, {"jobRunId", ""}, {"attempt", "0"}, {"attempt", "02"}, {"stepIndex", "-1"}, {"previousStepRunId", ""}, {"authoritativeStatus", "3"}, {"receiptDigest", "bad"}, {"receiptDigest", strings.Repeat("A", 64)}, {"unknown", "value"}} {
		t.Run(test.key+test.value, func(t *testing.T) {
			m := terminalConflictMetadata(info)
			m[test.key] = test.value
			s, e := status.New(codes.FailedPrecondition, "ignored").WithDetails(&errdetails.ErrorInfo{Domain: terminalConflictDomain, Reason: terminalConflictReason, Metadata: m})
			if e != nil {
				t.Fatal(e)
			}
			if _, ok := ParseTerminalConflict(s.Err(), request, "original"); ok {
				t.Fatal("malformed evidence accepted")
			}
		})
	}
	for _, test := range []struct {
		code           codes.Code
		domain, reason string
	}{{codes.Unavailable, terminalConflictDomain, terminalConflictReason}, {codes.PermissionDenied, terminalConflictDomain, terminalConflictReason}, {codes.FailedPrecondition, "other", terminalConflictReason}, {codes.FailedPrecondition, terminalConflictDomain, "other"}} {
		s, e := status.New(test.code, "ignored").WithDetails(&errdetails.ErrorInfo{Domain: test.domain, Reason: test.reason, Metadata: good})
		if e != nil {
			t.Fatal(e)
		}
		if _, ok := ParseTerminalConflict(s.Err(), request, "original"); ok {
			t.Fatal("wrong error classification accepted")
		}
	}
	detail := &errdetails.ErrorInfo{Domain: terminalConflictDomain, Reason: terminalConflictReason, Metadata: good}
	duplicate, e := status.New(codes.FailedPrecondition, "ignored").WithDetails(detail, detail)
	if e != nil {
		t.Fatal(e)
	}
	for _, err := range []error{nil, status.Error(codes.FailedPrecondition, "immutable terminal report conflict"), duplicate.Err(), CancelledStepClaimError("step", "original")} {
		if _, ok := ParseTerminalConflict(err, request, "original"); ok {
			t.Fatal("generic or ambiguous error accepted")
		}
	}
}

func TestTerminalPayloadDigestDeterministicAndComplete(t *testing.T) {
	a := &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "job", Status: 4, ArtifactUris: map[string]string{"a": "first", "z": "last"}, Artifacts: []*agentv1.ArtifactResult{{Name: "out", StorageKey: "key", Size: 1<<54 + 3, Sha256: strings.Repeat("b", 64)}}}
	first, err := TerminalPayloadDigest(a)
	if err != nil {
		t.Fatal(err)
	}
	b := proto.Clone(a).(*agentv1.ReportJobRunStatusRequest)
	b.ArtifactUris = map[string]string{"z": "last", "a": "first"}
	second, err := TerminalPayloadDigest(b)
	if err != nil || first != second {
		t.Fatal("map order changed wire digest")
	}
	b.Artifacts[0].Size++
	changed, err := TerminalPayloadDigest(b)
	if err != nil || first == changed {
		t.Fatal("large artifact integer omitted")
	}
	info := TerminalConflictInfo{Kind: "job", AgentID: "agent", RunID: "job", ClaimID: "claim", ClaimSource: "execution_claim", PayloadDigest: first, PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", Attempt: 1, AuthoritativeStatus: 7, ReceiptDigest: strings.Repeat("b", 64)}
	if got, ok := ParseTerminalConflict(TerminalConflictError(info), a, "claim"); !ok || got != info {
		t.Fatal("job conflict did not roundtrip")
	}
	for _, mutate := range []func(*TerminalConflictInfo){func(i *TerminalConflictInfo) { i.JobRunID = "other" }, func(i *TerminalConflictInfo) { i.Attempt = 2 }, func(i *TerminalConflictInfo) { i.StepIndex = 1 }, func(i *TerminalConflictInfo) { i.PreviousStepRunID = "previous" }, func(i *TerminalConflictInfo) { i.AgentID = "" }} {
		bad := info
		mutate(&bad)
		if _, ok := ParseTerminalConflict(TerminalConflictError(bad), a, "claim"); ok {
			t.Fatal("invalid job conflict accepted")
		}
	}
}
