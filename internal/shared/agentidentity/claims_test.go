package agentidentity

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestStepClaimIDBindsParentAndExactAttempt(t *testing.T) {
	first := StepClaimID("parent", "step")
	if len(first) != 36 || first != StepClaimID("parent", "step") {
		t.Fatal("step claim is not stable or bounded")
	}
	for _, other := range []string{StepClaimID("other-parent", "step"), StepClaimID("parent", "other-attempt"), StepClaimID("paren", "tstep")} {
		if first == other {
			t.Fatal("step claim lost parent/attempt domain separation")
		}
	}
}

func TestCancelledStepClaimSignalIsScoped(t *testing.T) {
	err := CancelledStepClaimError("step", "claim")
	if !IsCancelledStepClaim(err, "step", "claim") {
		t.Fatal("matching controller cancellation signal was lost")
	}
	if IsCancelledStepClaim(err, "other", "claim") || IsCancelledStepClaim(err, "step", "other") || IsCancelledStepClaim(nil, "step", "claim") {
		t.Fatal("unrelated cancellation signal accepted")
	}
}

func TestCancelledStepClaimSignalRejectsGenericAndForgedDetails(t *testing.T) {
	if IsCancelledStepClaim(status.Error(codes.FailedPrecondition, "controller cancelled this step claim"), "step", "claim") {
		t.Fatal("generic precondition rejection was treated as cancellation authority")
	}
	for _, test := range []struct {
		name, domain, reason string
		code                 codes.Code
	}{
		{"domain", "other", cancellationClaimReason, codes.FailedPrecondition},
		{"reason", cancellationClaimDomain, "OTHER", codes.FailedPrecondition},
		{"status", cancellationClaimDomain, cancellationClaimReason, codes.Canceled},
	} {
		result, err := status.New(test.code, "cancellation").WithDetails(&errdetails.ErrorInfo{Domain: test.domain, Reason: test.reason, Metadata: map[string]string{"stepRunId": "step", "claimId": "claim"}})
		if err != nil {
			t.Fatal(err)
		}
		if IsCancelledStepClaim(result.Err(), "step", "claim") {
			t.Fatalf("wrong %s accepted", test.name)
		}
	}
}
