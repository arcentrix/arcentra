// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
	"time"
)

func assertExecutionReceiptEqual(t *testing.T, want, got *model.CopilotExecutionIntent) {
	t.Helper()
	require.Equal(t, want.IntentID, got.IntentID)
	require.Equal(t, want.IntentDigest, got.IntentDigest)
	require.Equal(t, want.DecisionID, got.DecisionID)
	require.Equal(t, want.State, got.State)
	require.Equal(t, want.Version, got.Version)
	require.True(t, want.CreatedAt.Equal(got.CreatedAt))
	require.True(t, want.UpdatedAt.Equal(got.UpdatedAt))
	require.True(t, want.ExpiresAt.Equal(got.ExpiresAt))
	for _, pair := range [][2]*time.Time{{want.SubmittedAt, got.SubmittedAt}, {want.CancelledAt, got.CancelledAt}} {
		if pair[0] == nil {
			require.Nil(t, pair[1])
		} else {
			require.NotNil(t, pair[1])
			require.True(t, pair[0].Equal(*pair[1]))
		}
	}
}

func TestCopilotExecutionReturnsPersistedTimestampPrecision(t *testing.T) {
	for _, state := range []string{"submitted", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			f, r, in := executionFixture(t)
			// SQLite keeps nanoseconds. Model server-side timestamp coercion after writes
			// without altering the value held by the repository or its authorization clock.
			normalize := func(tx *gorm.DB) {
				if tx.Error != nil || tx.Statement.Table != "copilot_execution_intent" {
					return
				}
				err := tx.Exec("UPDATE copilot_execution_intent SET created_at = strftime('%Y-%m-%d %H:%M:%f+00:00', created_at), updated_at = strftime('%Y-%m-%d %H:%M:%f+00:00', updated_at), submitted_at = strftime('%Y-%m-%d %H:%M:%f+00:00', submitted_at), cancelled_at = strftime('%Y-%m-%d %H:%M:%f+00:00', cancelled_at)").Error
				if err != nil {
					tx.AddError(err)
				}
			}
			require.NoError(t, f.db.Callback().Create().After("gorm:create").Register("test:receipt_precision", normalize))
			require.NoError(t, f.db.Callback().Update().After("gorm:update").Register("test:receipt_precision", normalize))
			v := executionPrepare(t, r, in)
			prepared, created, err := r.CopilotExecutionPrepare(f.ctx, in, historyTestAllow)
			require.NoError(t, err)
			require.False(t, created)
			assertExecutionReceiptEqual(t, v, prepared)
			mutation := r.CopilotExecutionConfirm
			if state == "cancelled" {
				mutation = r.CopilotExecutionCancel
			}
			result, err := mutation(f.ctx, executionMutationInput(v), historyTestAllow)
			require.NoError(t, err)
			read, err := r.CopilotExecutionGet(f.ctx, v.OwnerUserID, v.ProjectID, v.IntentID, historyTestAllow)
			require.NoError(t, err)
			assertExecutionReceiptEqual(t, result, read)
			replay, err := mutation(f.ctx, executionMutationInput(v), historyTestAllow)
			require.NoError(t, err)
			assertExecutionReceiptEqual(t, result, replay)
		})
	}
}
