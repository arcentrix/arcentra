// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"

	"github.com/arcentrix/arcentra/internal/control/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func protectedRerunJob(job *model.JobRun) bool {
	return job != nil && (job.CopilotRerunIntentID != "" || job.CopilotRerunGateID != "" || job.ExpectedSourceCommit != "")
}

// A receipt inspection proves only the committed claim. It never returns
// dispatch bytes or grants permission to start another command.
func inspectProtectedRerunReceipt(ctx context.Context, repository any, job *model.JobRun) error {
	if !protectedRerunJob(job) {
		return nil
	}
	inspector, ok := repository.(interface {
		InspectCopilotRerunJobReceipt(context.Context, *model.JobRun) error
	})
	if !ok {
		return status.Error(codes.Unimplemented, "protected execution receipt inspection unavailable")
	}
	if err := inspector.InspectCopilotRerunJobReceipt(ctx, job); err != nil {
		return status.Error(codes.FailedPrecondition, "protected execution receipt is not confirmed")
	}
	return nil
}

// The first cancellation ACK changes the stored digest. Inspect a fresh row,
// rather than incorrectly treating the pre-commit snapshot as the receipt.
func inspectProtectedCancellationReceipt(ctx context.Context, repository any, before *model.JobRun, claim, digest string, sourceUpdates map[string]any) error {
	if !protectedRerunJob(before) {
		return nil
	}
	reader, ok := repository.(interface {
		GetByJobRunID(context.Context, string) (*model.JobRun, error)
	})
	if !ok {
		return status.Error(codes.Unimplemented, "protected execution receipt lookup unavailable")
	}
	current, err := reader.GetByJobRunID(ctx, before.JobRunID)
	if err != nil {
		return status.Error(codes.Internal, "protected execution receipt lookup failed")
	}
	if current == nil || current.JobRunID != before.JobRunID || current.PipelineID != before.PipelineID || current.PipelineRunID != before.PipelineRunID || current.AgentID != before.AgentID || current.CopilotRerunIntentID != before.CopilotRerunIntentID || current.CopilotRerunGateID != before.CopilotRerunGateID || current.ExpectedSourceCommit != before.ExpectedSourceCommit || current.Status != model.JobRunStatusCancelled || current.ClaimID != claim || current.CancellationClaimID != claim || current.ArtifactStorageID != before.ArtifactStorageID || current.ArtifactStorageFingerprint != before.ArtifactStorageFingerprint || current.ArtifactStorageType != before.ArtifactStorageType {
		return status.Error(codes.FailedPrecondition, "protected cancellation receipt identity changed")
	}
	if err := validateTerminalReplay(current.TerminalReportDigest, digest); err != nil {
		return err
	}
	if err := validateSourceReceiptReplay(current, sourceUpdates); err != nil {
		return err
	}
	return inspectProtectedRerunReceipt(ctx, repository, current)
}
