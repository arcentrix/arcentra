// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCopilotRerunPrepareBindsFreshReviewedIdentity(t *testing.T) {
	r, in := rerunIntentFixture(t)
	saved, err := r.CopilotRerunPrepare(context.Background(), in, permitRerun, testRerunReview)
	require.NoError(t, err)
	require.True(t, rerunUUID(saved.GateID))
	reviewed, err := retryapproval.ParseRunContext(saved.ReviewedRunContext)
	require.NoError(t, err)
	require.Equal(t, saved.NewRunID, reviewed.PipelineRunID)
	require.Equal(t, in.OwnerUserID, reviewed.ActorID)
	require.NotEqual(t, saved.OriginalRunID, reviewed.PipelineRunID)
	require.NotEmpty(t, saved.ExpectedSourceCommit)
	require.NotEmpty(t, saved.ReviewedAdmissionPolicyDigest)
	// A historical request response retains its reviewed receipt even if current
	// policy changes; only a later fresh confirmation re-evaluates that policy.
	deniedReview := func(context.Context, *Repositories, *CopilotRerunScope, string, *model.CopilotDefinitionSnapshot, *CopilotRerunOriginalEvidence, *CopilotRerunExecutionIdentities) (*CopilotRerunReviewedTarget, error) {
		t.Fatal("historical receipt must not rebuild current policy")
		return nil, ErrCopilotRerunForbidden
	}
	replayed, err := r.CopilotRerunPrepare(context.Background(), in, permitRerun, deniedReview)
	require.NoError(t, err)
	require.Equal(t, saved, replayed)
}

func TestCopilotRerunPrepareRejectsInvalidReviewedBinding(t *testing.T) {
	for _, kind := range []string{"missing-builder", "old-run", "wrong-actor", "wrong-project", "wrong-definition", "wrong-sha", "policy-digest", "timeout", "requester-candidate", "missing-gate", "aliased-gate", "missing-legacy-binding"} {
		t.Run(kind, func(t *testing.T) {
			r, in := rerunIntentFixture(t)
			review := CopilotRerunReviewBuilder(func(ctx context.Context, repos *Repositories, scope *CopilotRerunScope, owner string, snapshot *model.CopilotDefinitionSnapshot, evidence *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities) (*CopilotRerunReviewedTarget, error) {
				v, err := testRerunReview(ctx, repos, scope, owner, snapshot, evidence, ids)
				if err != nil {
					return nil, err
				}
				c, err := retryapproval.ParseRunContext(v.RunContext)
				require.NoError(t, err)
				switch kind {
				case "old-run":
					c.PipelineRunID = scope.Run.RunID
				case "wrong-actor":
					c.ActorID = "other"
				case "wrong-project":
					c.ProjectID = "other"
				case "wrong-definition":
					c.DefinitionPath = "other.yaml"
				case "wrong-sha":
					v.ExpectedSourceCommit = "wrong"
				case "policy-digest":
					v.AdmissionPolicyDigest = "wrong"
				case "timeout":
					c.Policy.TimeoutSeconds++
				case "requester-candidate":
					c.Policy.Candidates = []string{owner}
				case "missing-gate":
					ids.GateID = ""
				case "aliased-gate":
					ids.GateID = ids.RunID
				}
				// Recompute policy fingerprint only for a changed configured test policy.
				c.Policy.Fingerprint, _ = c.Policy.Digest()
				v.RunContext, _ = c.Canonical()
				v.RunContextDigest, _ = c.Digest()
				return v, nil
			})
			if kind == "missing-builder" {
				review = nil
			}
			if kind == "missing-legacy-binding" {
				saved, err := r.CopilotRerunPrepare(context.Background(), in, permitRerun, testRerunReview)
				require.NoError(t, err)
				require.NoError(t, r.database.Database().Model(&model.CopilotRerunIntent{}).Where("intent_id = ?", saved.IntentID).Update("reviewed_run_context", "").Error)
			}
			_, err := r.CopilotRerunPrepare(context.Background(), in, permitRerun, review)
			require.Error(t, err)
		})
	}
}
