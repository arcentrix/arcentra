// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func rerunInputFixture(t *testing.T) (*Repositories, *model.CopilotRerunIntent, taskqueue.JobInputIdentity, *taskqueue.SourceCheckoutExpectation) {
	t.Helper()
	r, saved, in := rerunClaimFixture(t)
	_, err := r.CopilotRerunClaim(context.Background(), in, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	db := r.database.Database()
	require.NoError(t, db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalDecision{}))
	var job model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", in.JobRunID).First(&job).Error)
	return r, saved, generationIdentity(&job), &taskqueue.SourceCheckoutExpectation{Version: 1, ScheduleID: in.ScheduleID, SealID: in.SealID, ExpectedSourceCommit: job.ExpectedSourceCommit}
}
func TestCopilotRerunInputsNilDoesNotInventObservationOrApproval(t *testing.T) {
	r, _, identity, expected := rerunInputFixture(t)
	ctx := context.Background()
	digest, err := expected.Digest(identity)
	require.NoError(t, err)
	got, observed, err := r.JobRun.(*JobRunRepo).GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, nil)
	require.NoError(t, err)
	require.Equal(t, expected, observed)
	require.Equal(t, "unknown", got.InputManifestState)
	require.Empty(t, got.InputManifest)
	require.Empty(t, got.RetryApprovalID)
	var n int64
	require.NoError(t, r.database.Database().Model(&model.ApprovalRequest{}).Count(&n).Error)
	require.Zero(t, n)
	_, err = r.JobRun.(*JobRunRepo).GetOrRecordJobInputs(ctx, identity, nil)
	require.Error(t, err)
}
func TestCopilotRerunInputsActualCheckoutPinsFreshApprovalAtomically(t *testing.T) {
	r, saved, identity, expected := rerunInputFixture(t)
	ctx := context.Background()
	digest, err := expected.Digest(identity)
	require.NoError(t, err)
	proposed, err := taskqueue.NewJobInputManifest(expected.ExpectedSourceCommit, nil, nil)
	require.NoError(t, err)
	got, _, err := r.JobRun.(*JobRunRepo).GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, proposed)
	require.NoError(t, err)
	require.Equal(t, "pinned", got.InputManifestState)
	require.NotEmpty(t, got.RetryApprovalID)
	var approval model.ApprovalRequest
	db := r.database.Database()
	require.NoError(t, db.Where("approval_id = ?", got.RetryApprovalID).First(&approval).Error)
	require.Equal(t, saved.NewRunID, approval.PipelineRunID)
	require.NotNil(t, approval.RetryJobRunID)
	require.Equal(t, saved.NewJobRunID, *approval.RetryJobRunID)
	require.Equal(t, model.ApprovalStatusPending, approval.Status)
	replay, _, err := r.JobRun.(*JobRunRepo).GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, proposed)
	require.NoError(t, err)
	require.Equal(t, got.RetryApprovalID, replay.RetryApprovalID)
	var n int64
	require.NoError(t, db.Model(&model.ApprovalDecision{}).Count(&n).Error)
	require.Zero(t, n)
}
func TestCopilotRerunInputsRejectChangedProofAndRollback(t *testing.T) {
	for _, which := range []string{"sha", "claim", "seal", "runtime", "source", "approval-write"} {
		t.Run(which, func(t *testing.T) {
			r, _, identity, expected := rerunInputFixture(t)
			ctx := context.Background()
			digest, err := expected.Digest(identity)
			require.NoError(t, err)
			proposed, err := taskqueue.NewJobInputManifest(expected.ExpectedSourceCommit, nil, nil)
			require.NoError(t, err)
			db := r.database.Database()
			switch which {
			case "sha":
				proposed.SourceCommit = strings.Repeat("f", 40)
			case "claim":
				identity.ClaimID = "00000000-0000-4000-8000-000000000001"
			case "seal":
				digest = "v1:" + strings.Repeat("a", 64)
			case "runtime":
				identity.RuntimeBindingDigest = "v1:" + strings.Repeat("a", 64)
			case "source":
				identity.SourceBindingDigest = "v1:" + strings.Repeat("a", 64)
			case "approval-write":
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("fail-approval", func(tx *gorm.DB) {
					if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "ApprovalRequest" {
						tx.AddError(ErrCopilotRerunConflict)
					}
				}))
				defer db.Callback().Create().Remove("fail-approval")
			}
			got, _, err := r.JobRun.(*JobRunRepo).GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, proposed)
			require.Error(t, err)
			require.Nil(t, got)
			var job model.JobRun
			require.NoError(t, db.Where("job_run_id = ?", identity.JobRunID).First(&job).Error)
			require.Equal(t, "unknown", job.InputManifestState)
			require.Empty(t, job.RetryApprovalID)
		})
	}
}

func TestCopilotRerunInputFinalDeadlineRollsBackObservationAndApproval(t *testing.T) {
	r, _, identity, expected := rerunInputFixture(t)
	db := r.database.Database()
	ctx := context.Background()
	var job model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", identity.JobRunID).First(&job).Error)
	require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", identity.JobRunID).Update("timeout", "1s").Error)
	const callback = "rerun-input-final-deadline"
	reachedFinalLock := false
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "CopilotRerunSchedule" {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		reachedFinalLock = true
		var staged int64
		require.NoError(t, tx.Session(&gorm.Session{NewDB: true}).Model(&model.ApprovalRequest{}).Count(&staged).Error)
		require.EqualValues(t, 1, staged)
		time.Sleep(time.Until(job.StartTime.Add(time.Second)) + 5*time.Millisecond)
	}))
	defer db.Callback().Query().Remove(callback)
	digest, err := expected.Digest(identity)
	require.NoError(t, err)
	proposed, err := taskqueue.NewJobInputManifest(expected.ExpectedSourceCommit, nil, nil)
	require.NoError(t, err)
	got, _, err := r.JobRun.(*JobRunRepo).GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, proposed)
	require.Error(t, err)
	require.Nil(t, got)
	require.True(t, reachedFinalLock)
	var after model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", identity.JobRunID).First(&after).Error)
	require.Equal(t, "unknown", after.InputManifestState)
	require.Empty(t, after.RetryApprovalID)
	var count int64
	require.NoError(t, db.Model(&model.ApprovalRequest{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestCopilotRerunInputAfterOwnerRevocationRemainsPreparationOnly(t *testing.T) {
	r, saved, identity, expected := rerunInputFixture(t)
	db := r.database.Database()
	ctx := context.Background()
	require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", saved.OwnerUserID).Update("is_enabled", 0).Error)
	digest, err := expected.Digest(identity)
	require.NoError(t, err)
	store := r.JobRun.(*JobRunRepo)
	got, _, err := store.GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, nil)
	require.NoError(t, err)
	require.Equal(t, "unknown", got.InputManifestState)
	require.Empty(t, got.RetryApprovalID)
	var approvals int64
	require.NoError(t, db.Model(&model.ApprovalRequest{}).Count(&approvals).Error)
	require.Zero(t, approvals)
	proposed, err := taskqueue.NewJobInputManifest(expected.ExpectedSourceCommit, nil, nil)
	require.NoError(t, err)
	got, _, err = store.GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, proposed)
	require.NoError(t, err)
	var approval model.ApprovalRequest
	require.NoError(t, db.Where("approval_id = ?", got.RetryApprovalID).First(&approval).Error)
	require.Equal(t, model.ApprovalStatusPending, approval.Status)
	require.Zero(t, approval.ApprovedCount)
	var decisions int64
	require.NoError(t, db.Model(&model.ApprovalDecision{}).Count(&decisions).Error)
	require.Zero(t, decisions)
	require.Empty(t, got.CurrentGenerationID)
	require.Zero(t, got.CurrentGeneration)
	// No bootstrap/startup bypass is introduced by this input-only operation.
	err = executionTransaction(ctx, db, identity.JobRunID, func(tx *gorm.DB) error {
		_, err := lockExecutionJob(ctx, tx, identity.JobRunID, executionLockActive)
		return err
	})
	require.Error(t, err)
}
