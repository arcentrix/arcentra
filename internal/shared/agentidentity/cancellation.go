package agentidentity

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const cancellationClaimDomain = "arcentra.agent.claim"
const cancellationClaimReason = "CONTROLLER_CANCELLED_STEP"

// CancelledStepClaimError 明确拒绝已由控制面取消的原步骤认领，不确认执行或终态
func CancelledStepClaimError(stepRunID, claim string) error {
	result, err := status.New(codes.FailedPrecondition, "controller cancelled this step claim").WithDetails(&errdetails.ErrorInfo{
		Domain: cancellationClaimDomain, Reason: cancellationClaimReason,
		Metadata: map[string]string{"stepRunId": stepRunID, "claimId": claim},
	})
	if err != nil {
		return status.Error(codes.FailedPrecondition, "controller cancelled step claim")
	}
	return result.Err()
}

// IsCancelledStepClaim 仅识别与本次步骤和原认领完全一致的控制面取消拒绝
func IsCancelledStepClaim(err error, stepRunID, claim string) bool {
	result := status.Convert(err)
	if result.Code() != codes.FailedPrecondition {
		return false
	}
	for _, detail := range result.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if ok && info.Domain == cancellationClaimDomain && info.Reason == cancellationClaimReason && info.Metadata["stepRunId"] == stepRunID && info.Metadata["claimId"] == claim {
			return true
		}
	}
	return false
}
