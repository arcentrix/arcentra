package service

import (
	"context"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func confirmCancelledJobReceipt(ctx context.Context, repository any, expected *model.JobRun, claim, digest string, sourceUpdates map[string]any) error {
	if expected.ClaimID != claim || expected.CancellationClaimID != claim {
		return status.Error(codes.PermissionDenied, "original cancellation claim required")
	}
	if expected.TerminalReportDigest != "" {
		if err := validateTerminalReplay(expected.TerminalReportDigest, digest); err != nil {
			return err
		}
		if err := validateSourceReceiptReplay(expected, sourceUpdates); err != nil {
			return err
		}
		return verifySourceReplay(ctx, repository, expected, claim)
	}
	writer, ok := repository.(interface {
		CommitCancellationReceiptWithSource(context.Context, *model.JobRun, string, string, map[string]any) error
	})
	if !ok {
		return status.Error(codes.Unimplemented, "atomic cancellation receipt unavailable")
	}
	if err := writer.CommitCancellationReceiptWithSource(ctx, expected, claim, digest, sourceUpdates); err != nil {
		return status.Error(codes.Internal, "commit cancellation receipt failed")
	}
	return nil
}

func confirmCancelledStepReceipt(ctx context.Context, repository any, expected *model.StepRun, claim, digest string) error {
	want := expected.ClaimID
	if want == "" {
		want = agentidentity.StepClaimID(expected.CancellationClaimID, expected.StepRunID)
	}
	if want != claim {
		return status.Error(codes.PermissionDenied, "original cancellation attempt claim required")
	}
	if expected.TerminalReportDigest != "" {
		return validateTerminalReplay(expected.TerminalReportDigest, digest)
	}
	writer, ok := repository.(interface {
		CommitCancellationReceipt(context.Context, *model.StepRun, string, string) error
	})
	if !ok {
		return status.Error(codes.Unimplemented, "atomic cancellation receipt unavailable")
	}
	if err := writer.CommitCancellationReceipt(ctx, expected, claim, digest); err != nil {
		return status.Error(codes.Internal, "commit cancellation receipt failed")
	}
	return nil
}
