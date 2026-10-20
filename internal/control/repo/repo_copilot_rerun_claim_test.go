// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func rerunClaimFixture(t *testing.T) (*Repositories, *model.CopilotRerunIntent, CopilotRerunClaimInput) {
	t.Helper()
	r, saved, lease, in := rerunSealFixture(t)
	sealed, err := r.CopilotRerunSeal(context.Background(), lease, in, permitRerun, testRerunSealBuilder)
	require.NoError(t, err)
	return r, saved, CopilotRerunClaimInput{AgentID: in.AgentID, PipelineID: saved.PipelineID, RunID: saved.NewRunID, JobRunID: saved.NewJobRunID, GateID: saved.GateID, ScheduleID: saved.ScheduleID, SealID: in.SealID, SealDigest: func() string { d, _ := sealed.Binding.Digest(); return d }(), PayloadDigest: sealed.Binding.PayloadDigest, ClaimID: uuid.NewString(), AdmissionNonce: []byte(strings.Repeat("synthetic-nonce!", 2))}
}
func testRerunClaimValidator(ctx context.Context, r *Repositories, scope *CopilotRerunScope, snapshot *model.CopilotDefinitionSnapshot, evidence *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, intent *model.CopilotRerunIntent, agent *model.Agent, schedule *model.CopilotRerunSchedule) (*CopilotRerunClaimMaterial, error) {
	seal, err := testRerunSealBuilder(ctx, r, scope, snapshot, evidence, ids, intent, agent, *schedule.SealID)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(seal.Payload)
	return &CopilotRerunClaimMaterial{Reviewed: seal.Reviewed, PayloadJSON: body}, err
}
func TestCopilotRerunClaimAtomicPreparationAndSameIncarnationReplay(t *testing.T) {
	r, saved, in := rerunClaimFixture(t)
	ctx := context.Background()
	db := r.database.Database()
	require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Updates(map[string]any{"lease_owner": "another-controller", "lease_epoch": 10, "lease_until": time.Now().Add(-time.Hour)}).Error)
	first, err := r.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	second, err := r.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	require.Equal(t, first, second)
	inspected, err := r.CopilotRerunInspectClaim(ctx, saved.OwnerUserID, saved.ProjectID, saved.IntentID, permitRerun)
	require.NoError(t, err)
	require.Equal(t, first.Receipt, inspected)
	var gate model.CopilotRerunAdmissionGate
	var job model.JobRun
	var schedule model.CopilotRerunSchedule
	require.NoError(t, db.Where("gate_id = ?", saved.GateID).First(&gate).Error)
	require.True(t, rerunClaimNonceMatches(gate.AdmissionNonceDigest, in))
	require.NotContains(t, gate.AdmissionNonceDigest, "synthetic")
	require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
	require.Equal(t, in.ClaimID, job.ClaimID)
	require.Equal(t, model.JobRunStatusRunning, job.Status)
	require.Empty(t, job.InputManifest)
	require.Empty(t, job.RetryApprovalID)
	require.Empty(t, job.CurrentGenerationID)
	require.NoError(t, db.Where("schedule_id = ?", saved.ScheduleID).First(&schedule).Error)
	require.Equal(t, CopilotRerunScheduleClaimed, schedule.State)
	require.Equal(t, uint64(10), schedule.LeaseEpoch)
	require.Error(t, executionTransaction(ctx, db, in.JobRunID, func(tx *gorm.DB) error { _, e := lockExecutionJob(ctx, tx, in.JobRunID, executionLockActive); return e }))
	restart := in
	restart.AdmissionNonce = append([]byte(nil), in.AdmissionNonce...)
	restart.AdmissionNonce[0] ^= 1
	_, err = r.CopilotRerunClaim(ctx, restart, permitRerun, testRerunClaimValidator)
	require.ErrorIs(t, err, ErrCopilotRerunConflict)
	changed := in
	changed.ClaimID = uuid.NewString()
	_, err = r.CopilotRerunClaim(ctx, changed, permitRerun, testRerunClaimValidator)
	require.ErrorIs(t, err, ErrCopilotRerunConflict)
	deny := func(context.Context, *Repositories, *CopilotRerunScope, string, string) error {
		return ErrCopilotRerunForbidden
	}
	got, err := r.CopilotRerunClaim(ctx, in, deny, testRerunClaimValidator)
	require.ErrorIs(t, err, ErrCopilotRerunForbidden)
	require.Nil(t, got)
}
func TestCopilotRerunClaimRollbackAndUnknownCommit(t *testing.T) {
	for _, scenario := range []string{"rollback", "lost-ack"} {
		t.Run(scenario, func(t *testing.T) {
			r, saved, in := rerunClaimFixture(t)
			db := r.database.Database()
			ctx := context.Background()
			if scenario == "rollback" {
				const name = "claim-schedule-write-fails"
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CopilotRerunSchedule" {
						tx.AddError(errors.New("synthetic"))
					}
				}))
				got, err := r.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
				require.Error(t, err)
				require.Nil(t, got)
				require.NoError(t, db.Callback().Update().Remove(name))
				var gate model.CopilotRerunAdmissionGate
				var job model.JobRun
				require.NoError(t, db.Where("gate_id = ?", saved.GateID).First(&gate).Error)
				require.Empty(t, gate.ClaimID)
				require.Empty(t, gate.AdmissionNonceDigest)
				require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
				require.Empty(t, job.ClaimID)
				require.Equal(t, model.JobRunStatusPending, job.Status)
			} else {
				pool, err := db.DB()
				require.NoError(t, err)
				lose := &atomic.Bool{}
				lose.Store(true)
				wrapped := db.Session(&gorm.Session{NewDB: true, Initialized: true})
				wrapped.Statement.ConnPool = historyLostCommitPool{ConnPool: pool, pool: pool, lose: lose}
				uncertain := &Repositories{database: statusTestDB{wrapped}}
				got, err := uncertain.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
				require.Nil(t, got)
				require.ErrorIs(t, err, ErrCopilotRerunUnavailable)
				inspected, inspectErr := r.CopilotRerunInspectClaim(ctx, saved.OwnerUserID, saved.ProjectID, saved.IntentID, permitRerun)
				require.NoError(t, inspectErr)
				require.Equal(t, in.ClaimID, inspected.ClaimID)
				got, err = r.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
				require.NoError(t, err)
				require.Equal(t, in.ClaimID, got.Receipt.ClaimID)
			}
		})
	}
}

func TestCopilotRerunClaimRejectsCurrentStateAndPolicyDrift(t *testing.T) {
	for _, kind := range []string{"cancelled", "disabled-agent", "disabled-owner", "changed-policy", "changed-payload", "wrong-agent", "uppercase-id"} {
		t.Run(kind, func(t *testing.T) {
			r, saved, in := rerunClaimFixture(t)
			db := r.database.Database()
			ctx := context.Background()
			validate := CopilotRerunClaimValidator(testRerunClaimValidator)
			switch kind {
			case "cancelled":
				_, err := r.CopilotRerunCancelUnclaimed(ctx, CopilotRerunTargetCancelInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, ExpectedIntentDigest: saved.IntentDigest, ExpectedIntentVersion: saved.Version, ExpectedGateVersion: 2, ExpectedScheduleVersion: 3}, permitRerun)
				require.NoError(t, err)
			case "disabled-agent":
				require.NoError(t, db.Model(&model.Agent{}).Where("agent_id = ?", in.AgentID).Update("is_enabled", 0).Error)
			case "disabled-owner":
				require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", saved.OwnerUserID).Update("is_enabled", 0).Error)
			case "wrong-agent":
				in.AgentID = "other"
			case "uppercase-id":
				in.RunID = strings.ToUpper(in.RunID)
			default:
				validate = func(ctx context.Context, r *Repositories, scope *CopilotRerunScope, snapshot *model.CopilotDefinitionSnapshot, evidence *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, intent *model.CopilotRerunIntent, agent *model.Agent, schedule *model.CopilotRerunSchedule) (*CopilotRerunClaimMaterial, error) {
					v, e := testRerunClaimValidator(ctx, r, scope, snapshot, evidence, ids, intent, agent, schedule)
					if e != nil {
						return nil, e
					}
					if kind == "changed-policy" {
						v.Reviewed.AdmissionPolicyDigest = "changed"
					} else {
						v.PayloadJSON = append(v.PayloadJSON, ' ')
					}
					return v, nil
				}
			}
			got, err := r.CopilotRerunClaim(ctx, in, permitRerun, validate)
			require.Error(t, err)
			require.Nil(t, got)
			var job model.JobRun
			require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
			require.Empty(t, job.ClaimID)
			require.Empty(t, job.InputManifest)
		})
	}
}

func TestCopilotRerunClaimRechecksExpiryAfterMaterialization(t *testing.T) {
	r, saved, lease, sealIn := rerunSealFixture(t)
	ctx := context.Background()
	db := r.database.Database()
	saved.ExpiresAt = time.Now().UTC().Truncate(time.Millisecond).Add(500 * time.Millisecond)
	saved.IntentDigest = rerunIntentDigest(saved)
	sealIn.ExpectedIntentDigest = saved.IntentDigest
	require.NoError(t, db.Model(&model.CopilotRerunIntent{}).Where("intent_id = ?", saved.IntentID).Updates(map[string]any{"expires_at": saved.ExpiresAt, "intent_digest": saved.IntentDigest}).Error)
	require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("expires_at", saved.ExpiresAt).Error)
	sealed, err := r.CopilotRerunSeal(ctx, lease, sealIn, permitRerun, testRerunSealBuilder)
	require.NoError(t, err)
	digest, err := sealed.Binding.Digest()
	require.NoError(t, err)
	in := CopilotRerunClaimInput{AgentID: sealIn.AgentID, PipelineID: saved.PipelineID, RunID: saved.NewRunID, JobRunID: saved.NewJobRunID, GateID: saved.GateID, ScheduleID: saved.ScheduleID, SealID: sealIn.SealID, SealDigest: digest, PayloadDigest: sealed.Binding.PayloadDigest, ClaimID: uuid.NewString(), AdmissionNonce: []byte(strings.Repeat("synthetic-nonce!", 2))}
	slow := func(ctx context.Context, r *Repositories, scope *CopilotRerunScope, snapshot *model.CopilotDefinitionSnapshot, evidence *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, intent *model.CopilotRerunIntent, agent *model.Agent, schedule *model.CopilotRerunSchedule) (*CopilotRerunClaimMaterial, error) {
		v, e := testRerunClaimValidator(ctx, r, scope, snapshot, evidence, ids, intent, agent, schedule)
		time.Sleep(time.Until(saved.ExpiresAt) + 5*time.Millisecond)
		return v, e
	}
	got, err := r.CopilotRerunClaim(ctx, in, permitRerun, slow)
	require.ErrorIs(t, err, ErrCopilotRerunExpired)
	require.Nil(t, got)
	var gate model.CopilotRerunAdmissionGate
	require.NoError(t, db.Where("gate_id = ?", saved.GateID).First(&gate).Error)
	require.Empty(t, gate.ClaimID)
	require.Empty(t, gate.AdmissionNonceDigest)
}

func TestCopilotRerunClaimProofCannotAuthorizeSeal(t *testing.T) {
	r, saved, _ := rerunClaimFixture(t)
	ctx := context.Background()
	db := r.database.Database()
	require.NoError(t, executionTransaction(ctx, db, saved.NewJobRunID, func(tx *gorm.DB) error {
		_, err := lockCopilotRerunGateForOperation(ctx, tx, saved, false, executionLockRerunClaim)
		require.NoError(t, err)
		_, err = lockExecutionJob(ctx, tx, saved.NewJobRunID, executionLockRerunSeal)
		require.ErrorIs(t, err, ErrCopilotRerunUnsupported)
		_, err = lockExecutionJob(ctx, tx, saved.NewJobRunID, executionLockRerunClaim)
		return err
	}))
}

func TestCopilotRerunClaimRetainsSealHistoricalReceipt(t *testing.T) {
	r, saved, in := rerunClaimFixture(t)
	ctx := context.Background()
	db := r.database.Database()
	_, err := r.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	sealIn := CopilotRerunSealInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, AgentID: in.AgentID, SealID: in.SealID, ExpectedIntentDigest: saved.IntentDigest, ExpectedReviewedRunContextDigest: saved.ReviewedRunContextDigest, ExpectedIntentVersion: saved.Version, ExpectedGateVersion: 1, ExpectedScheduleVersion: 2}
	for _, terminal := range []bool{false, true} {
		if terminal {
			now := time.Now().UTC().Truncate(time.Millisecond)
			require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", in.JobRunID).Updates(map[string]any{"status": model.JobRunStatusFailed, "end_time": now}).Error)
			require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", in.RunID).Updates(map[string]any{"status": model.PipelineStatusFailed, "end_time": now}).Error)
			require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", in.ScheduleID).Updates(map[string]any{"state": CopilotRerunScheduleCompleted, "completed_at": now}).Error)
		}
		observed, err := r.CopilotRerunInspectSeal(ctx, saved.OwnerUserID, saved.ProjectID, saved.IntentID, permitRerun)
		require.NoError(t, err)
		require.Equal(t, in.SealID, observed.Binding.SealID)
		require.Equal(t, in.PayloadDigest, observed.Binding.PayloadDigest)
		replay, err := r.CopilotRerunSeal(ctx, CopilotRerunLeaseToken{ScheduleID: in.ScheduleID}, sealIn, permitRerun, func(context.Context, *Repositories, *CopilotRerunScope, *model.CopilotDefinitionSnapshot, *CopilotRerunOriginalEvidence, *CopilotRerunExecutionIdentities, *model.CopilotRerunIntent, *model.Agent, string) (*CopilotRerunSealedPayload, error) {
			t.Fatal("receipt cannot rebuild payload")
			return nil, nil
		})
		require.NoError(t, err)
		require.Equal(t, observed, replay)
		_, err = r.CopilotRerunInspectSeal(ctx, "another-owner", saved.ProjectID, saved.IntentID, permitRerun)
		require.ErrorIs(t, err, ErrCopilotRerunForbidden)
	}
	require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", saved.ProjectID).Update("status", model.ProjectStatusArchived).Error)
	_, err = r.CopilotRerunInspectClaim(ctx, saved.OwnerUserID, saved.ProjectID, saved.IntentID, permitRerun)
	require.NoError(t, err)
	deny := func(context.Context, *Repositories, *CopilotRerunScope, string, string) error {
		return ErrCopilotRerunForbidden
	}
	_, err = r.CopilotRerunInspectSeal(ctx, saved.OwnerUserID, saved.ProjectID, saved.IntentID, deny)
	require.ErrorIs(t, err, ErrCopilotRerunForbidden)
}

func TestCopilotRerunClaimRechecksCandidateGrantExpiry(t *testing.T) {
	r, saved, in := rerunClaimFixture(t)
	db := r.database.Database()
	ctx := context.Background()
	require.NoError(t, db.AutoMigrate(&model.RoleGrant{}))
	expiry := time.Now().UTC().Truncate(time.Millisecond).Add(300 * time.Millisecond)
	require.NoError(t, db.Create(&model.RoleGrant{GrantID: "candidate-expiring", SubjectType: "user", SubjectID: "approver", RoleID: "reviewer", ScopeType: "project", ScopeID: saved.ProjectID, IsEnabled: 1, ExpiresAt: &expiry}).Error)
	validate := func(ctx context.Context, r *Repositories, scope *CopilotRerunScope, snapshot *model.CopilotDefinitionSnapshot, evidence *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, intent *model.CopilotRerunIntent, agent *model.Agent, schedule *model.CopilotRerunSchedule) (*CopilotRerunClaimMaterial, error) {
		_, err := r.RoleGrant.ListAllEnabled(ctx)
		if err != nil {
			return nil, err
		}
		v, err := testRerunClaimValidator(ctx, r, scope, snapshot, evidence, ids, intent, agent, schedule)
		time.Sleep(time.Until(expiry) + 5*time.Millisecond)
		return v, err
	}
	got, err := r.CopilotRerunClaim(ctx, in, permitRerun, validate)
	require.Error(t, err)
	require.Nil(t, got)
	var job model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", in.JobRunID).First(&job).Error)
	require.Empty(t, job.ClaimID)
}
