// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

func rerunConfirmFixture(t *testing.T) (*Repositories, *model.CopilotRerunIntent, CopilotRerunConfirmInput) {
	t.Helper()
	r, in := rerunIntentFixture(t)
	require.NoError(t, r.database.Database().AutoMigrate(&model.CopilotRerunAdmissionGate{}, &model.CopilotRerunSchedule{}))
	saved, err := testRerunPrepare(r, context.Background(), in, permitRerun)
	require.NoError(t, err)
	return r, saved, CopilotRerunConfirmInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, ExpectedVersion: saved.Version, ExpectedIntentDigest: saved.IntentDigest, ExpectedReviewedRunContextDigest: saved.ReviewedRunContextDigest}
}
func TestCopilotRerunConfirmCreatesOneInertTupleAndExactReceipt(t *testing.T) {
	r, saved, in := rerunConfirmFixture(t)
	ctx := context.Background()
	db := r.database.Database()
	var oldRun model.PipelineRun
	var oldJob model.JobRun
	require.NoError(t, db.Where("run_id = ?", saved.OriginalRunID).First(&oldRun).Error)
	require.NoError(t, db.Where("job_run_id = ?", saved.OriginalJobRunID).First(&oldJob).Error)
	receipt, err := r.CopilotRerunConfirm(ctx, in, permitRerun, testRerunReview)
	require.NoError(t, err)
	require.Equal(t, CopilotRerunConfirmed, receipt.State)
	require.EqualValues(t, 2, receipt.Version)
	require.NotNil(t, receipt.ConfirmedAt)
	replay, err := r.CopilotRerunConfirm(ctx, in, permitRerun, func(context.Context, *Repositories, *CopilotRerunScope, string, *model.CopilotDefinitionSnapshot, *CopilotRerunOriginalEvidence, *CopilotRerunExecutionIdentities) (*CopilotRerunReviewedTarget, error) {
		t.Fatal("historical receipt must not rebuild policy")
		return nil, nil
	})
	require.NoError(t, err)
	require.Equal(t, receipt, replay)
	observed, err := r.CopilotRerunInspect(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
	require.NoError(t, err)
	require.Equal(t, receipt, observed)
	_, err = r.CopilotRerunCancelPrepared(ctx, in, permitRerun)
	require.ErrorIs(t, err, ErrCopilotRerunConflict)
	var newRun model.PipelineRun
	var job model.JobRun
	var steps []model.StepRun
	require.NoError(t, db.Where("run_id = ?", saved.NewRunID).First(&newRun).Error)
	require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
	require.NoError(t, db.Where("pipeline_run_id = ?", saved.NewRunID).Find(&steps).Error)
	require.Equal(t, saved.IntentID, newRun.CopilotRerunIntentID)
	require.Equal(t, "{}", newRun.Env)
	require.Empty(t, newRun.CommitSha)
	require.Equal(t, saved.ExpectedSourceCommit, job.ExpectedSourceCommit)
	require.Empty(t, job.ClaimID)
	require.Empty(t, job.SourceResolvedCommit)
	require.Empty(t, job.InputManifest)
	require.Equal(t, "unknown", job.InputManifestState)
	require.Empty(t, job.RetryApprovalID)
	require.NotEmpty(t, steps)
	for _, step := range steps {
		require.Empty(t, step.ClaimID)
		require.Equal(t, "{}", step.Env)
		require.Equal(t, "{}", step.Secrets)
	}
	var rereadRun model.PipelineRun
	var rereadJob model.JobRun
	require.NoError(t, db.Where("run_id = ?", saved.OriginalRunID).First(&rereadRun).Error)
	require.NoError(t, db.Where("job_run_id = ?", saved.OriginalJobRunID).First(&rereadJob).Error)
	require.Equal(t, oldRun, rereadRun)
	require.Equal(t, oldJob, rereadJob)
	require.EqualError(t, r.JobRun.UpdateByJobRunID(ctx, job.JobRunID, map[string]any{"status": model.JobRunStatusRunning}), "execution identity or state changed")
}
func TestCopilotRerunConfirmRejectsPreexistingChildren(t *testing.T) {
	for _, kind := range []string{"job", "step", "job-only-step", "other-run-job-step"} {
		t.Run(kind, func(t *testing.T) {
			r, saved, in := rerunConfirmFixture(t)
			db := r.database.Database()
			ctx := context.Background()
			if kind == "job-only-step" || kind == "other-run-job-step" {
				parent := ""
				if kind == "other-run-job-step" {
					parent = "other-missing-run"
				}
				require.NoError(t, r.StepRun.Create(ctx, &model.StepRun{StepRunID: "orphan", PipelineRunID: parent, JobRunID: saved.NewJobRunID}))
			} else if kind == "job" {
				require.NoError(t, r.JobRun.Create(ctx, &model.JobRun{JobRunID: "orphan", PipelineRunID: saved.NewRunID}))
			} else {
				require.NoError(t, r.StepRun.Create(ctx, &model.StepRun{StepRunID: "orphan", PipelineRunID: saved.NewRunID}))
			}
			_, err := r.CopilotRerunConfirm(ctx, in, permitRerun, testRerunReview)
			require.ErrorIs(t, err, ErrCopilotRerunConflict)
			var n int64
			require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", saved.NewRunID).Count(&n).Error)
			require.Zero(t, n)
			var intent model.CopilotRerunIntent
			require.NoError(t, db.Where("intent_id = ?", saved.IntentID).First(&intent).Error)
			require.Equal(t, CopilotRerunPrepared, intent.State)
		})
	}
}
func TestCopilotRerunConfirmRollbackEveryInsertion(t *testing.T) {
	for failure := 1; failure <= 6; failure++ {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			r, saved, in := rerunConfirmFixture(t)
			db := r.database.Database()
			calls := 0
			key := "rerun-fail-insertion"
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register(key, func(tx *gorm.DB) {
				calls++
				if calls == failure {
					tx.AddError(errors.New("synthetic insertion failure"))
				}
			}))
			defer db.Callback().Create().Remove(key)
			_, err := r.CopilotRerunConfirm(context.Background(), in, permitRerun, testRerunReview)
			require.Equal(t, failure, calls)
			require.Error(t, err)
			require.NoError(t, db.Callback().Create().Remove(key))
			observed, err := r.CopilotRerunInspect(context.Background(), in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
			require.NoError(t, err)
			require.Equal(t, CopilotRerunPrepared, observed.State)
			require.Equal(t, saved.Version, observed.Version)
		})
	}
}
func TestCopilotRerunConfirmCurrentAuthorityPolicyAndCancel(t *testing.T) {
	for _, scenario := range []string{"revoked", "policy", "version", "digest", "expired", "cancel-first"} {
		t.Run(scenario, func(t *testing.T) {
			r, saved, in := rerunConfirmFixture(t)
			auth := CopilotRerunAuthorize(permitRerun)
			review := CopilotRerunReviewBuilder(testRerunReview)
			expected := ErrCopilotRerunConflict
			switch scenario {
			case "revoked":
				auth = func(context.Context, *Repositories, *CopilotRerunScope, string, string) error {
					return errors.New("revoked")
				}
				expected = ErrCopilotRerunForbidden
			case "policy":
				review = func(ctx context.Context, v *Repositories, s *CopilotRerunScope, o string, d *model.CopilotDefinitionSnapshot, e *CopilotRerunOriginalEvidence, i *CopilotRerunExecutionIdentities) (*CopilotRerunReviewedTarget, error) {
					x, err := testRerunReview(ctx, v, s, o, d, e, i)
					if err == nil {
						x.AdmissionPolicyDigest = rerunDigest("changed", true)
					}
					return x, err
				}
			case "version":
				in.ExpectedVersion++
			case "digest":
				in.ExpectedIntentDigest = rerunDigest("changed", true)
			case "expired":
				saved.ExpiresAt = time.Now().Add(-time.Second).UTC().Truncate(time.Millisecond)
				saved.IntentDigest = rerunIntentDigest(saved)
				require.NoError(t, r.database.Database().Model(saved).Updates(map[string]any{"expires_at": saved.ExpiresAt, "intent_digest": saved.IntentDigest}).Error)
				in.ExpectedIntentDigest = saved.IntentDigest
				expected = ErrCopilotRerunExpired
			case "cancel-first":
				cancelled, err := r.CopilotRerunCancelPrepared(context.Background(), in, permitRerun)
				require.NoError(t, err)
				require.Equal(t, CopilotRerunCancelled, cancelled.State)
				again, err := r.CopilotRerunCancelPrepared(context.Background(), in, permitRerun)
				require.NoError(t, err)
				require.Equal(t, cancelled, again)
			}
			_, err := r.CopilotRerunConfirm(context.Background(), in, auth, review)
			require.ErrorIs(t, err, expected)
			var ids CopilotRerunExecutionIdentities
			require.NoError(t, json.Unmarshal([]byte(saved.StepIdentitiesJSON), &ids))
			require.NoError(t, inspectAbsentCopilotRerunTuple(context.Background(), r.database.Database(), &ids))
		})
	}
}

func TestCopilotRerunConfirmLostCommitAcknowledgement(t *testing.T) {
	r, saved, in := rerunConfirmFixture(t)
	db := r.database.Database()
	pool, err := db.DB()
	require.NoError(t, err)
	lose := &atomic.Bool{}
	lose.Store(true)
	wrapped := db.Session(&gorm.Session{NewDB: true, Initialized: true})
	wrapped.Statement.ConnPool = historyLostCommitPool{ConnPool: pool, pool: pool, lose: lose}
	loss := &Repositories{database: statusTestDB{wrapped}}
	receipt, err := loss.CopilotRerunConfirm(context.Background(), in, permitRerun, testRerunReview)
	require.Nil(t, receipt)
	require.ErrorIs(t, err, ErrCopilotRerunUnavailable)
	require.False(t, lose.Load())
	observed, err := r.CopilotRerunInspect(context.Background(), in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
	require.NoError(t, err)
	require.Equal(t, CopilotRerunConfirmed, observed.State)
	require.Equal(t, saved.NewRunID, observed.NewRunID)
	replay, err := r.CopilotRerunConfirm(context.Background(), in, permitRerun, testRerunReview)
	require.NoError(t, err)
	require.Equal(t, observed, replay)
}
func TestCopilotRerunInspectRejectsAlteredTupleAndNoncanonicalID(t *testing.T) {
	for _, kind := range []string{"extra-step", "extra-job", "wrong-step-policy", "wrong-source", "missing-gate", "trigger-type", "deployment-flag", "continue-on-error", "step-actor", "uppercase"} {
		t.Run(kind, func(t *testing.T) {
			r, saved, in := rerunConfirmFixture(t)
			ctx := context.Background()
			db := r.database.Database()
			_, err := r.CopilotRerunConfirm(ctx, in, permitRerun, testRerunReview)
			require.NoError(t, err)
			switch kind {
			case "extra-step":
				require.NoError(t, db.Create(&model.StepRun{StepRunID: "unexpected", PipelineRunID: saved.NewRunID}).Error)
			case "extra-job":
				require.NoError(t, db.Create(&model.JobRun{JobRunID: "unexpected", PipelineRunID: saved.NewRunID}).Error)
			case "wrong-step-policy":
				require.NoError(t, db.Model(&model.StepRun{}).Where("pipeline_run_id = ?", saved.NewRunID).Update("retry_policy", "{}").Error)
			case "wrong-source":
				require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", saved.NewJobRunID).Update("source_repository", "https://wrong.invalid/repo").Error)
			case "missing-gate":
				require.NoError(t, db.Where("gate_id = ?", saved.GateID).Delete(&model.CopilotRerunAdmissionGate{}).Error)
			case "trigger-type":
				require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", saved.NewRunID).Update("trigger_type", 999).Error)
			case "deployment-flag":
				require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", saved.NewRunID).Update("allow_deployments", true).Error)
			case "continue-on-error":
				require.NoError(t, db.Model(&model.StepRun{}).Where("pipeline_run_id = ?", saved.NewRunID).Update("continue_on_error", true).Error)
			case "step-actor":
				require.NoError(t, db.Model(&model.StepRun{}).Where("pipeline_run_id = ?", saved.NewRunID).Update("created_by", "another").Error)
			case "uppercase":
				in.IntentID = strings.ToUpper(in.IntentID)
			}
			_, err = r.CopilotRerunInspect(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
			require.Error(t, err)
		})
	}
}

func TestCopilotRerunConfirmAndInspectionUsePrimary(t *testing.T) {
	r, _, in := rerunConfirmFixture(t)
	db := r.database.Database()
	require.NoError(t, db.Use(dbresolver.Register(dbresolver.Config{Replicas: []gorm.Dialector{sqlite.Open(filepath.Join(t.TempDir(), "empty-replica.db"))}})))
	confirmed, err := r.CopilotRerunConfirm(context.Background(), in, permitRerun, testRerunReview)
	require.NoError(t, err)
	observed, err := r.CopilotRerunInspect(context.Background(), in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
	require.NoError(t, err)
	require.Equal(t, confirmed, observed)
}

func TestCopilotRerunConfirmSourceMembershipStartsSnapshotAfterLocks(t *testing.T) {
	r, saved, in := rerunConfirmFixture(t)
	db := r.database.Database()
	var firstSnapshot bool
	var lockedTables []string
	const name = "confirm-source-first-snapshot"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if _, inside := tx.Statement.ConnPool.(gorm.TxCommitter); !inside || tx.Error != nil {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; locked {
			if !firstSnapshot {
				lockedTables = append(lockedTables, tx.Statement.Table)
			}
			return
		}
		if firstSnapshot {
			return
		}
		firstSnapshot = true
		require.NoError(t, requireRerunAttachmentTransaction(tx))
		require.Equal(t, "job_run", tx.Statement.Table)
		require.Equal(t, []string{"job_run_id"}, tx.Statement.Selects)
		require.Contains(t, tx.Statement.Vars, saved.OriginalRunID)
		require.Contains(t, lockedTables, "pipeline")
		require.Contains(t, lockedTables, "project")
		require.Contains(t, lockedTables, "pipeline_run")
		require.Contains(t, lockedTables, "job_run")
		require.NotContains(t, lockedTables, "copilot_rerun_intent", "source membership precedes Intent and all policy callbacks")
		var job model.JobRun
		require.NoError(t, tx.Session(&gorm.Session{NewDB: true}).Where("job_run_id = ?", saved.OriginalJobRunID).First(&job).Error)
		require.NoError(t, requireExecutionScope(tx, &job))
	}))
	defer db.Callback().Query().Remove(name)
	_, err := r.CopilotRerunConfirm(context.Background(), in, permitRerun, testRerunReview)
	require.NoError(t, err)
	require.True(t, firstSnapshot, "the first snapshot must be established beneath the source Run and Job locks")
}
