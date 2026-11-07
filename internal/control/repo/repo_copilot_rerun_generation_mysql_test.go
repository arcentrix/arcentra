// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Called by the disposable required gate in both collations. A separate
// connection holds the final Schedule lock; performance_schema proves waiting.
func rerunGenerationMySQLScenario(t *testing.T, db *gorm.DB, r *Repositories, saved *model.CopilotRerunIntent, in CopilotRerunClaimInput, scenario string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	workers := newRerunMySQLWorkers(ctx)
	ctx = workers.ctx
	var removeAuthorizer func()
	defer func() {
		workers.finish()
		if removeAuthorizer != nil {
			removeAuthorizer()
		}
	}()
	_, err := r.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	c, err := retryapproval.ParseRunContext(saved.ReviewedRunContext)
	require.NoError(t, err)
	for _, id := range c.Policy.Candidates {
		var n int64
		require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", id).Count(&n).Error)
		if n == 0 {
			require.NoError(t, db.Create(&model.User{UserID: id, IsEnabled: 1}).Error)
		}
	}
	require.NoError(t, SetRetryApprovalAuthorizer(r, func(ctx context.Context, view RetryApprovalAuthorizationView, _ *retryapproval.Evidence, id, action string) error {
		user, err := view.LiveRepositories().User.(IExecutionActorReader).GetExecutionActor(ctx, id)
		if err != nil || user == nil || user.UserID != id || user.IsEnabled != 1 {
			return ErrApprovalForbidden
		}
		if scenario == "generation-startup-grant-expiry" {
			_, err = view.LiveRepositories().RoleGrant.ListAllEnabled(ctx)
			if err != nil {
				return err
			}
		}
		return nil
	}))
	removeAuthorizer = func() { require.NoError(t, SetRetryApprovalAuthorizer(r, nil)) }
	jobs := r.JobRun.(*JobRunRepo)
	job, err := jobs.GetByJobRunID(ctx, in.JobRunID)
	require.NoError(t, err)
	identity := generationIdentity(job)
	admit := taskqueue.JobGenerationTransition{Version: 1, Operation: "admit", Identity: identity, RetryPlanDigest: job.RetryPlanDigest}
	admit.RequestDigest, err = admit.Digest()
	require.NoError(t, err)
	generation, err := jobs.ReconcileJobGeneration(ctx, admit)
	require.NoError(t, err)
	require.True(t, generation.PreparationOnly)
	expected := taskqueue.SourceCheckoutExpectation{Version: 1, ScheduleID: in.ScheduleID, SealID: in.SealID, ExpectedSourceCommit: job.ExpectedSourceCommit}
	digest, err := expected.Digest(identity)
	require.NoError(t, err)
	unknown, _, err := jobs.GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, nil)
	require.NoError(t, err)
	require.Empty(t, unknown.RetryApprovalID)
	manifest, err := taskqueue.NewJobInputManifest(job.ExpectedSourceCommit, nil, nil)
	require.NoError(t, err)
	job, _, err = jobs.GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, manifest)
	require.NoError(t, err)
	request := retryapproval.Request{Version: 1, Operation: "admit", Identity: identity, GenerationID: generation.GenerationID, GenerationBindingDigest: generation.BindingDigest, RetryPlanDigest: job.RetryPlanDigest, InputManifestDigest: job.InputManifestDigest}
	denied, err := jobs.ReconcileJobRetryApproval(ctx, request)
	require.Error(t, err)
	require.Nil(t, denied)
	approvals := r.Approval.(*ApprovalRepo)
	if scenario != "generation-vote-cancel" {
		_, err = approvals.Decide(ctx, job.RetryApprovalID, c.Policy.Candidates[0], model.ApprovalDecisionApprove, "")
		require.NoError(t, err)
	}
	if scenario == "generation-bootstrap" {
		got, err := jobs.ReconcileJobRetryApproval(ctx, request)
		require.NoError(t, err)
		require.True(t, got.ExecutionAllowed)
		require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", saved.OwnerUserID).Update("is_enabled", 0).Error)
		_, err = jobs.ReconcileJobRetryApproval(ctx, request)
		require.Error(t, err)
		_, err = approvals.Decide(ctx, job.RetryApprovalID, c.Policy.Candidates[0], model.ApprovalDecisionApprove, "")
		require.NoError(t, err)
		return
	}
	expiry := time.Now().UTC().Truncate(time.Millisecond).Add(time.Second)
	if scenario == "generation-startup-expiry" {
		var approval model.ApprovalRequest
		require.NoError(t, db.Where("approval_id = ?", job.RetryApprovalID).First(&approval).Error)
		evidence, err := retryapproval.ParseEvidence(approval.EvidenceJSON)
		require.NoError(t, err)
		evidence.ExpiresAtUnixMilli = expiry.UnixMilli()
		body, err := evidence.Canonical()
		require.NoError(t, err)
		hash, err := evidence.Digest()
		require.NoError(t, err)
		require.NoError(t, db.Model(&model.ApprovalRequest{}).Where("approval_id = ?", job.RetryApprovalID).Updates(map[string]any{"expires_at": expiry, "evidence_json": body, "evidence_hash": hash}).Error)
		require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", job.JobRunID).Update("retry_approval_evidence_hash", hash).Error)
	}
	if scenario == "generation-startup-grant-expiry" {
		require.NoError(t, db.Create(&model.RoleGrant{GrantID: "rerun-startup-expiring", SubjectType: "user", SubjectID: saved.OwnerUserID, RoleID: "synthetic-role", ScopeType: "project", ScopeID: saved.ProjectID, IsEnabled: 1, ExpiresAt: &expiry}).Error)
	}
	first, holder := rerunMySQLConnection(t, db)
	second, waiter := rerunMySQLConnection(t, db)
	held, release := make(chan struct{}), workers.release
	holderDone := workers.start(func() error {
		return first.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var schedule model.CopilotRerunSchedule
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("schedule_id = ?", in.ScheduleID).First(&schedule).Error; err != nil {
				return err
			}
			close(held)
			<-release
			if scenario == "generation-vote-cancel" {
				return tx.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", in.ScheduleID).Update("cancel_requested", true).Error
			}
			return nil
		})
	})
	select {
	case <-held:
	case err := <-holderDone:
		t.Fatalf("holder ended: %v", err)
	case <-ctx.Done():
		t.Fatal("holder timeout")
	}
	other := NewRepositories(statusTestDB{second}, nil)
	operationDone := workers.start(func() error {
		if scenario == "generation-vote-cancel" {
			_, err := other.Approval.(*ApprovalRepo).Decide(ctx, job.RetryApprovalID, c.Policy.Candidates[0], model.ApprovalDecisionApprove, "")
			return err
		}
		_, err := other.JobRun.(*JobRunRepo).ReconcileJobRetryApproval(ctx, request)
		return err
	})
	rerunMySQLWaitEdge(t, db, holder, waiter, operationDone)
	if scenario != "generation-vote-cancel" {
		time.Sleep(time.Until(expiry) + 10*time.Millisecond)
	}
	workers.releaseHolder()
	holderErr, operationErr := awaitRetryMySQL(t, holderDone), awaitRetryMySQL(t, operationDone)
	require.NoError(t, holderErr)
	require.Error(t, operationErr)
	var stored model.JobRunGeneration
	require.NoError(t, db.Where("generation_id = ?", generation.GenerationID).First(&stored).Error)
	require.Empty(t, stored.RetryApprovalAdmission)
	require.Equal(t, "preparing", stored.State)
	if scenario == "generation-vote-cancel" {
		var n int64
		require.NoError(t, db.Model(&model.ApprovalDecision{}).Where("approval_id = ?", job.RetryApprovalID).Count(&n).Error)
		require.Zero(t, n)
	}
}
