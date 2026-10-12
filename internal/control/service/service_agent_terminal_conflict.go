package service

import (
	"context"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func terminalJobConflict(ctx context.Context, repository any, expected *model.JobRun, request proto.Message, claim string) error {
	if expected.ClaimID == "" || expected.ClaimID != claim {
		return status.Error(codes.FailedPrecondition, "original terminal job claim unavailable")
	}
	validator, ok := repository.(interface {
		ValidateTerminalConflict(context.Context, *model.JobRun, string) error
	})
	if !ok {
		return status.Error(codes.Unimplemented, "terminal conflict verification unavailable")
	}
	if err := validator.ValidateTerminalConflict(ctx, expected, claim); err != nil {
		return status.Error(codes.Internal, "terminal conflict verification failed")
	}
	digest, err := agentidentity.TerminalPayloadDigest(request)
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid terminal report")
	}
	return agentidentity.TerminalConflictError(agentidentity.TerminalConflictInfo{Kind: "job", AgentID: expected.AgentID, RunID: expected.JobRunID, ClaimID: claim, ClaimSource: "execution_claim", PayloadDigest: digest, PipelineID: expected.PipelineID, PipelineRunID: expected.PipelineRunID, JobRunID: expected.JobRunID, Attempt: 1, AuthoritativeStatus: expected.Status, ReceiptDigest: expected.TerminalReportDigest})
}

func terminalStepConflict(ctx context.Context, repository any, expected *model.StepRun, request proto.Message, claim string) error {
	if expected.ClaimID == "" && expected.Status == model.JobRunStatusCancelled && expected.CancellationClaimID == "" {
		return status.Error(codes.FailedPrecondition, "original terminal step claim unavailable")
	}
	source := "execution_claim"
	want := expected.ClaimID
	if want == "" {
		source = "terminal_parent_claim"
		if expected.Status == model.JobRunStatusCancelled {
			source = "cancelled_parent_claim"
			want = agentidentity.StepClaimID(expected.CancellationClaimID, expected.StepRunID)
		}
	}
	if want != "" && want != claim {
		return status.Error(codes.PermissionDenied, "original terminal step claim required")
	}
	validator, ok := repository.(interface {
		ValidateTerminalConflict(context.Context, *model.StepRun, string) error
	})
	if !ok {
		return status.Error(codes.Unimplemented, "terminal conflict verification unavailable")
	}
	if err := validator.ValidateTerminalConflict(ctx, expected, claim); err != nil {
		return status.Error(codes.Internal, "terminal conflict verification failed")
	}
	digest, err := agentidentity.TerminalPayloadDigest(request)
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid terminal report")
	}
	return agentidentity.TerminalConflictError(agentidentity.TerminalConflictInfo{Kind: "step", AgentID: expected.AgentID, RunID: expected.StepRunID, ClaimID: claim, ClaimSource: source, PayloadDigest: digest, PipelineID: expected.PipelineID, PipelineRunID: expected.PipelineRunID, JobRunID: expected.JobRunID, Attempt: expected.Attempt, StepIndex: expected.StepIndex, PreviousStepRunID: expected.PreviousStepRunID, AuthoritativeStatus: expected.Status, ReceiptDigest: expected.TerminalReportDigest})
}
