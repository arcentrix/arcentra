// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCopilotRerunLeaseFencesTakeoverWithoutChangingSeal(t *testing.T) {
	r, _ := manualInboxFixture(t)
	db := r.Database()
	require.NoError(t, db.AutoMigrate(&model.CopilotRerunSchedule{}))
	sealID := uuid.NewString()
	row := model.CopilotRerunSchedule{ScheduleID: uuid.NewString(), IntentID: uuid.NewString(), OwnerUserID: "actor", ProjectID: "project", PipelineID: "pipeline", RunID: uuid.NewString(), JobRunID: uuid.NewString(), State: CopilotRerunScheduleSealed, Version: 1, SealID: &sealID, SealDigest: "immutable", AgentID: "fixed-agent", PayloadDigest: "payload", CipherVersion: 1, PayloadCiphertext: []byte("immutable sealed bytes"), CiphertextDigest: "cipher", SecretVersionsDigest: "versions", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(5 * time.Minute)}
	require.NoError(t, db.Create(&row).Error)
	repositories := NewRepositories(statusTestDB{db}, nil)
	ctx := context.Background()
	first, err := repositories.CopilotRerunAcquireLease(ctx, row.ScheduleID, "controller-one", time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 1, first.Epoch)
	_, err = repositories.CopilotRerunAcquireLease(ctx, row.ScheduleID, "controller-two", time.Second)
	require.ErrorIs(t, err, ErrCopilotRerunConflict)
	renewed, err := repositories.CopilotRerunRenewLease(ctx, *first, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, first.Epoch, renewed.Epoch)
	past := time.Now().Add(-time.Second)
	require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", row.ScheduleID).Update("lease_until", past).Error)
	second, err := repositories.CopilotRerunAcquireLease(ctx, row.ScheduleID, "controller-two", time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 2, second.Epoch)
	_, err = repositories.CopilotRerunRenewLease(ctx, *first, time.Second)
	require.ErrorIs(t, err, ErrCopilotRerunConflict)
	var stored model.CopilotRerunSchedule
	require.NoError(t, db.Where("schedule_id = ?", row.ScheduleID).First(&stored).Error)
	require.Equal(t, row.SealID, stored.SealID)
	require.Equal(t, row.SealDigest, stored.SealDigest)
	require.Equal(t, row.PayloadCiphertext, stored.PayloadCiphertext)
	require.Equal(t, row.AgentID, stored.AgentID)
	require.Equal(t, row.SecretVersionsDigest, stored.SecretVersionsDigest)
	require.Empty(t, stored.ClaimedID, "controller lease does not claim execution")
	require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", row.ScheduleID).Updates(map[string]any{"state": CopilotRerunScheduleClaimed, "claimed_id": "original-claim"}).Error)
	_, err = repositories.CopilotRerunRenewLease(ctx, *second, time.Second)
	require.ErrorIs(t, err, ErrCopilotRerunConflict)
}
func TestCopilotRerunLeaseRefusesClosedOrInvalidState(t *testing.T) {
	for _, scenario := range []string{"expired", "cancel-requested", "cancelled", "epoch-overflow", "version-overflow", "invalid-duration"} {
		t.Run(scenario, func(t *testing.T) {
			r, _ := manualInboxFixture(t)
			db := r.Database()
			require.NoError(t, db.AutoMigrate(&model.CopilotRerunSchedule{}))
			row := model.CopilotRerunSchedule{ScheduleID: uuid.NewString(), IntentID: uuid.NewString(), OwnerUserID: "actor", ProjectID: "project", PipelineID: "pipeline", RunID: uuid.NewString(), JobRunID: uuid.NewString(), State: CopilotRerunSchedulePending, Version: 1, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
			duration := time.Second
			switch scenario {
			case "expired":
				row.ExpiresAt = time.Now().Add(-time.Second)
			case "cancel-requested":
				row.CancelRequested = true
			case "cancelled":
				row.State = CopilotRerunScheduleCancelled
			case "epoch-overflow":
				row.LeaseEpoch = math.MaxInt64
			case "version-overflow":
				row.Version = math.MaxInt64
			case "invalid-duration":
				duration = 2 * time.Minute
			}
			// SQLite cannot store uint64 beyond MaxInt64; production guards are also
			// exercised with the largest signed representable values below.
			require.NoError(t, db.Create(&row).Error)
			repositories := NewRepositories(statusTestDB{db}, nil)
			result, err := repositories.CopilotRerunAcquireLease(context.Background(), row.ScheduleID, "controller", duration)
			require.Error(t, err)
			require.Nil(t, result)
		})
	}
}
