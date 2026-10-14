// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package taskqueue

import (
	"context"
	"fmt"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
)

// checkoutJobInputs 在容器暂存前固定输入；旧裸机流程保持原有语义
func checkoutJobInputs(ctx context.Context, client *agentgrpc.ClientWrapper, conf *config.AgentConfig, payload *taskpayload.JobRunTaskPayload, workspace string) (*agentv1.SourceCheckout, error) {
	if payload.Runtime == nil {
		return handleSourceClone(ctx, payload.Source, workspace)
	}
	if err := validateJobSourceBinding(payload); err != nil {
		return nil, err
	}
	if client == nil || client.AgentClient == nil || conf == nil || conf.Agent.ID == "" || payload.AgentID != conf.Agent.ID {
		return nil, fmt.Errorf("immutable container inputs require an authenticated controller")
	}
	sourceBinding := ""
	if payload.Source != nil {
		sourceBinding = payload.Source.BindingDigest
		if sourceBinding == "" {
			return nil, fmt.Errorf("immutable container source binding is required")
		}
	}
	identity := taskpayload.JobInputIdentity{PipelineID: payload.PipelineID, PipelineRunID: payload.PipelineRunID, JobRunID: payload.JobRunID, AgentID: conf.Agent.ID, ClaimID: executionClaimID(ctx, "job", payload.JobRunID), RuntimeBindingDigest: payload.Runtime.BindingDigest, SourceBindingDigest: sourceBinding}
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	expected, err := taskpayload.NewJobInputManifest("", payload.ArtifactURIs, payload.ArtifactReferences)
	if err != nil {
		return nil, err
	}
	req := &agentv1.GetOrRecordJobInputsRequest{AgentId: identity.AgentID, PipelineId: identity.PipelineID, PipelineRunId: identity.PipelineRunID, JobRunId: identity.JobRunID, OriginalClaimId: identity.ClaimID, RuntimeBindingDigest: identity.RuntimeBindingDigest, SourceBindingDigest: identity.SourceBindingDigest}
	call := func() (*agentv1.GetOrRecordJobInputsResponse, error) {
		rpcCtx, cancel := client.WithTimeoutAndAuth(ctx)
		defer cancel()
		return client.AgentClient.GetOrRecordJobInputs(withExecutionClaim(rpcCtx, "job", payload.JobRunID), req)
	}
	response, err := call()
	if err != nil {
		return nil, fmt.Errorf("read immutable job inputs: %w", err)
	}
	pinned, err := verifyJobInputResponse(response, identity, expected, true)
	if err != nil {
		return nil, err
	}
	commit := ""
	if pinned != nil {
		commit = pinned.SourceCommit
	}
	checkout, err := handleSourceCloneAtCommit(ctx, payload.Source, workspace, commit)
	if err != nil {
		return checkout, err
	}
	if checkout != nil {
		expected.SourceCommit = checkout.ResolvedCommit
	}
	if pinned != nil && expected.SourceCommit != pinned.SourceCommit {
		return checkout, fmt.Errorf("source checkout differs from pinned input commit")
	}
	encoded, err := expected.Canonical()
	if err != nil {
		return checkout, err
	}
	req.ManifestJson = encoded
	response, err = call()
	if err != nil {
		return checkout, fmt.Errorf("record immutable job inputs: %w", err)
	}
	recorded, err := verifyJobInputResponse(response, identity, expected, false)
	if err != nil {
		return checkout, err
	}
	recordedJSON, err := recorded.Canonical()
	if err != nil || recordedJSON != encoded {
		return checkout, fmt.Errorf("controller did not confirm exact job inputs")
	}
	return checkout, nil
}

func verifyJobInputResponse(response *agentv1.GetOrRecordJobInputsResponse, identity taskpayload.JobInputIdentity, expected *taskpayload.JobInputManifest, allowUnknown bool) (*taskpayload.JobInputManifest, error) {
	if response == nil {
		return nil, fmt.Errorf("controller returned no immutable input state")
	}
	if allowUnknown && response.State == "unknown" && response.ManifestJson == "" && response.ManifestDigest == "" {
		return nil, nil
	}
	if response.State != "pinned" {
		return nil, fmt.Errorf("controller did not confirm pinned input state")
	}
	manifest, err := taskpayload.ParseJobInputManifest(response.ManifestJson)
	if err != nil {
		return nil, err
	}
	digest, err := manifest.Digest(identity)
	if err != nil || digest != response.ManifestDigest {
		return nil, fmt.Errorf("controller input digest does not match execution identity")
	}
	if (identity.SourceBindingDigest != "" && manifest.SourceCommit == "") || (identity.SourceBindingDigest == "" && manifest.SourceCommit != "") {
		return nil, fmt.Errorf("controller source input does not match job")
	}
	copy := *manifest
	copy.SourceCommit = ""
	expectedCopy := *expected
	expectedCopy.SourceCommit = ""
	actualJSON, err := copy.Canonical()
	if err != nil {
		return nil, err
	}
	expectedJSON, err := expectedCopy.Canonical()
	if err != nil || actualJSON != expectedJSON {
		return nil, fmt.Errorf("controller inputs do not match dispatched artifact set")
	}
	return manifest, nil
}
