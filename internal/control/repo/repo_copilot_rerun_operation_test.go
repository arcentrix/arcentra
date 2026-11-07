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

func TestCopilotRerunInputProofIsTransactionOperationAndTupleBound(t *testing.T) {
	r, _, identity, _ := rerunInputFixture(t)
	ctx := context.Background()
	db := r.database.Database()
	var old rerunOperationProof
	require.NoError(t, executionTransaction(ctx, db, identity.JobRunID, func(tx *gorm.DB) error {
		job, _, _, err := lockCopilotRerunInputOperation(ctx, tx, identity)
		require.NoError(t, err)
		require.NoError(t, requireCopilotRerunInputProof(tx, job))
		value, _ := tx.Statement.Settings.Load(rerunOperationProofKey)
		old = value.(rerunOperationProof)
		for _, field := range []string{"claim", "job", "run", "runtime", "source", "gate", "intent", "retry", "expected", "agent", "pipeline"} {
			changed := *job
			switch field {
			case "claim":
				changed.ClaimID = "other"
			case "job":
				changed.JobRunID = "other"
			case "run":
				changed.PipelineRunID = "other"
			case "runtime":
				changed.RuntimeBindingDigest = "other"
			case "source":
				changed.SourceBindingDigest = "other"
			case "gate":
				changed.CopilotRerunGateID = "other"
			case "intent":
				changed.CopilotRerunIntentID = "other"
			case "retry":
				changed.RetryPlanDigest = "other"
			case "agent":
				changed.AgentID = "other"
			case "pipeline":
				changed.PipelineID = "other"
			case "expected":
				changed.ExpectedSourceCommit = "other"
			}
			require.Error(t, requireCopilotRerunInputProof(tx, &changed), field)
		}
		ordinary, err := lockExecutionJob(ctx, tx, identity.JobRunID, executionLockActive)
		require.ErrorIs(t, err, ErrCopilotRerunUnsupported)
		require.Nil(t, ordinary)
		changed := old
		changed.locator.operation = executionLockActive
		tx.Statement.Settings.Store(rerunOperationProofKey, changed)
		require.Error(t, requireCopilotRerunInputProof(tx, job))
		tx.Statement.Settings.Store(rerunOperationProofKey, old)
		return nil
	}))
	require.Error(t, executionTransaction(ctx, db, identity.JobRunID, func(tx *gorm.DB) error {
		job, _, _, err := lockCopilotRerunInputOperation(ctx, tx, identity)
		require.NoError(t, err)
		tx.Statement.Settings.Store(rerunOperationProofKey, old)
		require.Error(t, requireCopilotRerunInputProof(tx, job))
		return nil
	}))
}

func TestCopilotRerunInputOperationRejectsUnclaimedAndPartialMarkers(t *testing.T) {
	for _, which := range []string{"claim", "gate", "intent", "run-gate"} {
		t.Run(which, func(t *testing.T) {
			r, _, identity, _ := rerunInputFixture(t)
			db := r.database.Database()
			switch which {
			case "claim":
				require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", identity.JobRunID).Update("claim_id", "").Error)
			case "gate":
				require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", identity.JobRunID).Update("copilot_rerun_gate_id", "").Error)
			case "intent":
				require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", identity.JobRunID).Update("copilot_rerun_intent_id", "").Error)
			case "run-gate":
				require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", identity.PipelineRunID).Update("copilot_rerun_gate_id", "").Error)
			}
			err := executionTransaction(context.Background(), db, identity.JobRunID, func(tx *gorm.DB) error {
				_, _, _, err := lockCopilotRerunInputOperation(context.Background(), tx, identity)
				return err
			})
			require.Error(t, err)
		})
	}
}
