// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCopilotRerunDispatchReferenceLeaseAndReplay(t *testing.T) {
	r, saved, lease, in := rerunSealFixture(t)
	ctx := context.Background()
	db := r.database.Database()
	require.NoError(t, func() error {
		_, err := r.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
		return err
	}())
	ref, err := r.CopilotRerunDispatchReference(ctx, lease, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	require.Equal(t, saved.NewRunID, ref.RunID)
	require.Equal(t, in.SealID, ref.SealID)
	again, err := r.CopilotRerunDispatchReference(ctx, lease, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	require.Equal(t, ref, again)
	var job model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
	require.Empty(t, job.ClaimID)
	require.Empty(t, job.InputManifest)
	require.Empty(t, job.RetryApprovalID)
	require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("lease_until", time.Now().UTC().Add(-time.Second)).Error)
	takeover, err := r.CopilotRerunAcquireLease(ctx, saved.ScheduleID, "replacement", time.Minute)
	require.NoError(t, err)
	old, err := r.CopilotRerunDispatchReference(ctx, lease, permitRerun, testRerunClaimValidator)
	require.Error(t, err)
	require.Nil(t, old)
	replay, err := r.CopilotRerunDispatchReference(ctx, *takeover, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	require.Equal(t, ref, replay)
	claim := CopilotRerunClaimInput{AgentID: ref.AgentID, PipelineID: ref.PipelineID, RunID: ref.RunID, JobRunID: ref.JobRunID, GateID: ref.GateID, ScheduleID: ref.ScheduleID, SealID: ref.SealID, SealDigest: ref.SealDigest, PayloadDigest: ref.PayloadDigest, ClaimID: uuid.NewString(), AdmissionNonce: make([]byte, 32)}
	_, err = r.CopilotRerunClaim(ctx, claim, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	result, err := r.CopilotRerunDispatchReference(ctx, *takeover, permitRerun, func(context.Context, *Repositories, *CopilotRerunScope, *model.CopilotDefinitionSnapshot, *CopilotRerunOriginalEvidence, *CopilotRerunExecutionIdentities, *model.CopilotRerunIntent, *model.Agent, *model.CopilotRerunSchedule) (*CopilotRerunClaimMaterial, error) {
		t.Fatal("claimed dispatch reached validator")
		return nil, nil
	})
	require.Error(t, err)
	require.Nil(t, result)
}
func TestCopilotRerunDispatchRejectsCurrentDrift(t *testing.T) {
	for _, scenario := range []string{"owner", "policy", "config", "cancel", "agent", "cipher", "lease-expiry-after-validation"} {
		t.Run(scenario, func(t *testing.T) {
			r, saved, lease, in := rerunSealFixture(t)
			ctx := context.Background()
			db := r.database.Database()
			_, err := r.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
			require.NoError(t, err)
			authorize := permitRerun
			validate := CopilotRerunClaimValidator(testRerunClaimValidator)
			switch scenario {
			case "owner":
				authorize = func(context.Context, *Repositories, *CopilotRerunScope, string, string) error {
					return ErrCopilotRerunForbidden
				}
			case "policy":
				validate = func(context.Context, *Repositories, *CopilotRerunScope, *model.CopilotDefinitionSnapshot, *CopilotRerunOriginalEvidence, *CopilotRerunExecutionIdentities, *model.CopilotRerunIntent, *model.Agent, *model.CopilotRerunSchedule) (*CopilotRerunClaimMaterial, error) {
					return nil, errors.New("current policy changed")
				}
			case "config":
				require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", saved.PipelineID).Update("default_branch", "changed").Error)
			case "cancel":
				require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("cancel_requested", true).Error)
			case "agent":
				require.NoError(t, db.Model(&model.Agent{}).Where("agent_id = ?", in.AgentID).Update("is_enabled", 0).Error)
			case "cipher":
				require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("payload_ciphertext", []byte("altered")).Error)
			case "lease-expiry-after-validation":
				lease.Until = time.Now().Add(30 * time.Millisecond).UTC().Truncate(time.Millisecond)
				require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("lease_until", lease.Until).Error)
				validate = func(c context.Context, v *Repositories, s *CopilotRerunScope, p *model.CopilotDefinitionSnapshot, e *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, i *model.CopilotRerunIntent, a *model.Agent, q *model.CopilotRerunSchedule) (*CopilotRerunClaimMaterial, error) {
					out, err := testRerunClaimValidator(c, v, s, p, e, ids, i, a, q)
					time.Sleep(40 * time.Millisecond)
					return out, err
				}
			}
			ref, err := r.CopilotRerunDispatchReference(ctx, lease, authorize, validate)
			require.Error(t, err)
			require.Nil(t, ref)
			var job model.JobRun
			require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
			require.Empty(t, job.ClaimID)
		})
	}
}
func TestCopilotRerunDispatchScanBoundedAndAdvisory(t *testing.T) {
	r, saved, lease, _ := rerunSealFixture(t)
	ctx := context.Background()
	db := r.database.Database()
	page, err := r.CopilotRerunScanDispatch(ctx, CopilotRerunDispatchCursor{}, 32)
	require.NoError(t, err)
	require.Empty(t, page.Work)
	work, err := r.CopilotRerunReadDispatchWork(ctx, saved.ScheduleID)
	require.NoError(t, err)
	require.Equal(t, lease.Epoch+1, work.ScheduleVersion)
	require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("lease_until", time.Now().UTC().Add(-time.Second)).Error)
	page, err = r.CopilotRerunScanDispatch(ctx, CopilotRerunDispatchCursor{Bucket: 1}, 1)
	require.NoError(t, err)
	require.Len(t, page.Work, 1)
	require.Equal(t, saved.ScheduleID, page.Work[0].ScheduleID)
	next, err := r.CopilotRerunScanDispatch(ctx, page.Next, 1)
	require.NoError(t, err)
	require.Empty(t, next.Work)
	require.Equal(t, uint8(3), next.Next.Bucket)
	_, err = r.CopilotRerunScanDispatch(ctx, CopilotRerunDispatchCursor{}, 33)
	require.Error(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		v := NewRepositories(statusTestDB{tx}, nil)
		_, err := v.CopilotRerunScanDispatch(ctx, CopilotRerunDispatchCursor{}, 1)
		require.Error(t, err)
		return nil
	}))
	require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("cancel_requested", true).Error)
	page, err = r.CopilotRerunScanDispatch(ctx, CopilotRerunDispatchCursor{Bucket: 1}, 1)
	require.NoError(t, err)
	require.Empty(t, page.Work)
	require.Equal(t, 1, page.Inspected)
	require.NotZero(t, page.Next.Positions[1].ID)
}

func TestCopilotRerunDispatchCursorWrapReconsidersLeaseMoves(t *testing.T) {
	r, saved, _, _ := rerunSealFixture(t)
	db := r.database.Database()
	ctx := context.Background()
	past := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("lease_until", past).Error)
	page, err := r.CopilotRerunScanDispatch(ctx, CopilotRerunDispatchCursor{Bucket: 1}, 1)
	require.NoError(t, err)
	require.Len(t, page.Work, 1)
	// A concurrent lease change can move a row behind this observation cursor.
	// It is safely revisited after wrap; the cursor is never an admission receipt.
	require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("lease_until", past.Add(-time.Second)).Error)
	cursor := page.Next
	found := false
	for i := 0; i < 14; i++ {
		next, e := r.CopilotRerunScanDispatch(ctx, cursor, 1)
		require.NoError(t, e)
		require.LessOrEqual(t, next.Inspected, 1)
		cursor = next.Next
		for _, work := range next.Work {
			found = found || work.ScheduleID == saved.ScheduleID
		}
	}
	require.True(t, found)
	restarted, err := r.CopilotRerunScanDispatch(ctx, CopilotRerunDispatchCursor{Bucket: 1}, 1)
	require.NoError(t, err)
	require.Len(t, restarted.Work, 1)
	require.Equal(t, saved.ScheduleID, restarted.Work[0].ScheduleID)
	var job model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
	require.Empty(t, job.ClaimID)
}

func TestCopilotRerunDispatchRoundRobinUnderSustainedPendingArrivals(t *testing.T) {
	r, _, _, _ := rerunSealFixture(t)
	db := r.database.Database()
	ctx := context.Background()
	// Synthetic advisory rows only: scanning does not confer claim or execution
	// authority. Real closure/claim validation is tested separately.
	addWork := func(state string, expired bool) string {
		id, intentID, gateID, runID, jobID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
		deadline := time.Now().UTC().Add(time.Hour)
		if expired {
			deadline = time.Now().UTC().Add(-time.Hour)
		}
		claim := ""
		gateState := "unclaimed"
		if state == CopilotRerunScheduleClaimed {
			claim = uuid.NewString()
			gateState = "claimed"
		}
		require.NoError(t, db.Create(&model.CopilotRerunIntent{IntentID: intentID, ScheduleID: id, GateID: gateID, NewRunID: runID, NewJobRunID: jobID, State: CopilotRerunConfirmed, RequestID: uuid.NewString()}).Error)
		require.NoError(t, db.Create(&model.CopilotRerunAdmissionGate{GateID: gateID, ScheduleID: id, RunID: runID, JobRunID: jobID, State: gateState, ClaimID: claim}).Error)
		require.NoError(t, db.Create(&model.CopilotRerunSchedule{ScheduleID: id, IntentID: intentID, RunID: runID, JobRunID: jobID, State: state, ClaimedID: claim, ExpiresAt: deadline}).Error)
		return id
	}
	claimed := addWork(CopilotRerunScheduleClaimed, false)
	expired := addWork(CopilotRerunScheduleSealed, true)
	cursor := CopilotRerunDispatchCursor{}
	seenClaim, seenExpiry := false, false
	for turn := 0; turn < 6; turn++ {
		// Faster arrivals than the entire page budget keep pending/NULL full forever.
		for n := 0; n < 3; n++ {
			require.NoError(t, db.Create(&model.CopilotRerunSchedule{ScheduleID: uuid.NewString(), IntentID: uuid.NewString(), RunID: uuid.NewString(), JobRunID: uuid.NewString(), State: CopilotRerunSchedulePending, ExpiresAt: time.Now().UTC().Add(time.Hour)}).Error)
		}
		page, err := r.CopilotRerunScanDispatch(ctx, cursor, 2)
		require.NoError(t, err)
		require.LessOrEqual(t, page.Inspected, 2)
		require.Equal(t, uint8((turn+1)%6), page.Next.Bucket)
		cursor = page.Next
		for _, work := range page.Work {
			seenClaim = seenClaim || work.ScheduleID == claimed
			seenExpiry = seenExpiry || (work.ScheduleID == expired && !time.Now().Before(work.ExpiresAt))
		}
	}
	require.True(t, seenClaim, "full pending arrivals starved claimed observation")
	require.True(t, seenExpiry, "full pending arrivals starved sealed expiry")
	require.NotZero(t, cursor.Positions[0].ID, "pending progress must survive visits to other buckets")
}
