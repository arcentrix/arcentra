// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Actual history and approval-observe operations must serialize on one shared
// Pipeline prefix. The witness requires a server-side, exact-row lock wait;
// callback arrival or elapsed time never counts as contention evidence.
func historyObserveMySQLContention(t *testing.T, db *gorm.DB, r *Repositories, in CopilotExecutionPrepareInput, review bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := r.CopilotHistoryGet(ctx, in.OwnerUserID, in.ProjectID, in.ConversationID, historyTestAllow)
	require.NoError(t, err)
	var plan *model.CopilotActionPlan
	if review {
		var parent model.CopilotTurn
		require.NoError(t, db.Where("conversation_id = ? AND state = ?", c.ConversationID, "completed").First(&parent).Error)
		var created bool
		plan, created, err = r.CopilotHistoryCreatePlan(ctx, CopilotHistoryPlanInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: parent.TurnID, PlanID: "observe-review-plan", RequestID: "observe-review-request", Kind: "retry_approval_review", PlanDigest: "observe-review-digest", ConversationVersion: c.Version, TurnVersion: parent.Version, SchemaVersion: 1, CipherVersion: 1, PayloadCiphertext: []byte("opaque-repository-fixture")}, historyTestAllow)
		require.NoError(t, err)
		require.True(t, created)
	}
	var beforeJob model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", "job").First(&beforeJob).Error)
	var beforeGeneration model.JobRunGeneration
	require.NoError(t, db.Where("generation_id = ?", beforeJob.CurrentGenerationID).First(&beforeGeneration).Error)
	var beforeApproval model.ApprovalRequest
	require.NoError(t, db.Where("approval_id = ?", beforeJob.RetryApprovalID).First(&beforeApproval).Error)
	_, descriptor, err := loadGenerationRead(ctx, db, beforeJob.CurrentGenerationID, false)
	require.NoError(t, err)
	request := retryapproval.Request{Version: 1, Operation: "observe", Identity: generationIdentity(&beforeJob), GenerationID: beforeGeneration.GenerationID, GenerationBindingDigest: descriptor.BindingDigest, RetryPlanDigest: beforeJob.RetryPlanDigest, InputManifestDigest: beforeJob.InputManifestDigest}

	history := func(tagged context.Context) error {
		if review {
			_, e := r.CopilotHistoryTransitionPlan(tagged, CopilotHistoryPlanTransitionInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, PlanID: plan.PlanID, PlanDigest: plan.PlanDigest, Version: plan.Version, State: "reviewed"}, historyTestAllow)
			return e
		}
		_, _, e := r.CopilotHistoryReserve(tagged, historyReservation(c, "observe-reservation"), historyTestAllow)
		return e
	}
	observe := func(tagged context.Context) error {
		jobs := &JobRunRepo{IDatabase: statusTestDB{db}}
		response, e := jobs.ReconcileJobRetryApproval(tagged, request)
		if e == nil && (response == nil || response.State != "pending" || response.ExecutionAllowed || response.Admission != nil) {
			return errors.New("observe changed execution authority")
		}
		return e
	}
	first, second := history, observe
	if review {
		first, second = observe, history
	}
	executionMySQLPipelineRace(t, ctx, db, &beforeJob, first, second)
	require.NoError(t, ctx.Err())
	var afterJob model.JobRun
	var afterGeneration model.JobRunGeneration
	var afterApproval model.ApprovalRequest
	require.NoError(t, db.Where("job_run_id = ?", beforeJob.JobRunID).First(&afterJob).Error)
	require.NoError(t, db.Where("generation_id = ?", beforeGeneration.GenerationID).First(&afterGeneration).Error)
	require.NoError(t, db.Where("approval_id = ?", beforeApproval.ApprovalID).First(&afterApproval).Error)
	require.Equal(t, beforeJob, afterJob)
	require.Equal(t, beforeGeneration, afterGeneration)
	require.Equal(t, beforeApproval, afterApproval)
	executionMySQLNoVote(t, db)
	t.Log("actual history/approval observe paths: exact Pipeline contention and both commits; votes, approval, job and generation unchanged")
}

func executionMySQLPipelineRace(t *testing.T, ctx context.Context, db *gorm.DB, job *model.JobRun, first, second func(context.Context) error) {
	t.Helper()
	var schema string
	require.NoError(t, db.Raw("SELECT DATABASE()").Scan(&schema).Error)
	fixture := &stepAttemptFixture{generationFixture: &generationFixture{ctx: ctx, db: db, identity: generationIdentity(job)}}
	errs := stepAttemptMySQLOrderedRace(t, fixture, schema, first, second, true)
	require.NoError(t, errs[0], "first actual operation must commit")
	require.NoError(t, errs[1], "second actual operation must commit")
}

// Exercise the production cancellation/expiry entrypoints against live pending
// approval observation. Every ordering uses the same exact shared-lock witness.
func executionMySQLObserveLifecycleContention(t *testing.T, db *gorm.DB, scenario string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	jobs := &JobRunRepo{IDatabase: statusTestDB{db}}
	approvals := &ApprovalRepo{IDatabase: statusTestDB{db}}
	if scenario == "approval-expire-observe-contention" {
		fixture := &generationFixture{ctx: ctx, db: db, jobs: jobs}
		expiry := time.Now().Add(100 * time.Millisecond)
		setRetryApprovalTestExpiry(t, fixture, expiry)
		timer := time.NewTimer(time.Until(expiry) + time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatal("fixture expiry deadline")
		}
	}
	var beforeJob model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", "job").First(&beforeJob).Error)
	var beforeGeneration model.JobRunGeneration
	require.NoError(t, db.Where("generation_id = ?", beforeJob.CurrentGenerationID).First(&beforeGeneration).Error)
	var beforeApproval model.ApprovalRequest
	require.NoError(t, db.Where("approval_id = ?", beforeJob.RetryApprovalID).First(&beforeApproval).Error)
	var beforeRun model.PipelineRun
	require.NoError(t, db.Where("run_id = ?", beforeJob.PipelineRunID).First(&beforeRun).Error)
	_, descriptor, err := loadGenerationRead(ctx, db, beforeJob.CurrentGenerationID, false)
	require.NoError(t, err)
	request := retryapproval.Request{Version: 1, Operation: "observe", Identity: generationIdentity(&beforeJob), GenerationID: beforeGeneration.GenerationID, GenerationBindingDigest: descriptor.BindingDigest, RetryPlanDigest: beforeJob.RetryPlanDigest, InputManifestDigest: beforeJob.InputManifestDigest}
	expectedState := "pending"
	expectedApproval := beforeApproval.Status
	if scenario == "approval-cancel-observe-contention" {
		expectedState = "canceled"
		expectedApproval = model.ApprovalStatusCanceled
	}
	if scenario == "approval-expire-observe-contention" {
		expectedState = "expired"
		expectedApproval = model.ApprovalStatusExpired
	}
	observe := func(c context.Context) error {
		response, e := jobs.ReconcileJobRetryApproval(c, request)
		if e == nil && (response == nil || response.State != expectedState || response.ExecutionAllowed || response.Admission != nil) {
			return errors.New("observe returned unexpected authority")
		}
		return e
	}
	mutate := func(c context.Context) error {
		switch scenario {
		case "run-cancel-observe-contention":
			pipelines := NewPipelineRepo(statusTestDB{db}).(*PipelineRepo)
			return pipelines.UpdateRunIfStatus(c, beforeRun.RunID, beforeRun.Status, map[string]any{"status": model.PipelineStatusCancelled})
		case "approval-cancel-observe-contention":
			_, e := approvals.Cancel(c, beforeApproval.ApprovalID, "synthetic cancellation")
			return e
		case "approval-expire-observe-contention":
			_, e := approvals.Expire(c, beforeApproval.ApprovalID)
			return e
		default:
			return errors.New("unknown lifecycle scenario")
		}
	}
	first, second := mutate, observe
	if scenario == "run-cancel-observe-contention" {
		first, second = observe, mutate
	}
	executionMySQLPipelineRace(t, ctx, db, &beforeJob, first, second)
	var afterJob model.JobRun
	var afterGeneration model.JobRunGeneration
	var afterApproval model.ApprovalRequest
	var afterRun model.PipelineRun
	require.NoError(t, db.Where("job_run_id = ?", beforeJob.JobRunID).First(&afterJob).Error)
	require.NoError(t, db.Where("generation_id = ?", beforeGeneration.GenerationID).First(&afterGeneration).Error)
	require.NoError(t, db.Where("approval_id = ?", beforeApproval.ApprovalID).First(&afterApproval).Error)
	require.NoError(t, db.Where("run_id = ?", beforeRun.RunID).First(&afterRun).Error)
	require.Equal(t, beforeJob, afterJob)
	require.Equal(t, beforeGeneration, afterGeneration)
	require.Equal(t, expectedApproval, afterApproval.Status)
	require.Equal(t, beforeApproval.EvidenceJSON, afterApproval.EvidenceJSON)
	require.Equal(t, beforeApproval.EvidenceHash, afterApproval.EvidenceHash)
	if scenario == "run-cancel-observe-contention" {
		require.Equal(t, model.PipelineStatusCancelled, afterRun.Status)
		require.Equal(t, beforeApproval, afterApproval)
		// After the cancellation commits, the actual observation path must deny
		// fresh authority. This is a separate read, never a retried mutation.
		_, err = jobs.ReconcileJobRetryApproval(ctx, request)
		require.EqualError(t, err, "input operation requires a running pipeline")
	} else {
		require.Equal(t, beforeRun, afterRun)
	}
	executionMySQLNoVote(t, db)
}

// Local preflight checks the new lifecycle fixture contracts without claiming
// MySQL lock evidence; the required gate above remains the concurrency proof.
func TestCopilotObserveLifecycleFixtureContracts(t *testing.T) {
	for _, change := range []string{"run-cancel", "approval-cancel", "approval-expire"} {
		t.Run(change, func(t *testing.T) {
			f, _, _ := executionFixture(t)
			if change == "approval-expire" {
				expiry := time.Now().Add(20 * time.Millisecond)
				setRetryApprovalTestExpiry(t, f, expiry)
				timer := time.NewTimer(time.Until(expiry) + time.Millisecond)
				defer timer.Stop()
				<-timer.C
			}
			var job model.JobRun
			require.NoError(t, f.db.Where("job_run_id = ?", "job").First(&job).Error)
			_, descriptor, err := loadGenerationRead(f.ctx, f.db, job.CurrentGenerationID, false)
			require.NoError(t, err)
			request := retryApprovalTestRequest(t, f, descriptor, "observe")
			approvalRepo, approval := retryApprovalTestRow(t, f)
			state := "pending"
			switch change {
			case "run-cancel":
				response, e := f.jobs.ReconcileJobRetryApproval(f.ctx, request)
				require.NoError(t, e)
				require.Equal(t, "pending", response.State)
				var run model.PipelineRun
				require.NoError(t, f.db.Where("run_id = ?", "run").First(&run).Error)
				require.NoError(t, NewPipelineRepo(statusTestDB{f.db}).(*PipelineRepo).UpdateRunIfStatus(f.ctx, "run", run.Status, map[string]any{"status": model.PipelineStatusCancelled}))
			case "approval-cancel":
				_, err = approvalRepo.Cancel(f.ctx, approval.ApprovalID, "synthetic cancellation")
				require.NoError(t, err)
				state = "canceled"
			case "approval-expire":
				_, err = approvalRepo.Expire(f.ctx, approval.ApprovalID)
				require.NoError(t, err)
				state = "expired"
			}
			response, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request)
			if change == "run-cancel" {
				require.EqualError(t, err, "input operation requires a running pipeline")
			} else {
				require.NoError(t, err)
				require.Equal(t, state, response.State)
				require.False(t, response.ExecutionAllowed)
				require.Nil(t, response.Admission)
			}
			executionMySQLNoVote(t, f.db)
		})
	}
}
