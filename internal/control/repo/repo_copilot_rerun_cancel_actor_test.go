// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCopilotRerunCancelSeparateActorPreservesOwnerAndHistory(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "sealed"}[sealed], func(t *testing.T) {
			r, saved, lease, in := rerunSealFixture(t)
			ctx, db := context.Background(), r.database.Database()
			if sealed {
				_, err := r.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
				require.NoError(t, err)
			}
			require.NoError(t, db.Create(&model.User{UserID: "cancel-actor", IsEnabled: 1}).Error)
			// The initiator's disabled status must not impersonate or disable this actor.
			require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", saved.OwnerUserID).Update("is_enabled", 0).Error)
			hint, err := r.LocateCopilotRerunCancellation(ctx, saved.PipelineID, saved.NewRunID)
			require.NoError(t, err)
			require.Equal(t, saved.OwnerUserID, hint.Unclaimed.OwnerUserID)
			auth := func(_ context.Context, _ *Repositories, scope *CopilotRerunScope, actor, action string) error {
				require.Equal(t, "cancel-actor", actor)
				require.Equal(t, "cancel", action)
				require.Equal(t, saved.NewRunID, scope.Run.RunID)
				return nil
			}
			_, err = r.CopilotRerunCancelUnclaimed(ctx, hint.Unclaimed, auth)
			require.ErrorIs(t, err, ErrCopilotRerunForbidden)
			before := *saved
			receipt, err := r.CopilotRerunCancelUnclaimedAs(ctx, "cancel-actor", hint.Unclaimed, auth)
			require.NoError(t, err)
			again, err := r.CopilotRerunCancelUnclaimedAs(ctx, "cancel-actor", hint.Unclaimed, auth)
			require.NoError(t, err)
			require.Equal(t, receipt, again)
			var after model.CopilotRerunIntent
			require.NoError(t, db.Where("intent_id = ?", saved.IntentID).First(&after).Error)
			require.Equal(t, before, after)
			recovered, err := r.LocateCopilotRerunCancellation(ctx, saved.PipelineID, saved.NewRunID)
			require.NoError(t, err)
			require.Equal(t, hint.Unclaimed, recovered.Unclaimed)
			writes := 0
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("cancel-read-create", func(*gorm.DB) { writes++ }))
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("cancel-read-update", func(*gorm.DB) { writes++ }))
			require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("cancel-read-delete", func(*gorm.DB) { writes++ }))
			defer db.Callback().Create().Remove("cancel-read-create")
			defer db.Callback().Update().Remove("cancel-read-update")
			defer db.Callback().Delete().Remove("cancel-read-delete")
			observed, err := r.InspectCopilotRerunCancellation(ctx, "cancel-actor", recovered, auth)
			require.NoError(t, err)
			require.Equal(t, receipt.CancelledAt.UnixMilli(), observed.CompletedAtUnixMilli)
			require.Empty(t, observed.ClaimID)
			require.False(t, observed.AgentTerminalAcknowledged)
			require.Zero(t, writes)
			_, err = r.InspectCopilotRerunCancellation(ctx, "cancel-actor", recovered, func(context.Context, *Repositories, *CopilotRerunScope, string, string) error {
				return ErrCopilotRerunForbidden
			})
			require.ErrorIs(t, err, ErrCopilotRerunForbidden)
		})
	}
}

func TestCopilotRerunCancellationLocatorAndReceiptAreReadOnlyAndBounded(t *testing.T) {
	r, saved, identity, _ := rerunInputFixture(t)
	ctx, db := context.Background(), r.database.Database()
	require.NoError(t, db.Create(&model.User{UserID: "cancel-actor", IsEnabled: 1}).Error)
	hint, err := r.LocateCopilotRerunCancellation(ctx, saved.PipelineID, saved.NewRunID)
	require.NoError(t, err)
	require.True(t, hint.Claimed)
	require.Equal(t, identity, hint.Identity)
	raw, err := json.Marshal(hint)
	require.NoError(t, err)
	require.Equal(t, "{}", string(raw))
	_, err = r.InspectCopilotRerunCancellation(ctx, "cancel-actor", hint, permitRerun)
	require.ErrorIs(t, err, ErrCopilotRerunConflict)
	receipt, err := r.CopilotRerunCancelClaimed(ctx, "cancel-actor", identity, permitRerun)
	require.NoError(t, err)
	writes, gateLocks := 0, 0
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("cancel-receipt-create", func(*gorm.DB) { writes++ }))
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("cancel-receipt-update", func(*gorm.DB) { writes++ }))
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("cancel-receipt-delete", func(*gorm.DB) { writes++ }))
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("cancel-receipt-gate", func(q *gorm.DB) {
		if q.Statement.Schema != nil && q.Statement.Schema.Name == "CopilotRerunAdmissionGate" {
			if _, ok := q.Statement.Clauses["FOR"]; ok {
				gateLocks++
			}
		}
	}))
	defer db.Callback().Create().Remove("cancel-receipt-create")
	defer db.Callback().Update().Remove("cancel-receipt-update")
	defer db.Callback().Delete().Remove("cancel-receipt-delete")
	defer db.Callback().Query().Remove("cancel-receipt-gate")
	current, err := r.LocateCopilotRerunCancellation(ctx, saved.PipelineID, saved.NewRunID)
	require.NoError(t, err)
	observed, err := r.InspectCopilotRerunCancellation(ctx, "cancel-actor", current, permitRerun)
	require.NoError(t, err)
	require.Equal(t, receipt, observed)
	require.Zero(t, writes)
	require.Zero(t, gateLocks)
	altered := *current
	altered.Identity.ClaimID = "00000000-0000-4000-8000-000000000001"
	_, err = r.InspectCopilotRerunCancellation(ctx, "cancel-actor", &altered, permitRerun)
	require.Error(t, err)
	_, err = r.LocateCopilotRerunCancellation(ctx, "foreign-pipeline", saved.NewRunID)
	require.ErrorIs(t, err, ErrCopilotRerunForbidden)
}

func TestCopilotRerunPreclaimRejectsClaimedBeforeDomainLocks(t *testing.T) {
	r, saved, in := rerunClaimFixture(t)
	ctx, db := context.Background(), r.database.Database()
	hint, err := r.LocateCopilotRerunCancellation(ctx, saved.PipelineID, saved.NewRunID)
	require.NoError(t, err)
	_, err = r.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	locked := []string{}
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("cancel-claimed-lock-plan", func(q *gorm.DB) {
		if q.Statement.Schema != nil {
			if _, ok := q.Statement.Clauses["FOR"]; ok {
				locked = append(locked, q.Statement.Schema.Name)
			}
		}
	}))
	defer db.Callback().Query().Remove("cancel-claimed-lock-plan")
	_, err = r.CopilotRerunCancelUnclaimedAs(ctx, saved.OwnerUserID, hint.Unclaimed, permitRerun)
	require.ErrorIs(t, err, ErrCopilotRerunConflict)
	require.Equal(t, []string{"CopilotRerunAdmissionGate"}, locked)
}

func TestCopilotRerunCancelGateFirstAndLocatorPointerFence(t *testing.T) {
	r, saved, _, _ := rerunSealFixture(t)
	ctx, db := context.Background(), r.database.Database()
	hint, err := r.LocateCopilotRerunCancellation(ctx, saved.PipelineID, saved.NewRunID)
	require.NoError(t, err)
	locked := []string{}
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("cancel-first-lock-plan", func(q *gorm.DB) {
		if q.Statement.Schema != nil {
			if _, ok := q.Statement.Clauses["FOR"]; ok {
				locked = append(locked, q.Statement.Schema.Name)
			}
		}
	}))
	defer db.Callback().Query().Remove("cancel-first-lock-plan")
	_, err = r.CopilotRerunCancelUnclaimedAs(ctx, saved.OwnerUserID, hint.Unclaimed, permitRerun)
	require.NoError(t, err)
	require.NotEmpty(t, locked)
	require.Equal(t, "CopilotRerunAdmissionGate", locked[0])
	require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", saved.NewRunID).Updates(map[string]any{"copilot_rerun_intent_id": "", "copilot_rerun_gate_id": ""}).Error)
	_, err = r.LocateCopilotRerunCancellation(ctx, saved.PipelineID, saved.NewRunID)
	require.ErrorIs(t, err, ErrCopilotRerunConflict)
}

func TestCopilotRerunTargetScopeRejectsForeignPointersBeforeAdditionalLocks(t *testing.T) {
	r, saved, _, _ := rerunSealFixture(t)
	ctx, db := context.Background(), r.database.Database()
	for _, row := range []any{&model.Project{ProjectID: "foreign-project", IsEnabled: 1, Status: model.ProjectStatusActive}, &model.Pipeline{PipelineID: "foreign-pipeline", ProjectID: "foreign-project", IsEnabled: 1}, &model.PipelineRun{RunID: "foreign-run", PipelineID: "foreign-pipeline", ProjectID: "foreign-project"}} {
		require.NoError(t, db.Create(row).Error)
	}
	var foreignLocks []string
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("cancel-foreign-pointer-lock", func(q *gorm.DB) {
		if _, locked := q.Statement.Clauses["FOR"]; !locked {
			return
		}
		for _, value := range q.Statement.Vars {
			if id, ok := value.(string); ok && (id == "foreign-project" || id == "foreign-pipeline" || id == "foreign-run") {
				foreignLocks = append(foreignLocks, id)
			}
		}
	}))
	defer db.Callback().Query().Remove("cancel-foreign-pointer-lock")
	for _, field := range []string{"project", "pipeline", "run"} {
		t.Run(field, func(t *testing.T) {
			changed := *saved
			switch field {
			case "project":
				changed.ProjectID = "foreign-project"
			case "pipeline":
				changed.PipelineID = "foreign-pipeline"
			case "run":
				changed.NewRunID = "foreign-run"
			}
			err := executionTransaction(ctx, db, saved.NewJobRunID, func(tx *gorm.DB) error {
				_, err := lockCopilotRerunTargetGateMode(ctx, tx, saved, true)
				if err != nil {
					return err
				}
				_, _, err = lockCopilotRerunTargetScopeAs(ctx, tx, &changed, saved.OwnerUserID, "cancel", permitRerun, executionLockRerunSeal)
				return err
			})
			require.Error(t, err)
			require.Empty(t, foreignLocks)
		})
	}
}
