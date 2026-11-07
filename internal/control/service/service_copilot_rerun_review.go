// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

// This internal constructor captures immutable startup configuration. Reconfiguration
// requires a new builder/controller; it must never be used as a hot-reload fence.
// Candidate eligibility is deliberately recomputed through the transaction view.
func newCopilotRerunReviewBuilder(config *retryapproval.Policy, runtime taskqueue.OfflineRuntimePolicy, secrets *SecretService) (repo.CopilotRerunReviewBuilder, error) {
	policy, err := normalizeRetryApprovalPolicy(config)
	if err != nil || policy == nil || runtime.Validate() != nil || !runtime.Enabled || !runtime.AllowWholeJobRetry || !runtime.AllowWholeJobRetryApproval || secrets == nil {
		return nil, repo.ErrCopilotRerunUnsupported
	}
	if _, err := newCopilotRerunCipher(secrets.encryptKey); err != nil {
		return nil, repo.ErrCopilotRerunUnsupported
	}
	runtime = runtime.Clone()
	key := &SecretService{encryptKey: slices.Clone(secrets.encryptKey)}
	return func(ctx context.Context, view *repo.Repositories, scope *repo.CopilotRerunScope, owner string, snapshot *model.CopilotDefinitionSnapshot, evidence *repo.CopilotRerunOriginalEvidence, ids *repo.CopilotRerunExecutionIdentities) (*repo.CopilotRerunReviewedTarget, error) {
		if ctx.Err() != nil || view == nil || scope == nil || scope.Project == nil || scope.Pipeline == nil || scope.Run == nil || scope.Job == nil || snapshot == nil || evidence == nil {
			return nil, repo.ErrCopilotRerunUnsupported
		}
		if scope.Project.ProjectID != snapshot.ProjectID || scope.Pipeline.PipelineID != snapshot.PipelineID || scope.Pipeline.ProjectID != snapshot.ProjectID {
			return nil, repo.ErrCopilotRerunUnsupported
		}
		actual, err := repo.BuildCopilotRerunOriginalEvidence(snapshot, scope.Run, scope.Job, scope.JobCount, time.Now().UTC())
		if err != nil {
			return nil, repo.ErrCopilotRerunUnsupported
		}
		actualDigest, err := actual.Digest()
		if err != nil {
			return nil, repo.ErrCopilotRerunUnsupported
		}
		suppliedDigest, err := evidence.Digest()
		if err != nil || suppliedDigest != actualDigest {
			return nil, repo.ErrCopilotRerunUnsupported
		}
		parsed, _, refs, err := openCopilotDefinitionSnapshot(snapshot, runtime, key)
		if err != nil {
			return nil, repo.ErrCopilotRerunUnsupported
		}
		// Reference-bearing definitions need the transaction-bound metadata seam;
		// never resolve values through an independently committing SecretService.
		if len(refs) != 0 {
			return nil, repo.ErrCopilotRerunUnsupported
		}
		layout, err := copilotDefinitionLayout(parsed, runtime, policy.TimeoutSeconds)
		if err != nil || ids.Validate(&layout) != nil {
			return nil, repo.ErrCopilotRerunUnsupported
		}
		canonical, err := layout.Canonical()
		// A changed configured timeout cannot rewrite the saved execution layout;
		// even a new Prepare remains unsupported for this original snapshot.
		if err != nil || canonical != snapshot.LayoutJSON {
			return nil, repo.ErrCopilotRerunUnsupported
		}
		source := parsed.Jobs[0].Source
		if source.Type != evidence.Source.Type || source.Repo != evidence.Source.Repository || source.Branch != evidence.Source.Ref {
			return nil, repo.ErrCopilotRerunUnsupported
		}
		selected, err := copilotRerunCurrentApprovalPolicy(ctx, view, owner, snapshot.ProjectID, snapshot.PipelineID, policy)
		if err != nil {
			return nil, err
		}
		reviewed := retryapproval.RunContext{Version: 1, ProjectID: snapshot.ProjectID, PipelineID: snapshot.PipelineID, PipelineRunID: ids.RunID, ActorID: owner, DefinitionCommitSHA: snapshot.DefinitionRevision, DefinitionPath: snapshot.DefinitionPath, ResolvedDefinitionDigest: snapshot.ResolvedSpecDigest, GatedJobs: []string{snapshot.JobName}, Policy: *selected}
		body, err := reviewed.Canonical()
		if err != nil {
			return nil, repo.ErrCopilotRerunUnsupported
		}
		digest, err := reviewed.Digest()
		if err != nil {
			return nil, repo.ErrCopilotRerunUnsupported
		}
		admission, err := json.Marshal(struct {
			Configured retryapproval.Policy
			Selected   retryapproval.Policy
			Runtime    taskqueue.OfflineRuntimePolicy
		}{*policy, *selected, runtime})
		if err != nil || ctx.Err() != nil {
			return nil, repo.ErrCopilotRerunUnsupported
		}
		return &repo.CopilotRerunReviewedTarget{RunContext: body, RunContextDigest: digest, AdmissionPolicyDigest: definitionBytesDigest("arcentra-rerun-reviewed-admission-v1", admission), ExpectedSourceCommit: evidence.SourceCommit, Layout: &layout}, nil
	}, nil
}

func copilotRerunCurrentApprovalPolicy(ctx context.Context, view *repo.Repositories, owner, project, pipeline string, configured *retryapproval.Policy) (*retryapproval.Policy, error) {
	if configured == nil || authorizeRetryActor(ctx, view, owner, project, pipeline, "") != nil {
		return nil, repo.ErrCopilotRerunForbidden
	}
	selected := *configured
	selected.Candidates = nil
	for _, candidate := range configured.Candidates {
		if candidate != owner && authorizeRetryActor(ctx, view, candidate, project, pipeline, "approval:approve") == nil {
			selected.Candidates = append(selected.Candidates, candidate)
		}
	}
	if selected.Validate(owner) != nil || ctx.Err() != nil {
		return nil, repo.ErrCopilotRerunForbidden
	}
	return &selected, nil
}
