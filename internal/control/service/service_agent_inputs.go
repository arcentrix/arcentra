// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package service

import (
	"context"
	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
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
