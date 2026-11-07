// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
)

// CopilotRerunReviewBuilder is a trusted internal service boundary, never an
// HTTP payload. It must verify encrypted prospective content and current policy,
// requester/candidate permissions and reference access using the supplied scoped
// repositories. It must not perform network calls or start nested transactions.
// Its policy configuration must remain immutable through the outer transaction.
type CopilotRerunReviewBuilder func(context.Context, *Repositories, *CopilotRerunScope, string, *model.CopilotDefinitionSnapshot, *CopilotRerunOriginalEvidence, *CopilotRerunExecutionIdentities) (*CopilotRerunReviewedTarget, error)

type CopilotRerunReviewedTarget struct {
	RunContext            string
	RunContextDigest      string
	AdmissionPolicyDigest string
	ExpectedSourceCommit  string
	Layout                *CopilotRerunLayout
}

type CopilotRerunConfirmInput struct {
	OwnerUserID, ProjectID, IntentID                       string
	ExpectedVersion                                        uint64
	ExpectedIntentDigest, ExpectedReviewedRunContextDigest string
}

func validateCopilotRerunReviewedTarget(v *CopilotRerunReviewedTarget, snapshot *model.CopilotDefinitionSnapshot, evidence *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, owner string) error {
	if v == nil || snapshot == nil || evidence == nil || ids == nil || !sourceprovenance.ValidBindingDigest(v.AdmissionPolicyDigest) || v.ExpectedSourceCommit != evidence.SourceCommit {
		return ErrCopilotRerunInvalid
	}
	layout, err := ParseCopilotRerunLayout(snapshot.LayoutJSON)
	if err != nil || ids.Validate(layout) != nil || v.Layout == nil {
		return ErrCopilotRerunInvalid
	}
	body, err := v.Layout.Canonical()
	if err != nil || body != snapshot.LayoutJSON {
		return ErrCopilotRerunInvalid
	}
	reviewed, err := retryapproval.ParseRunContext(v.RunContext)
	if err != nil {
		return ErrCopilotRerunInvalid
	}
	canonical, err := reviewed.Canonical()
	if err != nil || canonical != v.RunContext {
		return ErrCopilotRerunInvalid
	}
	digest, err := reviewed.Digest()
	if err != nil || digest != v.RunContextDigest || reviewed.ProjectID != snapshot.ProjectID || reviewed.PipelineID != snapshot.PipelineID || reviewed.PipelineRunID != ids.RunID || reviewed.ActorID != owner || reviewed.DefinitionCommitSHA != snapshot.DefinitionRevision || reviewed.DefinitionPath != snapshot.DefinitionPath || reviewed.ResolvedDefinitionDigest != snapshot.ResolvedSpecDigest || len(reviewed.GatedJobs) != 1 || reviewed.GatedJobs[0] != snapshot.JobName || reviewed.Policy.TimeoutSeconds != layout.ApprovalTimeoutSeconds {
		return ErrCopilotRerunInvalid
	}
	for i, candidate := range reviewed.Policy.Candidates {
		if candidate == owner || (i > 0 && reviewed.Policy.Candidates[i-1] >= candidate) {
			return ErrCopilotRerunInvalid
		}
	}
	return nil
}
