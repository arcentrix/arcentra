// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package service

import (
	"context"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetOrRecordJobInputs 仅为认证原 Agent 的活动运行读取或一次性固定输入
func (a *AgentServiceImpl) GetOrRecordJobInputs(ctx context.Context, req *agentv1.GetOrRecordJobInputsRequest) (*agentv1.GetOrRecordJobInputsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "input execution identity is required")
	}
	agentID, err := authenticatedAgent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	identity := taskqueue.JobInputIdentity{PipelineID: req.PipelineId, PipelineRunID: req.PipelineRunId, JobRunID: req.JobRunId, AgentID: agentID, ClaimID: req.OriginalClaimId, RuntimeBindingDigest: req.RuntimeBindingDigest, SourceBindingDigest: req.SourceBindingDigest}
	if err := identity.Validate(); err != nil {
		return nil, status.Error(codes.InvalidArgument, "exact input execution identity is required")
	}
	claim, _, err := reportClaim(ctx, 3, 3, identity.ClaimID)
	if err != nil {
		return nil, err
	}
	if claim != identity.ClaimID {
		return nil, status.Error(codes.PermissionDenied, "input execution claim mismatch")
	}
	var proposed *taskqueue.JobInputManifest
	if req.ManifestJson != "" {
		proposed, err = taskqueue.ParseJobInputManifest(req.ManifestJson)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "canonical input manifest is required")
		}
	}
	if a.agentService == nil {
		return nil, status.Error(codes.Unavailable, "job repository unavailable")
	}
	// New expectations require an explicitly implemented transactional repository
	// contract. Ordinary storage cannot authorize this extension by accident.
	if req.SourceExpectationDigest != "" {
		if !sourceprovenance.ValidBindingDigest(req.SourceExpectationDigest) {
			return nil, status.Error(codes.InvalidArgument, "invalid source expectation digest")
		}
		return a.getOrRecordExpectedSourceInputs(ctx, req, identity, proposed)
	}
	store, ok := a.agentService.jobRunRepo.(interface {
		GetOrRecordJobInputs(context.Context, taskqueue.JobInputIdentity, *taskqueue.JobInputManifest) (*model.JobRun, error)
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "immutable input persistence unavailable")
	}
	job, err := store.GetOrRecordJobInputs(ctx, identity, proposed)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "immutable input identity, authority or live execution cannot be confirmed")
	}
	return &agentv1.GetOrRecordJobInputsResponse{State: job.InputManifestState, ManifestJson: job.InputManifest, ManifestDigest: job.InputManifestDigest}, nil
}

func validatePinnedInputReport(job *model.JobRun, req *agentv1.ReportJobRunStatusRequest) error {
	if !model.IsJobRunTerminal(int(req.Status)) {
		return nil
	}
	if job.InputExpectation == "" && job.InputManifestState != "pinned" && job.InputManifest == "" && job.InputManifestDigest == "" {
		return nil
	}
	if job.InputManifestState != "pinned" {
		if int(req.Status) == model.JobRunStatusSuccess || len(req.Artifacts) > 0 || len(req.ArtifactUris) > 0 {
			return status.Error(codes.FailedPrecondition, "successful execution or outputs require pinned container inputs")
		}
		return nil
	}
	manifest, err := taskqueue.ParseJobInputManifest(job.InputManifest)
	if err != nil {
		return status.Error(codes.FailedPrecondition, "stored input manifest is invalid")
	}
	identity := taskqueue.JobInputIdentity{PipelineID: job.PipelineID, PipelineRunID: job.PipelineRunID, JobRunID: job.JobRunID, AgentID: job.AgentID, ClaimID: job.ClaimID, RuntimeBindingDigest: job.RuntimeBindingDigest, SourceBindingDigest: job.SourceBindingDigest}
	digest, err := manifest.Digest(identity)
	if err != nil || digest != job.InputManifestDigest {
		return status.Error(codes.FailedPrecondition, "stored input manifest does not match execution")
	}
	expectation := *manifest
	expectation.SourceCommit = ""
	encoded, err := expectation.Canonical()
	if err != nil || encoded != job.InputExpectation {
		return status.Error(codes.FailedPrecondition, "stored input manifest does not match dispatch")
	}
	if (job.SourceBindingDigest == "" && manifest.SourceCommit != "") || (job.SourceBindingDigest != "" && manifest.SourceCommit == "") {
		return status.Error(codes.FailedPrecondition, "stored source input is invalid")
	}
	if req.SourceCheckout != nil {
		if manifest.SourceCommit == "" || req.SourceCheckout.BindingDigest != job.SourceBindingDigest || req.SourceCheckout.ResolvedCommit != manifest.SourceCommit {
			return status.Error(codes.FailedPrecondition, "terminal source checkout contradicts pinned input")
		}
	} else if int(req.Status) == model.JobRunStatusSuccess && manifest.SourceCommit != "" {
		return status.Error(codes.FailedPrecondition, "successful source checkout must match pinned input")
	}
	return nil
}

// ExpectedSourceInputRepository 定义已接入 JobRunRepo 的受保护来源观察事务
// The implementation validates the exact claimed Gate/seal and expected SHA in
// the same transaction as input pinning/fresh pending approval creation. It checks
// the enabled original Agent, not human execution authority; startup admission
// separately enforces current requester/voter policy. An initial nil proposal
// remains unknown without pinning or approval. Ordinary inputs reject protected
// rows when the extension is omitted (old Agent/anti-downgrade guard).
type ExpectedSourceInputRepository interface {
	GetOrRecordJobInputsWithSourceExpectation(context.Context, taskqueue.JobInputIdentity, string, *taskqueue.JobInputManifest) (*model.JobRun, *taskqueue.SourceCheckoutExpectation, error)
}

func (a *AgentServiceImpl) getOrRecordExpectedSourceInputs(ctx context.Context, req *agentv1.GetOrRecordJobInputsRequest, identity taskqueue.JobInputIdentity, proposed *taskqueue.JobInputManifest) (*agentv1.GetOrRecordJobInputsResponse, error) {
	store, ok := a.agentService.jobRunRepo.(ExpectedSourceInputRepository)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "expected source observation unavailable")
	}
	job, expectation, err := store.GetOrRecordJobInputsWithSourceExpectation(ctx, identity, req.SourceExpectationDigest, proposed)
	if err != nil || job == nil || expectation == nil {
		return nil, status.Error(codes.FailedPrecondition, "expected source observation cannot be confirmed")
	}
	if job.PipelineID != identity.PipelineID || job.PipelineRunID != identity.PipelineRunID || job.JobRunID != identity.JobRunID || job.AgentID != identity.AgentID || job.ClaimID != identity.ClaimID || job.RuntimeBindingDigest != identity.RuntimeBindingDigest || job.SourceBindingDigest != identity.SourceBindingDigest {
		return nil, status.Error(codes.FailedPrecondition, "source expectation execution mismatch")
	}
	digest, err := expectation.Digest(identity)
	if err != nil || digest != req.SourceExpectationDigest || expectation.ValidateObservation(proposed) != nil {
		return nil, status.Error(codes.FailedPrecondition, "source expectation mismatch")
	}
	body, err := expectation.Canonical()
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "invalid source expectation")
	}
	if job.InputManifestState == "unknown" {
		if proposed != nil || job.InputManifest != "" || job.InputManifestDigest != "" || job.RetryApprovalID != "" || job.RetryApprovalEvidenceHash != "" {
			return nil, status.Error(codes.FailedPrecondition, "unobserved source has observation evidence")
		}
	} else if job.InputManifestState == "pinned" {
		manifest, parseErr := taskqueue.ParseJobInputManifest(job.InputManifest)
		if parseErr != nil || expectation.ValidateObservation(manifest) != nil {
			return nil, status.Error(codes.FailedPrecondition, "invalid observed source")
		}
		if proposed != nil {
			encoded, encodeErr := proposed.Canonical()
			if encodeErr != nil || encoded != job.InputManifest {
				return nil, status.Error(codes.FailedPrecondition, "observation receipt mismatch")
			}
		}
		actualDigest, digestErr := manifest.Digest(identity)
		if digestErr != nil || actualDigest != job.InputManifestDigest {
			return nil, status.Error(codes.FailedPrecondition, "invalid observed input identity")
		}
	} else {
		return nil, status.Error(codes.FailedPrecondition, "invalid input observation state")
	}
	return &agentv1.GetOrRecordJobInputsResponse{State: job.InputManifestState, ManifestJson: job.InputManifest, ManifestDigest: job.InputManifestDigest, SourceExpectationJson: body, SourceExpectationDigest: digest}, nil
}
