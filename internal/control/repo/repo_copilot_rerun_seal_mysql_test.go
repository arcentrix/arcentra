// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func rerunSealMySQLSeed(t *testing.T, db *gorm.DB) (*Repositories, *model.CopilotRerunIntent, CopilotRerunLeaseToken, CopilotRerunSealInput) {
	t.Helper()
	r, _, confirm := rerunConfirmMySQLSeed(t, db)
	ctx := context.Background()
	saved, err := r.CopilotRerunConfirm(ctx, confirm, permitRerun, testRerunReview)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Millisecond)
	caps, _ := json.Marshal(taskqueue.AgentRuntimeCapabilities{ExecutionMode: taskqueue.RuntimeContainerdOfflineV1, ContainerdOfflineV1: true, ContainerdWholeJobRetryV1: true, ContainerdRetryApprovalV1: true, CopilotWholeJobRerunPreparationV1: true})
	agent := &model.Agent{AgentID: "sealed-agent", OS: "linux", IsEnabled: 1, Status: 1, LastHeartbeat: &now, RuntimeCapabilities: caps}
	retryApprovalMySQLJSONDefaults(agent)
	require.NoError(t, retryApprovalMySQLValidateJSON(db, agent))
	require.NoError(t, db.Create(agent).Error)
	lease, err := r.CopilotRerunAcquireLease(ctx, saved.ScheduleID, "controller", time.Minute)
	require.NoError(t, err)
	return r, saved, *lease, CopilotRerunSealInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, AgentID: agent.AgentID, SealID: uuid.NewString(), ExpectedIntentDigest: saved.IntentDigest, ExpectedReviewedRunContextDigest: saved.ReviewedRunContextDigest, ExpectedIntentVersion: saved.Version, ExpectedGateVersion: 1, ExpectedScheduleVersion: 2}
}
func TestCopilotRerunSealMySQLRequiredGate(t *testing.T) {
	required := os.Getenv("ARCENTRA_REQUIRE_COPILOT_RERUN_MYSQL")
	require.True(t, required == "" || required == "1")
	dsn := os.Getenv("ARCENTRA_COPILOT_RERUN_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required disposable rerun MySQL missing")
		}
		t.Skip("real MySQL absent; no seal concurrency acceptance claim")
	}
	cfg, err := rerunConfirmMySQLConfig(dsn)
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(12)
	defer pool.Close()
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.Empty(t, tables)
	for _, collation := range []string{"utf8mb4_0900_ai_ci", "utf8mb4_bin"} {
		t.Run(collation, func(t *testing.T) {
			for _, scenario := range []string{"seal-first", "cancel-first", "duplicate-seal", "lease-takeover", "expiry-wait", "revocation-wait", "rollback", "lost-commit-ack"} {
				t.Run(scenario, func(t *testing.T) {
					models := append(rerunConfirmMySQLModels(), &model.Agent{})
					scoped := db.Session(&gorm.Session{NewDB: true}).Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation)
					require.NoError(t, scoped.AutoMigrate(models...))
					defer func() {
						for i := len(models) - 1; i >= 0; i-- {
							require.NoError(t, db.Migrator().DropTable(models[i]))
						}
					}()
					r, saved, lease, in := rerunSealMySQLSeed(t, db)
					rerunSealMySQLScenario(t, db, r, saved, lease, in, scenario)
				})
			}
		})
	}
}
func rerunSealMySQLScenario(t *testing.T, db *gorm.DB, r *Repositories, saved *model.CopilotRerunIntent, lease CopilotRerunLeaseToken, in CopilotRerunSealInput, scenario string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cancelInput := CopilotRerunTargetCancelInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, ExpectedIntentDigest: saved.IntentDigest, ExpectedIntentVersion: saved.Version, ExpectedGateVersion: 1, ExpectedScheduleVersion: 2}
	if scenario == "lost-commit-ack" {
		pool, err := db.DB()
		require.NoError(t, err)
		lose := &atomic.Bool{}
		lose.Store(true)
		wrapped := db.Session(&gorm.Session{NewDB: true, Initialized: true})
		wrapped.Statement.ConnPool = historyLostCommitPool{ConnPool: pool, pool: pool, lose: lose}
		uncertain := NewRepositories(statusTestDB{wrapped}, nil)
		got, err := uncertain.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
		require.Nil(t, got)
		require.ErrorIs(t, err, ErrCopilotRerunUnavailable)
		observed, err := r.CopilotRerunInspectSeal(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
		require.NoError(t, err)
		require.Equal(t, in.SealID, observed.Binding.SealID)
		cancelInput.ExpectedGateVersion = 2
		cancelInput.ExpectedScheduleVersion = 3
		_, err = r.CopilotRerunCancelUnclaimed(ctx, cancelInput, permitRerun)
		require.NoError(t, err)
		observed, err = r.CopilotRerunInspectSeal(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
		require.NoError(t, err)
		require.Equal(t, CopilotRerunScheduleCancelled, observed.State)
		require.Equal(t, in.SealID, observed.Binding.SealID)
		replay, err := r.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
		require.NoError(t, err)
		require.Equal(t, observed, replay)

		return
	}
	if scenario == "rollback" {
		const name = "rerun-seal-rollback"
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
			if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CopilotRerunSchedule" {
				tx.AddError(errors.New("synthetic schedule write failure"))
			}
		}))
		_, err := r.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
		require.NoError(t, db.Callback().Update().Remove(name))
		require.Error(t, err)
		observed, err := r.CopilotRerunInspectSeal(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
		require.NoError(t, err)
		require.Equal(t, CopilotRerunSchedulePending, observed.State)
		return
	}
	first, holder := rerunMySQLConnection(t, db)
	second, waiter := rerunMySQLConnection(t, db)
	firstRepo, secondRepo := NewRepositories(statusTestDB{first}, nil), NewRepositories(statusTestDB{second}, nil)
	held, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var once atomic.Bool
	if scenario == "lease-takeover" || scenario == "expiry-wait" || scenario == "revocation-wait" {
		firstDone := make(chan error, 1)
		secondDone := make(chan error, 1)
		if scenario == "expiry-wait" {
			lease.Until = time.Now().UTC().Truncate(time.Millisecond).Add(500 * time.Millisecond)
			require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("lease_until", lease.Until).Error)
		}
		go func() {
			firstDone <- first.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if scenario == "lease-takeover" {
					if err := tx.Exec("SELECT schedule_id FROM copilot_rerun_schedule WHERE schedule_id = ? FOR UPDATE", saved.ScheduleID).Error; err != nil {
						return err
					}
				} else {
					if err := tx.Exec("SELECT gate_id FROM copilot_rerun_admission_gate WHERE gate_id = ? FOR UPDATE", saved.GateID).Error; err != nil {
						return err
					}
				}
				close(held)
				<-release
				if scenario == "lease-takeover" {
					return tx.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Updates(map[string]any{"lease_epoch": lease.Epoch + 1, "lease_owner": "replacement", "version": 3}).Error
				}
				if scenario == "revocation-wait" {
					return tx.Model(&model.User{}).Where("user_id = ?", saved.OwnerUserID).Update("is_enabled", 0).Error
				}
				return nil
			})
		}()
		select {
		case <-held:
		case err := <-firstDone:
			t.Fatalf("holder ended before lock: %v", err)
		case <-ctx.Done():
			t.Fatal("holder lock timeout")
		}
		go func() {
			_, err := secondRepo.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
			secondDone <- err
		}()
		rerunMySQLWaitEdge(t, db, holder, waiter, secondDone)
		if scenario == "expiry-wait" {
			time.Sleep(time.Until(lease.Until) + 20*time.Millisecond)
		}
		close(release)
		require.NoError(t, <-firstDone)
		require.Error(t, <-secondDone)
		var job model.JobRun
		require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
		require.Empty(t, job.AgentID)
		return
	}
	const callback = "rerun-seal-gate-holder"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "CopilotRerunAdmissionGate" {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; !ok {
			return
		}
		if once.CompareAndSwap(false, true) {
			close(held)
			<-release
		}
	}))
	defer func() { require.NoError(t, db.Callback().Query().Remove(callback)) }()
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() {
		var err error
		if scenario == "cancel-first" {
			_, err = firstRepo.CopilotRerunCancelUnclaimed(ctx, cancelInput, permitRerun)
		} else {
			_, err = firstRepo.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
		}
		firstDone <- err
	}()
	select {
	case <-held:
	case err := <-firstDone:
		t.Fatalf("holder ended before lock: %v", err)
	case <-ctx.Done():
		t.Fatal("holder lock timeout")
	}
	go func() {
		var err error
		if scenario == "seal-first" {
			_, err = secondRepo.CopilotRerunCancelUnclaimed(ctx, cancelInput, permitRerun)
		} else {
			_, err = secondRepo.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
		}
		secondDone <- err
	}()
	rerunMySQLWaitEdge(t, db, holder, waiter, secondDone)
	close(release)
	require.NoError(t, <-firstDone)
	err := <-secondDone
	if scenario == "duplicate-seal" {
		require.NoError(t, err)
	} else {
		require.ErrorIs(t, err, ErrCopilotRerunConflict)
	}
}
