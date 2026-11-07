// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCopilotRerunStepProofIsExactAndOrdinaryMutationStaysDenied(t *testing.T) {
	r, _, identity, _ := rerunInputFixture(t)
	ctx := context.Background()
	db := r.database.Database()
	var step model.StepRun
	require.NoError(t, db.Where("job_run_id = ?", identity.JobRunID).First(&step).Error)
	claim := agentidentity.StepClaimID(identity.ClaimID, step.StepRunID)
	require.NoError(t, executionTransaction(ctx, db, identity.JobRunID, func(tx *gorm.DB) error {
		_, err := lockCopilotRerunStepOperation(ctx, tx, &step, claim, model.JobRunStatusRunning)
		require.NoError(t, err)
		_, err = lockCopilotRerunStepOperation(ctx, tx, &step, claim, model.JobRunStatusRunning)
		require.NoError(t, err)
		changed := step
		changed.StepRunID = "foreign-step"
		_, err = lockCopilotRerunStepOperation(ctx, tx, &changed, claim, model.JobRunStatusRunning)
		require.Error(t, err)
		_, err = lockCopilotRerunStepOperation(ctx, tx, &step, claim, model.JobRunStatusFailed)
		require.Error(t, err)
		foreign := tx.Session(&gorm.Session{NewDB: true}).WithContext(ctx)
		foreign.Statement.ConnPool = db.Statement.ConnPool
		_, err = lockCopilotRerunStepOperation(ctx, foreign, &step, claim, model.JobRunStatusRunning)
		require.Error(t, err)
		_, err = lockExecutionJob(ctx, tx, identity.JobRunID, executionLockActive)
		require.Error(t, err)
		return nil
	}))
}

func TestCopilotRerunTerminalReportPreservesClaimClockAndFirstReceipt(t *testing.T) {
	r, _, identity, ctx, artifact := rerunOutputFixture(t)
	jobs := r.JobRun.(*JobRunRepo)
	db := r.database.Database()
	job, err := jobs.GetByJobRunID(ctx, identity.JobRunID)
	require.NoError(t, err)
	claimStart := *job.StartTime
	// The actual protocol truncates Agent timestamps to seconds, which may
	// precede the controller claim within that same second.
	reported := time.Unix(claimStart.Unix(), 0)
	updates := map[string]any{"status": model.JobRunStatusSuccess, "claim_id": identity.ClaimID, "terminal_report_digest": strings.Repeat("a", 64), "start_time": reported, "end_time": reported, "duration": int64(0), "source_resolved_commit": job.ExpectedSourceCommit, "source_provenance_state": "reported"}
	before := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, jobs.PatchReportedStatusAndArtifacts(ctx, job, updates, []*model.ProjectArtifact{artifact}))
	current, err := jobs.GetByJobRunID(ctx, identity.JobRunID)
	require.NoError(t, err)
	require.True(t, current.StartTime.Equal(claimStart))
	require.False(t, current.EndTime.Before(before))
	require.Equal(t, current.EndTime.Sub(claimStart).Milliseconds(), current.Duration)
	firstEnd, firstDuration := *current.EndTime, current.Duration
	time.Sleep(3 * time.Millisecond)
	require.NoError(t, jobs.InspectCopilotRerunJobReceipt(ctx, current))
	require.NoError(t, jobs.ValidateCompletedArtifacts(ctx, current.JobRunID, []*model.ProjectArtifact{artifact}))
	changed := *current
	changed.TerminalReportDigest = strings.Repeat("b", 64)
	require.Error(t, jobs.InspectCopilotRerunJobReceipt(ctx, &changed))
	require.Error(t, jobs.PatchReportedStatusAndArtifacts(ctx, job, updates, []*model.ProjectArtifact{artifact}))
	require.NoError(t, db.Where("job_run_id = ?", job.JobRunID).First(current).Error)
	require.True(t, current.EndTime.Equal(firstEnd))
	require.Equal(t, firstDuration, current.Duration)
	receipt, err := jobs.FinalizeCopilotRerun(ctx, identity)
	require.NoError(t, err)
	require.Equal(t, firstEnd.UnixMilli(), receipt.CompletedAtUnixMilli)
}

func TestCopilotRerunTerminalClockRejectsBackwardServerTime(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Millisecond)
	updates := map[string]any{"start_time": start, "end_time": start, "duration": int64(0)}
	require.Error(t, normalizeCopilotRerunTerminalClock(updates, &start, start.Add(-time.Millisecond)))
	require.Equal(t, start, updates["start_time"])
	require.Equal(t, start, updates["end_time"])
	require.Error(t, normalizeCopilotRerunTerminalClock(updates, nil, start))
}
