// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCopilotRerunClaimIncarnationRejectionPreservesExistingEvidence(t *testing.T) {
	r, saved, in := rerunClaimFixture(t)
	first, err := r.CopilotRerunClaim(context.Background(), in, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	db := r.database.Database()
	var before model.CopilotRerunAdmissionGate
	var jobBefore model.JobRun
	require.NoError(t, db.Where("gate_id = ?", saved.GateID).First(&before).Error)
	require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&jobBefore).Error)
	for _, kind := range []string{"nonce", "claim", "both"} {
		t.Run(kind, func(t *testing.T) {
			changed := in
			changed.AdmissionNonce = append([]byte(nil), in.AdmissionNonce...)
			if kind != "claim" {
				changed.AdmissionNonce[0] ^= 1
			}
			if kind != "nonce" {
				changed.ClaimID = uuid.NewString()
			}
			got, err := r.CopilotRerunClaim(context.Background(), changed, permitRerun, testRerunClaimValidator)
			require.Nil(t, got)
			require.ErrorIs(t, err, ErrCopilotRerunClaimRejected)
			require.ErrorIs(t, err, errCopilotRerunIncarnationMismatch)
			require.ErrorIs(t, err, ErrCopilotRerunConflict)
			var after model.CopilotRerunAdmissionGate
			var jobAfter model.JobRun
			require.NoError(t, db.Where("gate_id = ?", saved.GateID).First(&after).Error)
			require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&jobAfter).Error)
			require.Equal(t, before, after)
			require.Equal(t, jobBefore, jobAfter)
		})
	}
	replay, err := r.CopilotRerunClaim(context.Background(), in, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	require.Equal(t, first, replay)
	// An actual storage callback conflict is not evidence of incarnation rejection.
	const name = "synthetic-gate-read-conflict"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CopilotRerunAdmissionGate" {
			tx.AddError(ErrCopilotRerunConflict)
		}
	}))
	got, err := r.CopilotRerunClaim(context.Background(), in, permitRerun, testRerunClaimValidator)
	require.Nil(t, got)
	require.ErrorIs(t, err, ErrCopilotRerunConflict)
	require.NotErrorIs(t, err, ErrCopilotRerunClaimRejected)
	require.NotErrorIs(t, err, errCopilotRerunIncarnationMismatch)
	require.NoError(t, db.Callback().Query().Remove(name))
}
