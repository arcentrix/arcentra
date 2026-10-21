// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func rerunExpiryFixture(t *testing.T, sealed bool) (*Repositories, *model.CopilotRerunIntent, *taskqueue.RerunClaimReference) {
	return rerunExpiryFixtureWithTTL(t, sealed, time.Second)
}
func rerunExpiryFixtureWithTTL(t *testing.T, sealed bool, ttl time.Duration) (*Repositories, *model.CopilotRerunIntent, *taskqueue.RerunClaimReference) {
	t.Helper()
	ctx := context.Background()
	r, in := rerunIntentFixture(t)
	db := r.database.Database()
	require.NoError(t, db.AutoMigrate(&model.CopilotRerunAdmissionGate{}, &model.CopilotRerunSchedule{}, &model.Agent{}, &model.JobRunGeneration{}, &model.StepRunArtifact{}, &model.ProjectArtifact{}, &model.ApprovalRequest{}, &model.StepRunLog{}))
	in.ExpiresAt = time.Now().Add(ttl).UTC().Truncate(time.Millisecond)
	saved, err := testRerunPrepare(r, ctx, in, permitRerun)
	require.NoError(t, err)
	saved, err = r.CopilotRerunConfirm(ctx, CopilotRerunConfirmInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, ExpectedVersion: saved.Version, ExpectedIntentDigest: saved.IntentDigest, ExpectedReviewedRunContextDigest: saved.ReviewedRunContextDigest}, permitRerun, testRerunReview)
	require.NoError(t, err)
	if !sealed {
		return r, saved, nil
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	caps, _ := json.Marshal(taskqueue.AgentRuntimeCapabilities{ExecutionMode: taskqueue.RuntimeContainerdOfflineV1, ContainerdOfflineV1: true, ContainerdWholeJobRetryV1: true, ContainerdRetryApprovalV1: true, CopilotWholeJobRerunPreparationV1: true})
	require.NoError(t, db.Create(&model.Agent{AgentID: "sealed-agent", OS: "linux", IsEnabled: 1, Status: 1, LastHeartbeat: &now, RuntimeCapabilities: caps}).Error)
	lease, err := r.CopilotRerunAcquireLease(ctx, saved.ScheduleID, "expiry-controller", time.Minute)
	require.NoError(t, err)
	_, err = r.CopilotRerunSeal(ctx, *lease, CopilotRerunSealInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, AgentID: "sealed-agent", SealID: uuid.NewString(), ExpectedIntentDigest: saved.IntentDigest, ExpectedReviewedRunContextDigest: saved.ReviewedRunContextDigest, ExpectedIntentVersion: saved.Version, ExpectedGateVersion: 1, ExpectedScheduleVersion: 2}, permitRerun, testRerunSealBuilder)
	require.NoError(t, err)
	ref, err := r.CopilotRerunDispatchReference(ctx, *lease, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	return r, saved, ref
}

func TestCopilotRerunExpireUnclaimedExactClosure(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "sealed-unknown-enqueue"}[sealed], func(t *testing.T) {
			r, saved, ref := rerunExpiryFixture(t, sealed)
			ctx := context.Background()
			db := r.database.Database()
			var original model.JobRun
			require.NoError(t, db.Where("job_run_id = ?", saved.OriginalJobRunID).First(&original).Error)
			got, err := r.CopilotRerunExpireUnclaimed(ctx, saved.ScheduleID)
			require.Error(t, err)
			require.Nil(t, got)
			// A queue timeout is deliberately not recorded as proof; the Gate alone
			// serializes closure against any late notification's first claim.
			time.Sleep(time.Until(saved.ExpiresAt) + time.Millisecond)
			got, err = r.CopilotRerunExpireUnclaimed(ctx, saved.ScheduleID)
			require.NoError(t, err)
			require.NotNil(t, got)
			again, err := r.CopilotRerunExpireUnclaimed(ctx, saved.ScheduleID)
			require.NoError(t, err)
			require.Equal(t, got, again)
			var job model.JobRun
			require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
			require.Equal(t, model.JobRunStatusCancelled, job.Status)
			require.Equal(t, CopilotRerunExpiredBeforeClaimReason, job.ErrorMessage)
			require.Empty(t, job.ClaimID)
			var unchanged model.JobRun
			require.NoError(t, db.Where("job_run_id = ?", saved.OriginalJobRunID).First(&unchanged).Error)
			require.Equal(t, original, unchanged)
			if ref != nil {
				out, err := r.CopilotRerunClaim(ctx, expiryClaimInput(ref), permitRerun, testRerunClaimValidator)
				require.Error(t, err)
				require.Nil(t, out)
			}
		})
	}
}
func expiryClaimInput(ref *taskqueue.RerunClaimReference) CopilotRerunClaimInput {
	return CopilotRerunClaimInput{AgentID: ref.AgentID, PipelineID: ref.PipelineID, RunID: ref.RunID, JobRunID: ref.JobRunID, GateID: ref.GateID, ScheduleID: ref.ScheduleID, SealID: ref.SealID, SealDigest: ref.SealDigest, PayloadDigest: ref.PayloadDigest, ClaimID: uuid.NewString(), AdmissionNonce: make([]byte, 32)}
}
func TestCopilotRerunExpireClaimedRemainsUnknown(t *testing.T) {
	r, saved, ref := rerunExpiryFixture(t, true)
	ctx := context.Background()
	claim, err := r.CopilotRerunClaim(ctx, expiryClaimInput(ref), permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	time.Sleep(time.Until(saved.ExpiresAt) + time.Millisecond)
	out, err := r.CopilotRerunExpireUnclaimed(ctx, saved.ScheduleID)
	require.Error(t, err)
	require.Nil(t, out)
	var job model.JobRun
	require.NoError(t, r.database.Database().Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
	require.Equal(t, claim.Receipt.ClaimID, job.ClaimID)
	require.Nil(t, job.EndTime)
}

func TestCopilotRerunExpireRejectsDurableEvidenceAndRollsBack(t *testing.T) {
	for _, kind := range []string{"job-artifacts", "step-admission", "generation-row", "artifact-row", "canonical-artifact-row", "canonical-artifact-wrong-run", "approval-row", "log-row", "schedule-write"} {
		t.Run(kind, func(t *testing.T) {
			r, saved, _ := rerunExpiryFixture(t, false)
			db := r.database.Database()
			ctx := context.Background()
			var step model.StepRun
			require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&step).Error)
			switch kind {
			case "job-artifacts":
				require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", saved.NewJobRunID).Update("artifact_references", `{"unexpected":true}`).Error)
			case "step-admission":
				require.NoError(t, db.Model(&model.StepRun{}).Where("step_run_id = ?", step.StepRunID).Update("attempt_admission", "durable-receipt").Error)
			case "generation-row":
				require.NoError(t, db.Create(&model.JobRunGeneration{GenerationID: uuid.NewString(), JobRunID: saved.NewJobRunID}).Error)
			case "artifact-row":
				require.NoError(t, db.Create(&model.StepRunArtifact{ArtifactID: uuid.NewString(), JobRunID: saved.NewJobRunID, StepRunID: step.StepRunID}).Error)
			case "canonical-artifact-row", "canonical-artifact-wrong-run":
				runID := saved.NewRunID
				if kind == "canonical-artifact-wrong-run" {
					runID = "different-run"
				}
				require.NoError(t, db.Create(&model.ProjectArtifact{ArtifactID: uuid.NewString(), PipelineRunID: runID, JobRunID: saved.NewJobRunID, StepRunID: step.StepRunID}).Error)
			case "approval-row":
				require.NoError(t, db.Create(&model.ApprovalRequest{ApprovalID: uuid.NewString(), RetryJobRunID: &saved.NewJobRunID}).Error)
			case "log-row":
				require.NoError(t, db.Create(&model.StepRunLog{LogID: uuid.NewString(), StepRunID: step.StepRunID}).Error)
			case "schedule-write":
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:expiry-write", func(tx *gorm.DB) {
					if tx.Statement.Table == "copilot_rerun_schedule" {
						tx.AddError(errors.New("synthetic lost write"))
					}
				}))
				defer db.Callback().Update().Remove("test:expiry-write")
			}
			time.Sleep(time.Until(saved.ExpiresAt) + time.Millisecond)
			out, err := r.CopilotRerunExpireUnclaimed(ctx, saved.ScheduleID)
			require.Error(t, err)
			require.Nil(t, out)
			var job model.JobRun
			require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
			require.Equal(t, model.JobRunStatusPending, job.Status)
			require.Nil(t, job.EndTime)
			var gate model.CopilotRerunAdmissionGate
			require.NoError(t, db.Where("gate_id = ?", saved.GateID).First(&gate).Error)
			require.Equal(t, "unclaimed", gate.State)
			require.Nil(t, gate.ClosedAt)
			var steps []model.StepRun
			require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).Find(&steps).Error)
			for _, row := range steps {
				require.Equal(t, model.JobRunStatusPending, row.Status)
				require.Nil(t, row.EndTime)
			}
		})
	}
}
