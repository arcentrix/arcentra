package service

import (
	"context"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func prepareJobSource(job *model.JobRun, req *agentv1.ReportJobRunStatusRequest, claim string) (map[string]any, error) {
	if err := validatePinnedInputReport(job, req); err != nil {
		return nil, err
	}
	evidence := req.SourceCheckout
	if evidence == nil {
		if model.IsJobRunTerminal(int(req.Status)) {
			return map[string]any{"source_provenance_state": "unknown", "source_resolved_commit": ""}, nil
		}
		return nil, nil
	}
	if !model.IsJobRunTerminal(int(req.Status)) {
		return nil, status.Error(codes.InvalidArgument, "source checkout requires a terminal report")
	}
	if job.ClaimID == "" || job.ClaimID != claim || job.AgentID == "" {
		return nil, status.Error(codes.PermissionDenied, "source checkout requires the original execution claim")
	}
	if !job.SourceExpectationCaptured || !sourceprovenance.ValidBindingDigest(evidence.BindingDigest) || evidence.BindingDigest != job.SourceBindingDigest {
		return nil, status.Error(codes.FailedPrecondition, "source checkout binding does not match the dispatched job")
	}
	binding, err := sourceprovenance.NewBinding(job.PipelineID, job.PipelineRunID, job.JobRunID, job.SourceType, job.SourceRepository, job.SourceRef)
	if err != nil || binding.Type != "git" || binding.Digest != job.SourceBindingDigest {
		return nil, status.Error(codes.FailedPrecondition, "stored source expectation is invalid")
	}
	if !sourceprovenance.ValidCommit(evidence.ResolvedCommit) {
		return nil, status.Error(codes.InvalidArgument, "source checkout requires a full canonical Git commit")
	}
	return map[string]any{"source_provenance_state": "reported", "source_resolved_commit": evidence.ResolvedCommit}, nil
}

func verifySourceReplay(ctx context.Context, repository any, job *model.JobRun, claim string) error {
	validator, ok := repository.(interface {
		ValidateTerminalConflict(context.Context, *model.JobRun, string) error
	})
	if !ok {
		return status.Error(codes.Unimplemented, "atomic source replay verification unavailable")
	}
	if err := validator.ValidateTerminalConflict(ctx, job, claim); err != nil {
		return status.Error(codes.Internal, "source replay verification failed")
	}
	return nil
}

func validateSourceReceiptReplay(job *model.JobRun, sourceUpdates map[string]any) error {
	state, _ := sourceUpdates["source_provenance_state"].(string)
	commit, _ := sourceUpdates["source_resolved_commit"].(string)
	storedState := job.SourceProvenanceState
	if storedState == "" {
		storedState = "unknown"
	}
	if state != storedState || commit != job.SourceResolvedCommit {
		return status.Error(codes.FailedPrecondition, "stored source result does not match the terminal receipt")
	}
	return nil
}
