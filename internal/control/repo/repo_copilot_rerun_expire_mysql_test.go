// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCopilotRerunExpiryMySQLRequiredGate(t *testing.T) {
	required := os.Getenv("ARCENTRA_REQUIRE_COPILOT_RERUN_MYSQL")
	require.True(t, required == "" || required == "1")
	dsn := os.Getenv("ARCENTRA_COPILOT_RERUN_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required disposable rerun MySQL missing")
		}
		t.Skip("real MySQL absent; no expiry contention acceptance claim")
	}
	cfg, err := rerunConfirmMySQLConfig(dsn)
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(10)
	defer pool.Close()
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.Empty(t, tables)
	for _, collation := range []string{"utf8mb4_0900_ai_ci", "utf8mb4_bin"} {
		t.Run(collation, func(t *testing.T) {
			for _, scenario := range []string{"claim-first", "expiry-first", "lost-expiry-ack"} {
				t.Run(scenario, func(t *testing.T) {
					models := append(rerunConfirmMySQLModels(), &model.Agent{}, &model.JobRunGeneration{}, &model.StepRunArtifact{}, &model.ProjectArtifact{}, &model.ApprovalRequest{}, &model.StepRunLog{})
					require.NoError(t, db.Session(&gorm.Session{NewDB: true}).Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation).AutoMigrate(models...))
					defer func() {
						for i := len(models) - 1; i >= 0; i-- {
							require.NoError(t, db.Migrator().DropTable(models[i]))
						}
					}()
					require.True(t, db.Migrator().HasIndex(&model.StepRunArtifact{}, "idx_step_run_artifact_job_run"), "required indexed expiry schema is not integrated")
					require.True(t, db.Migrator().HasIndex(&model.ProjectArtifact{}, "idx_project_artifact_job_run"), "required indexed expiry schema is not integrated")
					source, prepare := rerunIntentFixture(t)
					for _, entity := range models {
						if !source.database.Database().Migrator().HasTable(entity) {
							continue
						}
						rows := reflect.New(reflect.SliceOf(reflect.TypeOf(entity).Elem())).Interface()
						require.NoError(t, source.database.Database().Find(rows).Error)
						n := reflect.ValueOf(rows).Elem().Len()
						for i := 0; i < n; i++ {
							row := reflect.ValueOf(rows).Elem().Index(i).Addr().Interface()
							retryApprovalMySQLJSONDefaults(row)
							require.NoError(t, retryApprovalMySQLValidateJSON(db, row))
						}
						if n > 0 {
							require.NoError(t, db.Create(rows).Error)
						}
					}
					r := NewRepositories(statusTestDB{db}, nil)
					saved, ref := rerunExpiryMySQLPrepare(t, r, prepare)
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					if scenario == "lost-expiry-ack" {
						time.Sleep(time.Until(saved.ExpiresAt) + time.Millisecond)
						lose := &atomic.Bool{}
						lose.Store(true)
						wrapped := db.Session(&gorm.Session{NewDB: true, Initialized: true})
						wrapped.Statement.ConnPool = historyLostCommitPool{ConnPool: pool, pool: pool, lose: lose}
						uncertain := NewRepositories(statusTestDB{wrapped}, nil)
						out, err := uncertain.CopilotRerunExpireUnclaimed(ctx, saved.ScheduleID)
						require.ErrorIs(t, err, ErrCopilotRerunUnavailable)
						require.Nil(t, out)
						first, err := r.CopilotRerunExpireUnclaimed(ctx, saved.ScheduleID)
						require.NoError(t, err)
						again, err := r.CopilotRerunExpireUnclaimed(ctx, saved.ScheduleID)
						require.NoError(t, err)
						require.Equal(t, first, again)
						return
					}
					first, holder := rerunMySQLConnection(t, db)
					second, waiter := rerunMySQLConnection(t, db)
					firstRepo := NewRepositories(statusTestDB{first}, nil)
					secondRepo := NewRepositories(statusTestDB{second}, nil)
					held, release := make(chan struct{}), make(chan struct{})
					var once atomic.Bool
					const callback = "test:expiry-gate-holder"
					require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
						if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "CopilotRerunAdmissionGate" {
							return
						}
						if _, ok := tx.Statement.Clauses["FOR"]; !ok {
							return
						}
						if once.CompareAndSwap(false, true) {
							close(held)
							select {
							case <-release:
							case <-ctx.Done():
								tx.AddError(ctx.Err())
							}
						}
					}))
					defer db.Callback().Query().Remove(callback)
					if scenario == "expiry-first" {
						time.Sleep(time.Until(saved.ExpiresAt) + time.Millisecond)
					}
					firstDone, secondDone := make(chan error, 1), make(chan error, 1)
					go func() {
						var e error
						if scenario == "expiry-first" {
							_, e = firstRepo.CopilotRerunExpireUnclaimed(ctx, saved.ScheduleID)
						} else {
							_, e = firstRepo.CopilotRerunClaim(ctx, expiryClaimInput(ref), permitRerun, testRerunClaimValidator)
						}
						firstDone <- e
					}()
					select {
					case <-held:
					case err := <-firstDone:
						t.Fatalf("holder exited before lock: %v", err)
					case <-ctx.Done():
						t.Fatal("holder deadline")
					}
					go func() {
						var e error
						if scenario == "expiry-first" {
							_, e = secondRepo.CopilotRerunClaim(ctx, expiryClaimInput(ref), permitRerun, testRerunClaimValidator)
						} else {
							_, e = secondRepo.CopilotRerunExpireUnclaimed(ctx, saved.ScheduleID)
						}
						secondDone <- e
					}()
					rerunMySQLWaitEdge(t, db, holder, waiter, secondDone)
					close(release)
					require.NoError(t, <-firstDone)
					require.Error(t, <-secondDone)
					var job model.JobRun
					require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
					if scenario == "expiry-first" {
						require.Empty(t, job.ClaimID)
						require.Equal(t, model.JobRunStatusCancelled, job.Status)
					} else {
						require.NotEmpty(t, job.ClaimID)
						require.Nil(t, job.EndTime)
						time.Sleep(time.Until(saved.ExpiresAt) + time.Millisecond)
						out, err := r.CopilotRerunExpireUnclaimed(ctx, saved.ScheduleID)
						require.Error(t, err)
						require.Nil(t, out)
					}
				})
			}
		})
	}
}

// Begin the deliberately finite admission window only after schema/source copy.
func rerunExpiryMySQLPrepare(t *testing.T, r *Repositories, prepare CopilotRerunPrepareInput) (*model.CopilotRerunIntent, *taskqueue.RerunClaimReference) {
	t.Helper()
	ctx := context.Background()
	db := r.database.Database()
	now := time.Now().UTC().Truncate(time.Millisecond)
	caps, _ := json.Marshal(taskqueue.AgentRuntimeCapabilities{ExecutionMode: taskqueue.RuntimeContainerdOfflineV1, ContainerdOfflineV1: true, ContainerdWholeJobRetryV1: true, ContainerdRetryApprovalV1: true, CopilotWholeJobRerunPreparationV1: true})
	agent := &model.Agent{AgentID: "sealed-agent", OS: "linux", IsEnabled: 1, Status: 1, LastHeartbeat: &now, RuntimeCapabilities: caps}
	retryApprovalMySQLJSONDefaults(agent)
	require.NoError(t, retryApprovalMySQLValidateJSON(db, agent))
	require.NoError(t, db.Create(agent).Error)
	prepare.ExpiresAt = time.Now().UTC().Add(5 * time.Second).Truncate(time.Millisecond)
	saved, err := testRerunPrepare(r, ctx, prepare, permitRerun)
	require.NoError(t, err)
	saved, err = r.CopilotRerunConfirm(ctx, CopilotRerunConfirmInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, ExpectedVersion: saved.Version, ExpectedIntentDigest: saved.IntentDigest, ExpectedReviewedRunContextDigest: saved.ReviewedRunContextDigest}, permitRerun, testRerunReview)
	require.NoError(t, err)
	lease, err := r.CopilotRerunAcquireLease(ctx, saved.ScheduleID, "expiry-controller", time.Minute)
	require.NoError(t, err)
	_, err = r.CopilotRerunSeal(ctx, *lease, CopilotRerunSealInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, AgentID: agent.AgentID, SealID: uuid.NewString(), ExpectedIntentDigest: saved.IntentDigest, ExpectedReviewedRunContextDigest: saved.ReviewedRunContextDigest, ExpectedIntentVersion: saved.Version, ExpectedGateVersion: 1, ExpectedScheduleVersion: 2}, permitRerun, testRerunSealBuilder)
	require.NoError(t, err)
	ref, err := r.CopilotRerunDispatchReference(ctx, *lease, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	return saved, ref
}
