// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func rerunReportMySQLModels() []any {
	return append(rerunConfirmMySQLModels(), &model.Agent{}, &model.StorageConfig{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}, &model.JobRunGeneration{}, &model.TeamMember{}, &model.Team{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Role{}, &model.RoleGrant{}, &model.ProjectArtifact{})
}
func rerunReportMySQLSeed(t *testing.T, db *gorm.DB) (*Repositories, *model.CopilotRerunIntent, taskqueue.JobInputIdentity, context.Context, *model.ProjectArtifact) {
	t.Helper()
	source, saved, identity, ctx, artifact := rerunOutputFixture(t)
	src := source.database.Database()
	for _, entity := range rerunReportMySQLModels() {
		if !src.Migrator().HasTable(entity) {
			continue
		}
		rows := reflect.New(reflect.SliceOf(reflect.TypeOf(entity).Elem())).Interface()
		require.NoError(t, src.Find(rows).Error)
		for i := 0; i < reflect.ValueOf(rows).Elem().Len(); i++ {
			row := reflect.ValueOf(rows).Elem().Index(i).Addr().Interface()
			retryApprovalMySQLJSONDefaults(row)
			require.NoError(t, retryApprovalMySQLValidateJSON(db, row))
		}
		if reflect.ValueOf(rows).Elem().Len() > 0 {
			require.NoError(t, db.Create(rows).Error)
		}
	}
	r := NewRepositories(statusTestDB{db}, nil)
	require.NoError(t, SetRetryApprovalAuthorizer(r, func(ctx context.Context, view RetryApprovalAuthorizationView, _ *retryapproval.Evidence, id, action string) error {
		u, e := view.LiveRepositories().User.(IExecutionActorReader).GetExecutionActor(ctx, id)
		if e != nil || u == nil || u.IsEnabled != 1 {
			return ErrApprovalForbidden
		}
		return nil
	}))
	t.Cleanup(func() { _ = SetRetryApprovalAuthorizer(r, nil) })
	return r, saved, identity, ctx, artifact
}

func TestCopilotRerunReportsMySQLRequiredGate(t *testing.T) {
	required := os.Getenv("ARCENTRA_REQUIRE_COPILOT_RERUN_MYSQL")
	require.True(t, required == "" || required == "1")
	dsn := os.Getenv("ARCENTRA_COPILOT_RERUN_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required disposable rerun MySQL missing")
		}
		t.Skip("real MySQL absent; no postclaim contention acceptance claim")
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
			for _, scenario := range []string{"report-first", "cancel-first", "duplicate-finalizer", "artifact-rollback", "lost-finalizer-ack"} {
				t.Run(scenario, func(t *testing.T) {
					models := rerunReportMySQLModels()
					scoped := db.Session(&gorm.Session{NewDB: true}).Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation)
					require.NoError(t, scoped.AutoMigrate(models...))
					defer func() {
						for i := len(models) - 1; i >= 0; i-- {
							require.NoError(t, db.Migrator().DropTable(models[i]))
						}
					}()
					r, saved, identity, reportCtx, artifact := rerunReportMySQLSeed(t, db)
					rerunReportMySQLScenario(t, db, r, saved, identity, reportCtx, artifact, scenario)
				})
			}
		})
	}
}
func rerunReportMySQLScenario(t *testing.T, db *gorm.DB, r *Repositories, saved *model.CopilotRerunIntent, identity taskqueue.JobInputIdentity, reportCtx context.Context, artifact *model.ProjectArtifact, scenario string) {
	ctx, cancel := context.WithTimeout(reportCtx, 15*time.Second)
	defer cancel()
	jobs := r.JobRun.(*JobRunRepo)
	job, err := jobs.GetByJobRunID(ctx, identity.JobRunID)
	require.NoError(t, err)
	updates := map[string]any{"status": model.JobRunStatusSuccess, "claim_id": identity.ClaimID, "terminal_report_digest": strings.Repeat("a", 64), "end_time": time.Now().UTC().Truncate(time.Millisecond), "source_resolved_commit": job.ExpectedSourceCommit, "source_provenance_state": "reported"}
	publish := func(store *Repositories) error {
		return store.JobRun.(*JobRunRepo).PatchReportedStatusAndArtifacts(ctx, job, updates, []*model.ProjectArtifact{artifact})
	}
	if scenario == "artifact-rollback" {
		const callback = "rerun-mysql-output-failure"
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "JobRun" {
				tx.AddError(ErrCopilotRerunConflict)
			}
		}))
		require.Error(t, publish(r))
		require.NoError(t, db.Callback().Update().Remove(callback))
		var count int64
		require.NoError(t, db.Model(&model.ProjectArtifact{}).Count(&count).Error)
		require.Zero(t, count)
		current, err := jobs.GetByJobRunID(ctx, identity.JobRunID)
		require.NoError(t, err)
		require.Equal(t, model.JobRunStatusRunning, current.Status)
		return
	}
	if scenario == "duplicate-finalizer" || scenario == "lost-finalizer-ack" {
		require.NoError(t, publish(r))
	}
	if scenario == "lost-finalizer-ack" {
		pool, err := db.DB()
		require.NoError(t, err)
		lose := &atomic.Bool{}
		lose.Store(true)
		wrapped := db.Session(&gorm.Session{NewDB: true, Initialized: true})
		wrapped.Statement.ConnPool = historyLostCommitPool{ConnPool: pool, pool: pool, lose: lose}
		uncertain := NewRepositories(statusTestDB{wrapped}, nil)
		receipt, err := uncertain.JobRun.(*JobRunRepo).FinalizeCopilotRerun(ctx, identity)
		require.ErrorIs(t, err, ErrCopilotRerunUnavailable)
		require.Nil(t, receipt)
		receipt, err = jobs.FinalizeCopilotRerun(ctx, identity)
		require.NoError(t, err)
		require.Equal(t, model.PipelineStatusSuccess, receipt.RunStatus)
		require.NoError(t, jobs.ValidateCompletedArtifacts(ctx, job.JobRunID, []*model.ProjectArtifact{artifact}))
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
	const callback = "rerun-postclaim-first-prefix"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "Pipeline" {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		if once.CompareAndSwap(false, true) {
			close(held)
			<-release
		}
	}))
	defer db.Callback().Query().Remove(callback)
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() {
		if scenario == "cancel-first" {
			_, e := firstRepo.CopilotRerunCancelClaimed(ctx, saved.OwnerUserID, identity, permitRerun)
			firstDone <- e
		} else if scenario == "duplicate-finalizer" {
			_, e := firstRepo.JobRun.(*JobRunRepo).FinalizeCopilotRerun(ctx, identity)
			firstDone <- e
		} else {
			firstDone <- publish(firstRepo)
		}
	}()
	select {
	case <-held:
	case e := <-firstDone:
		t.Fatalf("first operation ended before prefix lock: %v", e)
	case <-ctx.Done():
		t.Fatal("prefix holder timeout")
	}
	go func() {
		if scenario == "report-first" {
			_, e := secondRepo.CopilotRerunCancelClaimed(ctx, saved.OwnerUserID, identity, permitRerun)
			secondDone <- e
		} else if scenario == "duplicate-finalizer" {
			_, e := secondRepo.JobRun.(*JobRunRepo).FinalizeCopilotRerun(ctx, identity)
			secondDone <- e
		} else {
			secondDone <- publish(secondRepo)
		}
	}()
	rerunMySQLWaitEdge(t, db, holder, waiter, secondDone)
	close(release)
	require.NoError(t, <-firstDone)
	secondErr := <-secondDone
	if scenario == "duplicate-finalizer" {
		require.NoError(t, secondErr)
	} else {
		require.Error(t, secondErr)
	}
	receipt, err := jobs.FinalizeCopilotRerun(ctx, identity)
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Model(&model.ProjectArtifact{}).Count(&count).Error)
	if scenario == "cancel-first" {
		require.Equal(t, model.PipelineStatusCancelled, receipt.RunStatus)
		require.Zero(t, count)
	} else {
		require.Equal(t, model.PipelineStatusSuccess, receipt.RunStatus)
		require.EqualValues(t, 1, count)
		require.NoError(t, jobs.ValidateCompletedArtifacts(ctx, job.JobRunID, []*model.ProjectArtifact{artifact}))
	}
	var original model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", saved.OriginalJobRunID).First(&original).Error)
	require.Equal(t, model.JobRunStatusFailed, original.Status)
	t.Log("exact postclaim prefix wait, terminal aggregate and once-only output receipt verified")
}

func TestCopilotRerunReportMySQLSeedJSONShapes(t *testing.T) {
	r, _, _, _, _ := rerunOutputFixture(t)
	db := r.database.Database()
	for _, entity := range rerunReportMySQLModels() {
		if !db.Migrator().HasTable(entity) {
			continue
		}
		rows := reflect.New(reflect.SliceOf(reflect.TypeOf(entity).Elem())).Interface()
		require.NoError(t, db.Find(rows).Error)
		for i := 0; i < reflect.ValueOf(rows).Elem().Len(); i++ {
			row := reflect.ValueOf(rows).Elem().Index(i).Addr().Interface()
			retryApprovalMySQLJSONDefaults(row)
			require.NoError(t, retryApprovalMySQLValidateJSON(db, row))
		}
	}
}
