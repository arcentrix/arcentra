// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCopilotRerunCrossParentAttachmentPipelinePrefix(t *testing.T) {
	for _, kind := range []string{"orphan-step-move", "captured-job-step", "captured-job-move", "duplicate-job"} {
		t.Run(kind, func(t *testing.T) {
			r, saved, _ := rerunConfirmFixture(t)
			ctx, db := context.Background(), r.database.Database()
			if kind == "orphan-step-move" {
				require.NoError(t, r.StepRun.Create(ctx, &model.StepRun{StepRunID: "orphan", PipelineRunID: saved.OriginalRunID}))
			}
			var locked []string
			const hook = "cross-parent-prefix"
			require.NoError(t, db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
				if _, ok := tx.Statement.Clauses["FOR"]; ok {
					locked = append(locked, tx.Statement.Table)
				}
			}))
			defer db.Callback().Query().Remove(hook)
			var err error
			switch kind {
			case "orphan-step-move":
				err = r.StepRun.PatchByStepRunID(ctx, "orphan", map[string]any{"pipeline_run_id": saved.NewRunID})
			case "captured-job-step":
				err = r.StepRun.Create(ctx, &model.StepRun{StepRunID: "foreign-run-step", PipelineRunID: saved.NewRunID, JobRunID: saved.OriginalJobRunID})
			case "captured-job-move":
				err = r.JobRun.UpdateByJobRunID(ctx, saved.OriginalJobRunID, map[string]any{"pipeline_run_id": saved.NewRunID})
			case "duplicate-job":
				err = r.JobRun.Create(ctx, &model.JobRun{JobRunID: saved.OriginalJobRunID, PipelineRunID: saved.NewRunID})
			}
			if kind == "orphan-step-move" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.GreaterOrEqual(t, len(locked), 2)
			require.Equal(t, "pipeline", locked[0])
			require.Equal(t, "pipeline_run", locked[1])
			for _, table := range locked[1:] {
				require.NotEqual(t, "pipeline", table)
			}
		})
	}
}

func TestCopilotRerunCrossParentPipelinesAreSorted(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	require.NoError(t, db.Create(&model.Pipeline{PipelineID: "a-pipeline"}).Error)
	require.NoError(t, db.Create(&model.PipelineRun{RunID: "a-run", PipelineID: "a-pipeline", Env: "{}"}).Error)
	steps := NewStepRunRepo(statusTestDB{db})
	require.NoError(t, steps.Create(ctx, &model.StepRun{StepRunID: "move", PipelineRunID: "run"}))
	var tables, ids []string
	const hook = "sorted-cross-parent-prefix"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Clauses["FOR"]; !ok {
			return
		}
		if tx.Statement.Table == "pipeline" || tx.Statement.Table == "pipeline_run" {
			tables = append(tables, tx.Statement.Table)
			ids = append(ids, tx.Statement.Vars[0].(string))
		}
	}))
	defer db.Callback().Query().Remove(hook)
	require.NoError(t, steps.PatchByStepRunID(ctx, "move", map[string]any{"pipeline_run_id": "a-run"}))
	require.Equal(t, []string{"pipeline", "pipeline", "pipeline_run", "pipeline_run"}, tables)
	require.Equal(t, []string{"a-pipeline", "pipeline", "a-run", "run"}, ids)
}

func TestCopilotRerunAttachmentRejectsChangedParentHints(t *testing.T) {
	for _, change := range []string{"run-link", "run-appears", "job-link", "job-appears"} {
		t.Run(change, func(t *testing.T) {
			ctx, db, _, _ := sourceTestRepository(t)
			run, job := "run", "job"
			if change == "run-appears" {
				run = "missing-run"
			}
			if change == "job-appears" {
				job = "missing-job"
			}
			hint, err := locateOrdinaryAttachmentScope(ctx, db, []string{run}, job)
			require.NoError(t, err)
			switch change {
			case "run-link":
				require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", run).Update("pipeline_id", "changed").Error)
			case "run-appears":
				require.NoError(t, db.Create(&model.PipelineRun{RunID: run, PipelineID: "pipeline", Env: "{}"}).Error)
			case "job-link":
				require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", job).Update("pipeline_id", "changed").Error)
			case "job-appears":
				row := &model.JobRun{JobRunID: job, PipelineID: "pipeline", PipelineRunID: run}
				retryApprovalMySQLJSONDefaults(row)
				require.NoError(t, db.Create(row).Error)
			}
			err = rerunAttachmentTransaction(ctx, db, func(tx *gorm.DB) error {
				if err := hint.lockParents(ctx, tx); err != nil {
					return err
				}
				return hint.lockJob(ctx, tx, job)
			})
			require.ErrorIs(t, err, ErrCopilotRerunConflict)
		})
	}
}

func TestCopilotRerunCrossParentNestedMutationIsRejected(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	steps := NewStepRunRepo(statusTestDB{db})
	require.NoError(t, steps.Create(ctx, &model.StepRun{StepRunID: "nested-move", PipelineRunID: "run", JobRunID: "job"}))
	pipelineLocks := 0
	const hook = "nested-same-parent-no-late-pipeline"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if _, locked := tx.Statement.Clauses["FOR"]; locked && tx.Statement.Table == "pipeline" {
			pipelineLocks++
		}
	}))
	defer db.Callback().Query().Remove(hook)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		inner := NewStepRunRepo(statusTestDB{tx})
		require.NoError(t, inner.Create(ctx, &model.StepRun{StepRunID: "same-parent", PipelineRunID: "run", JobRunID: "job"}))
		require.ErrorIs(t, inner.PatchByStepRunID(ctx, "nested-move", map[string]any{"pipeline_run_id": "missing"}), ErrCopilotRerunUnsupported)
		require.NoError(t, inner.PatchByStepRunID(ctx, "same-parent", map[string]any{"status": model.JobRunStatusRunning}))
		jobs := NewJobRunRepo(statusTestDB{tx})
		require.NoError(t, jobs.UpdateByJobRunID(ctx, "job", map[string]any{"status": model.JobRunStatusRunning}))
		require.ErrorIs(t, jobs.UpdateByJobRunID(ctx, "job", map[string]any{"pipeline_run_id": "missing"}), ErrCopilotRerunUnsupported)
		return nil
	}))
	require.Zero(t, pipelineLocks, "single-parent nested operations cannot acquire a late Pipeline prefix")
}
