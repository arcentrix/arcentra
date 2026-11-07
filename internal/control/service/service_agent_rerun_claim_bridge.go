// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"bytes"
	"context"
	"errors"

	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

var errRerunBridgeUnknown = errors.New("protected rerun claim outcome unknown; retain original identity")

// Private and inert: construction never installs this adapter in AgentService.
// No ordinary repository interface is wrapped or granted protected access.
type agentRerunClaimBridge struct {
	repositories *repo.Repositories
	validate     repo.CopilotRerunClaimValidator
}

func newAgentRerunClaimBridge(repositories *repo.Repositories, policy *retryapproval.Policy, runtime taskqueue.OfflineRuntimePolicy, secrets *SecretService) (*agentRerunClaimBridge, error) {
	if repositories == nil || repositories.User == nil || repositories.Project == nil || repositories.Pipeline == nil || repositories.Agent == nil {
		return nil, errRerunBridgeUnknown
	}
	validate, err := newCopilotRerunClaimValidator(policy, runtime, secrets)
	if err != nil {
		return nil, errRerunBridgeUnknown
	}
	return &agentRerunClaimBridge{repositories: repositories, validate: validate}, nil
}
func (b *agentRerunClaimBridge) ClaimAndFetchCopilotRerun(ctx context.Context, in taskqueue.RerunClaimRequest) (*taskqueue.RerunClaimResult, error) {
	if requireRerunConfidentialTransport(ctx) != nil {
		return nil, taskqueue.ErrRerunClaimRejected
	}
	agent, err := authenticatedAgent(ctx, in.Reference.AgentID)
	if err != nil || agent != in.Reference.AgentID {
		return nil, taskqueue.ErrRerunClaimRejected
	}
	if in.Validate() != nil {
		return nil, taskqueue.ErrRerunClaimRejected
	}
	if b == nil || b.repositories == nil || b.validate == nil {
		return nil, errRerunBridgeUnknown
	}
	ref := in.Reference
	request := repo.CopilotRerunClaimInput{AgentID: agent, PipelineID: ref.PipelineID, RunID: ref.RunID, JobRunID: ref.JobRunID, GateID: ref.GateID, ScheduleID: ref.ScheduleID, SealID: ref.SealID, SealDigest: ref.SealDigest, PayloadDigest: ref.PayloadDigest, ClaimID: in.ClaimID, AdmissionNonce: in.AdmissionNonce, ArtifactStorageID: in.ArtifactStorageID, ArtifactStorageFingerprint: in.ArtifactStorageFingerprint}
	authorize := func(ctx context.Context, view *repo.Repositories, scope *repo.CopilotRerunScope, owner, action string) error {
		if action != "claim" || scope == nil || scope.Project == nil || scope.Pipeline == nil || scope.Run == nil || scope.Job == nil || scope.Pipeline.PipelineID != ref.PipelineID || scope.Pipeline.ProjectID != scope.Project.ProjectID || scope.Run.RunID != ref.RunID || scope.Run.PipelineID != ref.PipelineID || scope.Run.ProjectID != scope.Project.ProjectID || scope.Job.JobRunID != ref.JobRunID || scope.Job.PipelineRunID != ref.RunID || scope.Job.PipelineID != ref.PipelineID || scope.Run.TriggeredBy != owner {
			return repo.ErrCopilotRerunForbidden
		}
		return authorizeRetryActor(ctx, view, owner, scope.Project.ProjectID, ref.PipelineID, "")
	}
	out, err := b.repositories.CopilotRerunClaim(ctx, request, authorize, b.validate)
	if err != nil {
		return nil, rerunBridgeError(err)
	}
	if out == nil || out.Receipt == nil {
		return nil, errRerunBridgeUnknown
	}
	r := out.Receipt
	result := &taskqueue.RerunClaimResult{Receipt: taskqueue.RerunClaimReceipt{Reference: taskqueue.RerunClaimReference{AgentID: r.AgentID, PipelineID: r.PipelineID, RunID: r.RunID, JobRunID: r.JobRunID, GateID: r.GateID, ScheduleID: r.ScheduleID, SealID: r.SealID, SealDigest: r.SealDigest, PayloadDigest: r.PayloadDigest}, ClaimID: r.ClaimID, ClaimedAtUnixMilli: r.ClaimedAtUnixMilli, ArtifactStorageID: r.ArtifactStorageID, ArtifactStorageFingerprint: r.ArtifactStorageFingerprint, ArtifactStorageType: r.ArtifactStorageType}}
	if !result.Receipt.Matches(in) || !validRerunClaimPayload(ref, out.PayloadJSON) {
		return nil, errRerunBridgeUnknown
	}
	result.PayloadJSON = bytes.Clone(out.PayloadJSON)
	return result, nil
}

func rerunBridgeError(err error) error {
	if errors.Is(err, repo.ErrCopilotRerunClaimRejected) {
		return taskqueue.ErrRerunClaimRejected
	}
	return errRerunBridgeUnknown
}
