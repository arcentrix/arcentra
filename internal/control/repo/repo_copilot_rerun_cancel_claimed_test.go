// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCopilotRerunClaimedCancellationAndLateReceipts(t *testing.T) {
	r, saved, identity, _ := rerunInputFixture(t)
	ctx := context.Background()
	db := r.database.Database()
	jobs := r.JobRun.(*JobRunRepo)
	deny := func(context.Context, *Repositories, *CopilotRerunScope, string, string) error {
		return ErrCopilotRerunForbidden
	}
	_, err := r.CopilotRerunCancelClaimed(ctx, saved.OwnerUserID, identity, deny)
	require.Error(t, err)
	var before model.CopilotRerunAdmissionGate
	require.NoError(t, db.Where("gate_id = ?", saved.GateID).First(&before).Error)
	cancelled, err := r.CopilotRerunCancelClaimed(ctx, saved.OwnerUserID, identity, permitRerun)
	require.NoError(t, err)
	require.Equal(t, model.PipelineStatusCancelled, cancelled.RunStatus)
	require.False(t, cancelled.AgentTerminalAcknowledged)
	replay, err := r.CopilotRerunCancelClaimed(ctx, saved.OwnerUserID, identity, permitRerun)
	require.NoError(t, err)
	require.Equal(t, cancelled, replay)
	var after model.CopilotRerunAdmissionGate
	require.NoError(t, db.Where("gate_id = ?", saved.GateID).First(&after).Error)
	require.Equal(t, before, after)
	var steps []model.StepRun
	require.NoError(t, db.Where("job_run_id = ?", identity.JobRunID).Find(&steps).Error)
	for i := range steps {
		step := &steps[i]
		require.Equal(t, model.JobRunStatusCancelled, step.Status)
		require.NoError(t, r.StepRun.(*StepRunRepo).CommitCancellationReceipt(ctx, step, agentidentity.StepClaimID(identity.ClaimID, step.StepRunID), strings.Repeat("b", 64)))
	}
	job, err := jobs.GetByJobRunID(ctx, identity.JobRunID)
	require.NoError(t, err)
	require.NoError(t, jobs.CommitCancellationReceipt(ctx, job, identity.ClaimID, strings.Repeat("a", 64)))
	job, err = jobs.GetByJobRunID(ctx, identity.JobRunID)
	require.NoError(t, err)
	require.NoError(t, jobs.InspectCopilotRerunJobReceipt(ctx, job))
	terminal, err := jobs.FinalizeCopilotRerun(ctx, identity)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("a", 64), terminal.TerminalReportDigest)
	require.True(t, terminal.AgentTerminalAcknowledged)
	require.Equal(t, cancelled.CompletedAtUnixMilli, terminal.CompletedAtUnixMilli)
	require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", saved.OwnerUserID).Update("is_enabled", 0).Error)
	require.NoError(t, jobs.InspectCopilotRerunJobReceipt(ctx, job))
	_, err = r.CopilotRerunCancelClaimed(ctx, saved.OwnerUserID, identity, permitRerun)
	require.Error(t, err)
	changed := *job
	changed.ClaimID = "00000000-0000-4000-8000-000000000001"
	require.Error(t, jobs.InspectCopilotRerunJobReceipt(ctx, &changed))
}

func TestCopilotRerunClaimedCancellationRollsBackAggregate(t *testing.T) {
	r, saved, identity, _ := rerunInputFixture(t)
	db := r.database.Database()
	ctx := context.Background()
	const callback = "rerun-cancel-suffix-error"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CopilotRerunSchedule" {
			tx.AddError(ErrCopilotRerunConflict)
		}
	}))
	defer db.Callback().Update().Remove(callback)
	_, err := r.CopilotRerunCancelClaimed(ctx, saved.OwnerUserID, identity, permitRerun)
	require.Error(t, err)
	var job model.JobRun
	var run model.PipelineRun
	var schedule model.CopilotRerunSchedule
	require.NoError(t, db.Where("job_run_id = ?", identity.JobRunID).First(&job).Error)
	require.Equal(t, model.JobRunStatusRunning, job.Status)
	require.Empty(t, job.CancellationClaimID)
	require.NoError(t, db.Where("run_id = ?", identity.PipelineRunID).First(&run).Error)
	require.Equal(t, model.PipelineStatusRunning, run.Status)
	require.Nil(t, run.EndTime)
	require.NoError(t, db.Where("schedule_id = ?", saved.ScheduleID).First(&schedule).Error)
	require.Equal(t, CopilotRerunScheduleClaimed, schedule.State)
	require.False(t, schedule.CancelRequested)
	var n int64
	require.NoError(t, db.Model(&model.StepRun{}).Where("job_run_id = ? AND status = ?", identity.JobRunID, model.JobRunStatusCancelled).Count(&n).Error)
	require.Zero(t, n)
}
