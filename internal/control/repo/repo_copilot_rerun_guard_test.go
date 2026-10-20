// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestCopilotRerunInertTargetsRejectGenericMutations(t *testing.T) {
	ctx, db, jobs, binding := sourceTestRepository(t)
	require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Updates(map[string]any{"copilot_rerun_intent_id": "intent", "copilot_rerun_gate_id": "gate"}).Error)
	require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"copilot_rerun_intent_id": "intent", "copilot_rerun_gate_id": "gate", "expected_source_commit": "expected"}).Error)
	var original model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", "job").First(&original).Error)
	for _, mode := range []executionLockMode{executionLockActive, executionLockLegacy, executionLockInspect, executionLockTerminalReceipt} {
		err := executionTransaction(ctx, db, "job", func(tx *gorm.DB) error { _, err := lockExecutionJob(ctx, tx, "job", mode); return err })
		require.Error(t, err)
	}
	require.Error(t, jobs.CaptureSourceExpectation(ctx, "pipeline", "run", "job", "agent", binding))
	require.Error(t, jobs.UpdateByJobRunID(ctx, "job", map[string]any{"status": model.JobRunStatusRunning}))
	require.Error(t, jobs.PatchStatusIfCurrent(ctx, "job", original.Status, map[string]any{"status": model.JobRunStatusRunning}))
	require.Error(t, jobs.PatchReportedStatus(ctx, &original, map[string]any{"status": model.JobRunStatusRunning, "claim_id": "claim"}))
	require.Error(t, jobs.CancelAgentJobIfCurrent(ctx, "job", original.Status, map[string]any{"status": model.JobRunStatusCancelled}))
	require.Error(t, jobs.Create(ctx, &model.JobRun{JobRunID: "extra", PipelineID: "pipeline", PipelineRunID: "run"}))
	ordinary := &model.JobRun{JobRunID: "ordinary", PipelineID: "pipeline"}
	require.NoError(t, jobs.Create(ctx, ordinary))
	require.Error(t, jobs.UpdateByJobRunID(ctx, "ordinary", map[string]any{"pipeline_run_id": "run"}))
	steps := &StepRunRepo{IDatabase: statusTestDB{db}}
	require.NoError(t, db.Create(&model.StepRun{StepRunID: "protected-step", PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", Status: model.JobRunStatusPending, Args: "{}", Env: "{}", Secrets: "{}", LabelSelector: "{}", RetryPolicy: "{}"}).Error)
	require.Error(t, steps.Create(ctx, &model.StepRun{StepRunID: "extra-step", JobRunID: "job"}))
	require.Error(t, steps.Create(ctx, &model.StepRun{StepRunID: "standalone", PipelineRunID: "run"}))
	require.NoError(t, steps.Create(ctx, &model.StepRun{StepRunID: "legacy-step", Status: 1}))
	for _, key := range []string{"pipeline_run_id", "PipelineRunID"} {
		require.Error(t, steps.PatchByStepRunID(ctx, "legacy-step", map[string]any{key: "run"}))
		require.Error(t, steps.PatchStatusIfCurrent(ctx, "legacy-step", 1, map[string]any{key: "run"}))
		require.Error(t, steps.PatchReportedStatus(ctx, &model.StepRun{StepRunID: "legacy-step", Status: 1}, map[string]any{key: "run"}))
	}
	require.Error(t, steps.PatchByStepRunID(ctx, "protected-step", map[string]any{"status": model.JobRunStatusRunning}))
	require.Error(t, steps.PatchStatusIfCurrent(ctx, "protected-step", model.JobRunStatusPending, map[string]any{"status": model.JobRunStatusRunning}))
	require.Error(t, steps.DeleteByStepRunID(ctx, "protected-step"))
	var step model.StepRun
	require.NoError(t, db.Where("step_run_id = ?", "protected-step").First(&step).Error)
	require.Error(t, steps.PatchReportedStatus(ctx, &step, map[string]any{"status": model.JobRunStatusRunning, "claim_id": "claim"}))
	// Even preexisting/corrupt standalone attachments cannot escape by moving
	// to an ordinary run or blanking the current parent identity.
	attached := &model.StepRun{StepRunID: "attached", PipelineRunID: "run", Status: 1, Args: "{}", Env: "{}", Secrets: "{}", LabelSelector: "{}", RetryPolicy: "{}"}
	require.NoError(t, db.Create(attached).Error)
	for _, key := range []string{"pipeline_run_id", "PipelineRunID", "PIPELINE_RUN_ID"} {
		require.Error(t, steps.PatchByStepRunID(ctx, "attached", map[string]any{key: ""}))
		require.Error(t, steps.PatchStatusIfCurrent(ctx, "attached", 1, map[string]any{key: ""}))
	}
	require.Error(t, steps.PatchReportedStatus(ctx, attached, map[string]any{"status": 3, "claim_id": "claim"}))
	require.Error(t, steps.DeleteByStepRunID(ctx, "attached"))
	missingParent := *attached
	missingParent.ID = 0
	missingParent.StepRunID = "missing-parent"
	missingParent.JobRunID = "absent-job"
	require.NoError(t, db.Create(&missingParent).Error)
	require.Error(t, steps.PatchReportedStatus(ctx, &missingParent, map[string]any{"status": 3, "claim_id": "claim"}))
	require.Error(t, steps.DeleteByStepRunID(ctx, missingParent.StepRunID))

	var selected []model.StepRun
	require.NoError(t, ExcludeRuntimeSteps(db.Model(&model.StepRun{})).Find(&selected).Error)
	require.Len(t, selected, 1)
	require.Equal(t, "legacy-step", selected[0].StepRunID)
	pipeline := &PipelineRepo{IDatabase: statusTestDB{db}}
	var run model.PipelineRun
	require.NoError(t, db.Where("run_id = ?", "run").First(&run).Error)
	admitted, err := pipeline.StartManualRunIfPending(ctx, &run)
	require.NoError(t, err)
	require.False(t, admitted)
	require.Error(t, pipeline.UpdateRun(ctx, "run", map[string]any{"status": model.PipelineStatusRunning}))
	require.Error(t, pipeline.UpdateRunIfStatus(ctx, "run", run.Status, map[string]any{"status": model.PipelineStatusRunning}))
	summary, err := RecoverInterruptedRuns(ctx, db, []string{"run"}, true)
	require.NoError(t, err)
	require.Zero(t, summary.Runs)
	var after model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", "job").First(&after).Error)
	require.Equal(t, original, after)
	var runAfter model.PipelineRun
	require.NoError(t, db.Where("run_id = ?", "run").First(&runAfter).Error)
	require.Equal(t, run, runAfter)
}

func TestCopilotRerunProtectedFieldsCannotBeForgedOrCleared(t *testing.T) {
	ctx, db, jobs, _ := sourceTestRepository(t)
	pipeline := &PipelineRepo{IDatabase: statusTestDB{db}}
	for _, field := range []string{"copilot_rerun_intent_id", "CopilotRerunIntentID", "copilot_rerun_gate_id", "CopilotRerunGateID", "expected_source_commit", "ExpectedSourceCommit"} {
		require.Error(t, jobs.UpdateByJobRunID(ctx, "job", map[string]any{field: ""}))
		require.Error(t, jobs.PatchReportedStatus(ctx, &model.JobRun{JobRunID: "job", Status: 1}, map[string]any{field: ""}))
		require.Error(t, pipeline.UpdateRun(ctx, "run", map[string]any{field: ""}))
		require.Error(t, pipeline.UpdateRunIfStatus(ctx, "run", 1, map[string]any{field: ""}))
	}
	require.Error(t, jobs.Create(context.Background(), &model.JobRun{JobRunID: "forged", CopilotRerunIntentID: "intent"}))
	require.Error(t, pipeline.CreateRun(ctx, &model.PipelineRun{RunID: "forged", CopilotRerunIntentID: "intent"}))
}

func TestCopilotRerunAttachmentLocksParentInWriteTransaction(t *testing.T) {
	for _, kind := range []string{"job", "step"} {
		t.Run(kind, func(t *testing.T) {
			ctx, db, jobs, _ := sourceTestRepository(t)
			steps := &StepRunRepo{IDatabase: statusTestDB{db}}
			require.NoError(t, steps.Create(ctx, &model.StepRun{StepRunID: "movable", PipelineRunID: "run", JobRunID: "job", Status: 1}))
			locked := false
			var order []string
			var pool gorm.ConnPool
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("assert-parent-current-lock", func(q *gorm.DB) {
				if !q.DryRun {
					if _, ok := q.Statement.Clauses["FOR"]; ok {
						order = append(order, q.Statement.Table)
					}
				}
				if q.DryRun || q.Statement.Table != "pipeline_run" {
					return
				}
				_, ok := q.Statement.Clauses["FOR"]
				require.True(t, ok, "destination must not be a standalone pre-read")
				_, ok = q.Statement.ConnPool.(gorm.TxCommitter)
				require.True(t, ok)
				locked = true
				pool = q.Statement.ConnPool
			}))
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("assert-parent-held-during-write", func(q *gorm.DB) {
				require.True(t, locked)
				require.Equal(t, pool, q.Statement.ConnPool)
			}))
			var err error
			if kind == "job" {
				err = jobs.UpdateByJobRunID(ctx, "job", map[string]any{"pipeline_run_id": "future-run"})
			} else {
				err = steps.PatchByStepRunID(ctx, "movable", map[string]any{"pipeline_run_id": "future-run"})
			}
			require.NoError(t, err, "legacy missing parent stays allowed under its write-transaction key lock")
			require.True(t, locked)
			if kind == "step" {
				require.GreaterOrEqual(t, len(order), 3)
				require.Equal(t, []string{"pipeline_run", "job_run", "step_run"}, order[len(order)-3:])
			}
		})
	}
}

func TestCopilotRerunStepParentHintCannotAuthorizeLaterProtectedParent(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	steps := &StepRunRepo{IDatabase: statusTestDB{db}}
	require.NoError(t, steps.Create(ctx, &model.StepRun{StepRunID: "movable", PipelineRunID: "future-run", Status: 1}))
	inserted := false
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("parent-appears-after-step-hint", func(q *gorm.DB) {
		if inserted || q.Statement.Table != "step_run" {
			return
		}
		if _, inside := q.Statement.ConnPool.(gorm.TxCommitter); inside {
			return
		}
		inserted = true
		require.NoError(t, db.Create(&model.PipelineRun{RunID: "future-run", CopilotRerunIntentID: "intent", CopilotRerunGateID: "gate", Env: "{}"}).Error)
	}))
	err := steps.PatchByStepRunID(ctx, "movable", map[string]any{"status": 3})
	require.ErrorIs(t, err, ErrCopilotRerunUnsupported)
	require.True(t, inserted)
	var step model.StepRun
	require.NoError(t, db.Where("step_run_id = ?", "movable").First(&step).Error)
	require.Equal(t, 1, step.Status)
}

func TestCopilotRerunParentUpdateRejectsDuplicateAliases(t *testing.T) {
	for _, patch := range []map[string]any{
		{"pipeline_run_id": "", "PipelineRunID": "other"},
		{"pipeline_run_id": "same", "PIPELINE_RUN_ID": "same"},
	} {
		_, err := rerunUpdateRunDestination(patch)
		require.Error(t, err)
	}
}
