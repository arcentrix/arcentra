// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/safe"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func rerunConfirmMySQLConfig(dsn string) (*drivermysql.Config, error) {
	c, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid rerun fixture connection")
	}
	name := c.DBName
	const prefix = "arcentra_copilot_rerun_test"
	if name != prefix && !strings.HasPrefix(name, prefix+"_") || len(name) > 64 || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return nil, fmt.Errorf("dedicated rerun schema required")
	}
	c.DBName = "arcentra_copilot_execution_test"
	c, err = copilotExecutionMySQLConfig(c.FormatDSN())
	if err != nil {
		return nil, err
	}
	c.DBName = name
	// Prove the production helper does not depend on operator default RR.
	c.Params["transaction_isolation"] = "'READ-COMMITTED'"
	return c, nil
}
func rerunConfirmMySQLModels() []any {
	return []any{&model.Organization{}, &model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.User{}, &model.CopilotDefinitionSnapshot{}, &model.CopilotRerunIntent{}, &model.CopilotRerunAdmissionGate{}, &model.CopilotRerunSchedule{}}
}
func rerunConfirmMySQLSeed(t *testing.T, db *gorm.DB) (*Repositories, *model.CopilotRerunIntent, CopilotRerunConfirmInput) {
	t.Helper()
	source, saved, in := rerunConfirmFixture(t)
	src := source.database.Database()
	for _, entity := range rerunConfirmMySQLModels() {
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
	return NewRepositories(statusTestDB{db}, nil), saved, in
}
func rerunMySQLConnection(t *testing.T, db *gorm.DB) (*gorm.DB, uint64) {
	t.Helper()
	pool, err := db.DB()
	require.NoError(t, err)
	conn, err := pool.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	var id uint64
	require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT CONNECTION_ID()").Scan(&id))
	bound := db.Session(&gorm.Session{NewDB: true, Initialized: true})
	bound.Statement.ConnPool = conn
	return bound, id
}
func rerunMySQLWaitEdge(t *testing.T, db *gorm.DB, holder, waiter uint64, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("waiter ended without server wait: %v", err)
		default:
		}
		var n int64
		require.NoError(t, db.Raw(`SELECT COUNT(*) FROM performance_schema.data_lock_waits w JOIN performance_schema.threads b ON b.THREAD_ID=w.BLOCKING_THREAD_ID JOIN performance_schema.threads r ON r.THREAD_ID=w.REQUESTING_THREAD_ID WHERE b.PROCESSLIST_ID=? AND r.PROCESSLIST_ID=?`, holder, waiter).Scan(&n).Error)
		if n > 0 {
			t.Log("exact independent MySQL holder/waiter lock edge observed")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("missing exact server lock wait")
}

// Every callback must remain registered until all operations using its processor
// have returned, including fatal assertion/timeout paths. Error channels alone
// cannot serve as joins because the server-wait probe may consume a failure.
type rerunMySQLWorkers struct {
	ctx     context.Context
	cancel  context.CancelFunc
	release chan struct{}
	once    sync.Once
	group   sync.WaitGroup
}

func newRerunMySQLWorkers(ctx context.Context) *rerunMySQLWorkers {
	ctx, cancel := context.WithCancel(ctx)
	return &rerunMySQLWorkers{ctx: ctx, cancel: cancel, release: make(chan struct{})}
}
func (w *rerunMySQLWorkers) start(fn func() error) <-chan error {
	done := make(chan error, 1)
	w.group.Add(1)
	safe.Go(func() {
		defer w.group.Done()
		done <- fn()
	})
	return done
}
func (w *rerunMySQLWorkers) releaseHolder() { w.once.Do(func() { close(w.release) }) }
func (w *rerunMySQLWorkers) finish() {
	w.cancel()
	w.releaseHolder()
	w.group.Wait()
}

func TestCopilotRerunConfirmMySQLDSNGuards(t *testing.T) {
	for _, dsn := range []string{"u@tcp(remote.invalid:3306)/arcentra_copilot_rerun_test", "u@tcp(127.0.0.1:3306)/production", "u@tcp(127.0.0.1:3306)/arcentra_copilot_rerun_testunsafe"} {
		_, err := rerunConfirmMySQLConfig(dsn)
		require.Error(t, err)
	}
	_, err := rerunConfirmMySQLConfig("u@tcp(127.0.0.1:3306)/arcentra_copilot_rerun_test_ci")
	require.NoError(t, err)
}
func TestCopilotRerunConfirmMySQLRequiredGate(t *testing.T) {
	required := os.Getenv("ARCENTRA_REQUIRE_COPILOT_RERUN_MYSQL")
	require.True(t, required == "" || required == "1")
	dsn := os.Getenv("ARCENTRA_COPILOT_RERUN_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required disposable MySQL missing")
		}
		t.Skip("real MySQL absent; no concurrency acceptance claim")
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
			models := rerunConfirmMySQLModels()
			require.NoError(t, db.Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation).AutoMigrate(models...))
			var column struct {
				DataType      string
				CollationName string
			}
			require.NoError(t, db.Raw("SELECT DATA_TYPE AS data_type, COLLATION_NAME AS collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='step_run' AND column_name='job_run_id'").Scan(&column).Error)
			require.Equal(t, "longtext", column.DataType)
			require.Equal(t, collation, column.CollationName)
			var prefix int
			require.NoError(t, db.Raw("SELECT SUB_PART FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='step_run' AND index_name='idx_step_run_job_run' AND column_name='job_run_id'").Scan(&prefix).Error)
			require.Equal(t, 191, prefix)

			defer func() {
				for i := len(models) - 1; i >= 0; i-- {
					require.NoError(t, db.Migrator().DropTable(models[i]))
				}
			}()
			for _, scenario := range []string{"source-step-move-attachment-first", "source-step-move-confirm-first", "source-step-job-attachment-first", "source-step-job-confirm-first", "original-job-move-attachment-first", "original-job-move-confirm-first", "original-job-duplicate-attachment-first", "original-job-duplicate-confirm-first", "source-job-attachment-first", "source-job-confirm-first", "source-job-move-attachment-first", "source-job-move-confirm-first", "job-attachment-first", "job-confirm-first", "step-attachment-first", "step-confirm-first", "job-move-attachment-first", "job-move-confirm-first", "step-move-attachment-first", "step-move-confirm-first", "step-job-only-attachment-first", "step-job-only-confirm-first", "job-only-run-move", "job-step-index", "duplicate-confirm", "cancel-first", "confirm-first", "revocation-wait", "policy-wait", "expiry-wait", "rollback-inserts", "lost-commit-ack", "canonical-identity"} {
				t.Run(scenario, func(t *testing.T) {
					defer func() {
						for i := len(models) - 1; i >= 0; i-- {
							require.NoError(t, db.Where("1=1").Delete(models[i]).Error)
						}
					}()
					r, saved, in := rerunConfirmMySQLSeed(t, db)
					rerunConfirmMySQLScenario(t, db, r, saved, in, scenario, collation)
				})
			}
		})
	}
}

func rerunConfirmMySQLScenario(t *testing.T, db *gorm.DB, r *Repositories, saved *model.CopilotRerunIntent, in CopilotRerunConfirmInput, scenario, collation string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if strings.HasPrefix(scenario, "source-job-") {
		rerunMySQLSourceAttachmentRace(t, ctx, db, r, saved, in, scenario)
		return
	}
	if strings.HasPrefix(scenario, "source-step-") || strings.HasPrefix(scenario, "original-job-") || strings.Contains(scenario, "attachment") || strings.Contains(scenario, "confirm-first") && (strings.HasPrefix(scenario, "job-") || strings.HasPrefix(scenario, "step-")) {
		rerunMySQLAttachmentRace(t, ctx, db, r, saved, in, scenario)
		return
	}
	switch scenario {
	case "job-only-run-move":
		require.NoError(t, r.StepRun.Create(ctx, &model.StepRun{StepRunID: "orphan", JobRunID: saved.NewJobRunID}))
		require.NoError(t, r.StepRun.PatchByStepRunID(ctx, "orphan", map[string]any{"pipeline_run_id": "other-absent-run"}))
		_, err := r.CopilotRerunConfirm(ctx, in, permitRerun, testRerunReview)
		require.ErrorIs(t, err, ErrCopilotRerunConflict)
	case "job-step-index":
		rows := make([]model.StepRun, 2000)
		for i := range rows {
			rows[i] = model.StepRun{StepRunID: fmt.Sprintf("unrelated-step-%04d", i), JobRunID: fmt.Sprintf("unrelated-job-%04d", i)}
			retryApprovalMySQLJSONDefaults(&rows[i])
		}
		require.NoError(t, db.CreateInBatches(rows, 200).Error)
		var plan []struct {
			Type string         `gorm:"column:type"`
			Key  sql.NullString `gorm:"column:key"`
		}
		require.NoError(t, db.Raw("EXPLAIN SELECT id FROM step_run WHERE job_run_id = ? LIMIT 1 FOR UPDATE", saved.NewJobRunID).Scan(&plan).Error)
		require.Len(t, plan, 1)
		require.True(t, plan[0].Key.Valid)
		require.Contains(t, []string{"ref", "range", "const"}, plan[0].Type)
		_, err := r.CopilotRerunConfirm(ctx, in, permitRerun, testRerunReview)
		require.NoError(t, err)
	case "lost-commit-ack":
		pool, err := db.DB()
		require.NoError(t, err)
		lose := &atomic.Bool{}
		lose.Store(true)
		wrapped := db.Session(&gorm.Session{NewDB: true, Initialized: true})
		wrapped.Statement.ConnPool = historyLostCommitPool{ConnPool: pool, pool: pool, lose: lose}
		loss := &Repositories{database: statusTestDB{wrapped}}
		got, err := loss.CopilotRerunConfirm(ctx, in, permitRerun, testRerunReview)
		require.Nil(t, got)
		require.ErrorIs(t, err, ErrCopilotRerunUnavailable)
		require.False(t, lose.Load())
		got, err = r.CopilotRerunInspect(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
		require.NoError(t, err)
		require.Equal(t, CopilotRerunConfirmed, got.State)
	case "canonical-identity":
		bad := in
		bad.IntentID = strings.ToUpper(in.IntentID)
		_, err := r.CopilotRerunConfirm(ctx, bad, permitRerun, testRerunReview)
		require.ErrorIs(t, err, ErrCopilotRerunInvalid)
		_, err = r.CopilotRerunInspect(ctx, in.OwnerUserID, in.ProjectID, bad.IntentID, permitRerun)
		require.ErrorIs(t, err, ErrCopilotRerunInvalid)
		alias := &model.PipelineRun{RunID: strings.ToUpper(saved.NewRunID), Env: "{}"}
		require.NoError(t, db.Create(alias).Error)
		_, err = r.CopilotRerunConfirm(ctx, in, permitRerun, testRerunReview)
		if collation == "utf8mb4_bin" {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ErrCopilotRerunConflict)
		}
	case "rollback-inserts":
		for failure := 1; failure <= 6; failure++ {
			calls := 0
			name := "rerun-insert-failure"
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
				calls++
				if calls == failure {
					tx.AddError(fmt.Errorf("synthetic insert failure"))
				}
			}))
			_, err := r.CopilotRerunConfirm(ctx, in, permitRerun, testRerunReview)
			require.NoError(t, db.Callback().Create().Remove(name))
			require.Error(t, err)
			require.Equal(t, failure, calls)
			got, err := r.CopilotRerunInspect(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
			require.NoError(t, err)
			require.Equal(t, CopilotRerunPrepared, got.State)
		}
	default:
		rerunMySQLSourceRace(t, ctx, db, r, saved, in, scenario)
	}
}

func rerunMySQLAttachmentRace(t *testing.T, ctx context.Context, db *gorm.DB, r *Repositories, saved *model.CopilotRerunIntent, in CopilotRerunConfirmInput, scenario string) {
	t.Helper()
	workers := newRerunMySQLWorkers(ctx)
	defer workers.cancel()
	ctx = workers.ctx
	first, firstID := rerunMySQLConnection(t, db)
	second, secondID := rerunMySQLConnection(t, db)
	firstRepo := NewRepositories(statusTestDB{first}, nil)
	secondRepo := NewRepositories(statusTestDB{second}, nil)
	attachFirst := strings.HasSuffix(scenario, "attachment-first")
	isJob := strings.HasPrefix(scenario, "job-") || strings.HasPrefix(scenario, "original-job-")
	originalJob := strings.HasPrefix(scenario, "original-job-")
	sourceJobStep := strings.HasPrefix(scenario, "source-step-job-")
	sourceStepMove := strings.HasPrefix(scenario, "source-step-move-")
	var originalSourceJob model.JobRun
	if originalJob || sourceJobStep {
		require.NoError(t, db.Where("job_run_id = ?", saved.OriginalJobRunID).First(&originalSourceJob).Error)
	}
	jobOnly := strings.Contains(scenario, "job-only")
	moving := strings.Contains(scenario, "-move-")
	if moving && !originalJob {
		if isJob {
			require.NoError(t, r.JobRun.Create(ctx, &model.JobRun{JobRunID: "ordinary-child"}))
		} else {
			parent := ""
			if sourceStepMove {
				parent = saved.OriginalRunID
				require.Less(t, saved.NewRunID, parent, "exercise the previous target-gap before source-Run inversion")
			}
			require.NoError(t, r.StepRun.Create(ctx, &model.StepRun{StepRunID: "ordinary-child", PipelineRunID: parent}))
		}
	}
	held, release := make(chan struct{}), workers.release
	var once atomic.Bool
	name := "rerun-parent-hold"
	callback := func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Name != map[bool]string{false: "PipelineRun", true: "JobRun"}[jobOnly] {
			return
		}
		matches := false
		if attachFirst {
			for _, arg := range tx.Statement.Vars {
				if id, ok := arg.(string); ok && id == map[bool]string{false: saved.NewRunID, true: saved.NewJobRunID}[jobOnly] {
					matches = true
				}
			}
			_, locked := tx.Statement.Clauses["FOR"]
			matches = matches && locked
		} else {
			if jobOnly {
				row, ok := tx.Statement.Dest.(*model.JobRun)
				matches = ok && row.JobRunID == saved.NewJobRunID
			} else {
				row, ok := tx.Statement.Dest.(*model.PipelineRun)
				matches = ok && row.RunID == saved.NewRunID
			}
		}
		if matches && once.CompareAndSwap(false, true) {
			close(held)
			select {
			case <-release:
			case <-ctx.Done():
				tx.AddError(ctx.Err())
			}
		}
	}
	if attachFirst {
		require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, callback))
		defer func() { workers.finish(); _ = db.Callback().Query().Remove(name) }()
	} else {
		require.NoError(t, db.Callback().Create().After("gorm:create").Register(name, callback))
		defer func() { workers.finish(); _ = db.Callback().Create().Remove(name) }()
	}
	attach := func(repos *Repositories) error {
		if originalJob {
			if moving {
				return repos.JobRun.UpdateByJobRunID(ctx, saved.OriginalJobRunID, map[string]any{"pipeline_run_id": saved.NewRunID})
			}
			return repos.JobRun.Create(ctx, &model.JobRun{JobRunID: saved.OriginalJobRunID, PipelineRunID: saved.NewRunID})
		}
		if sourceJobStep {
			return repos.StepRun.Create(ctx, &model.StepRun{StepRunID: "ordinary-child", PipelineRunID: saved.NewRunID, JobRunID: saved.OriginalJobRunID})
		}
		if moving {
			if isJob {
				return repos.JobRun.UpdateByJobRunID(ctx, "ordinary-child", map[string]any{"pipeline_run_id": saved.NewRunID})
			}
			return repos.StepRun.PatchByStepRunID(ctx, "ordinary-child", map[string]any{"pipeline_run_id": saved.NewRunID})
		}
		if isJob {
			return repos.JobRun.Create(ctx, &model.JobRun{JobRunID: "ordinary-child", PipelineRunID: saved.NewRunID})
		}
		if jobOnly {
			return repos.StepRun.Create(ctx, &model.StepRun{StepRunID: "ordinary-child", JobRunID: saved.NewJobRunID})
		}
		return repos.StepRun.Create(ctx, &model.StepRun{StepRunID: "ordinary-child", PipelineRunID: saved.NewRunID})
	}
	confirm := func(repos *Repositories) error {
		_, err := repos.CopilotRerunConfirm(ctx, in, permitRerun, testRerunReview)
		return err
	}
	firstDone := workers.start(func() error {
		if attachFirst {
			return attach(firstRepo)
		}
		return confirm(firstRepo)
	})
	select {
	case <-held:
	case err := <-firstDone:
		t.Fatalf("first operation ended before held boundary: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	secondDone := workers.start(func() error {
		if attachFirst {
			return confirm(secondRepo)
		}
		return attach(secondRepo)
	})
	rerunMySQLWaitEdge(t, db, firstID, secondID, secondDone)
	workers.releaseHolder()
	firstErr := awaitRetryMySQL(t, firstDone)
	err := awaitRetryMySQL(t, secondDone)
	if attachFirst && (originalJob || sourceJobStep) {
		switch {
		case sourceJobStep:
			require.EqualError(t, firstErr, "captured retry plan cannot acquire new step allocations")
		case moving:
			require.EqualError(t, firstErr, "execution identity or state changed")
		default:
			var duplicate *drivermysql.MySQLError
			require.ErrorAs(t, firstErr, &duplicate)
			require.EqualValues(t, 1062, duplicate.Number, "a lock timeout/deadlock is not a duplicate-identity rejection")
		}
		require.NoError(t, err)
	} else if attachFirst {
		require.NoError(t, firstErr)
		require.ErrorIs(t, err, ErrCopilotRerunConflict)
	} else {
		require.NoError(t, firstErr)
		if jobOnly {
			require.EqualError(t, err, "captured retry plan cannot acquire new step allocations")
		} else {
			require.ErrorIs(t, err, ErrCopilotRerunUnsupported)
		}
	}
	if originalJob || sourceJobStep {
		var after model.JobRun
		require.NoError(t, db.Where("job_run_id = ?", saved.OriginalJobRunID).First(&after).Error)
		require.Equal(t, originalSourceJob, after, "rejected source mutation must leave the exact original Job unchanged")
		var extraSteps int64
		require.NoError(t, db.Model(&model.StepRun{}).Where("step_run_id = ?", "ordinary-child").Count(&extraSteps).Error)
		require.Zero(t, extraSteps)
	}
	var parentCount int64
	require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", saved.NewRunID).Count(&parentCount).Error)
	if attachFirst && !originalJob && !sourceJobStep {
		require.Zero(t, parentCount)
	} else {
		require.EqualValues(t, 1, parentCount)
		_, err = r.CopilotRerunInspect(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
		require.NoError(t, err)
	}
}

func rerunMySQLSourceRace(t *testing.T, ctx context.Context, db *gorm.DB, r *Repositories, saved *model.CopilotRerunIntent, in CopilotRerunConfirmInput, scenario string) {
	t.Helper()
	workers := newRerunMySQLWorkers(ctx)
	defer workers.cancel()
	ctx = workers.ctx
	first, firstID := rerunMySQLConnection(t, db)
	second, secondID := rerunMySQLConnection(t, db)
	other := NewRepositories(statusTestDB{second}, nil)
	if scenario == "revocation-wait" || scenario == "policy-wait" || scenario == "expiry-wait" {
		if scenario == "expiry-wait" {
			saved.ExpiresAt = time.Now().Add(time.Second).UTC().Truncate(time.Millisecond)
			saved.IntentDigest = rerunIntentDigest(saved)
			in.ExpectedIntentDigest = saved.IntentDigest
			require.NoError(t, db.Model(saved).Updates(map[string]any{"expires_at": saved.ExpiresAt, "intent_digest": saved.IntentDigest}).Error)
		}
		holder := first.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
		require.NoError(t, holder.Error)
		defer holder.Rollback()
		require.NoError(t, holder.Exec("SELECT id FROM pipeline WHERE pipeline_id = ? FOR UPDATE", saved.PipelineID).Error)
		var changed atomic.Bool
		review := CopilotRerunReviewBuilder(testRerunReview)
		if scenario == "policy-wait" {
			review = func(ctx context.Context, v *Repositories, s *CopilotRerunScope, o string, d *model.CopilotDefinitionSnapshot, e *CopilotRerunOriginalEvidence, i *CopilotRerunExecutionIdentities) (*CopilotRerunReviewedTarget, error) {
				x, err := testRerunReview(ctx, v, s, o, d, e, i)
				if err == nil && changed.Load() {
					x.AdmissionPolicyDigest = rerunDigest("changed", true)
				}
				return x, err
			}
		}
		defer workers.finish()
		done := workers.start(func() error { _, err := other.CopilotRerunConfirm(ctx, in, permitRerun, review); return err })
		rerunMySQLWaitEdge(t, db, firstID, secondID, done)
		expected := ErrCopilotRerunConflict
		switch scenario {
		case "revocation-wait":
			require.NoError(t, holder.Model(&model.User{}).Where("user_id = ?", in.OwnerUserID).Update("is_enabled", 0).Error)
			expected = ErrCopilotRerunForbidden
		case "policy-wait":
			changed.Store(true)
		case "expiry-wait":
			expected = ErrCopilotRerunExpired
			timer := time.NewTimer(time.Until(saved.ExpiresAt) + 5*time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}

		}
		require.NoError(t, holder.Commit().Error)
		require.ErrorIs(t, awaitRetryMySQL(t, done), expected)
		var n int64
		require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", saved.NewRunID).Count(&n).Error)
		require.Zero(t, n)
		return
	}
	held, release := make(chan struct{}), workers.release
	var once atomic.Bool
	name := "rerun-source-hold"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Pipeline" {
			if _, locked := tx.Statement.Clauses["FOR"]; locked && once.CompareAndSwap(false, true) {
				close(held)
				select {
				case <-release:
				case <-ctx.Done():
					tx.AddError(ctx.Err())
				}
			}
		}
	}))
	defer func() { workers.finish(); _ = db.Callback().Query().Remove(name) }()
	firstRepo := NewRepositories(statusTestDB{first}, nil)
	firstDone := workers.start(func() error {
		var err error
		if scenario == "cancel-first" {
			_, err = firstRepo.CopilotRerunCancelPrepared(ctx, in, permitRerun)
		} else {
			_, err = firstRepo.CopilotRerunConfirm(ctx, in, permitRerun, testRerunReview)
		}
		return err
	})
	select {
	case <-held:
	case err := <-firstDone:
		t.Fatalf("missing source holder: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	secondDone := workers.start(func() error {
		var err error
		if scenario == "confirm-first" {
			_, err = other.CopilotRerunCancelPrepared(ctx, in, permitRerun)
		} else {
			_, err = other.CopilotRerunConfirm(ctx, in, permitRerun, testRerunReview)
		}
		return err
	})
	rerunMySQLWaitEdge(t, db, firstID, secondID, secondDone)
	workers.releaseHolder()
	firstErr := awaitRetryMySQL(t, firstDone)
	err := awaitRetryMySQL(t, secondDone)
	require.NoError(t, firstErr)
	if scenario == "duplicate-confirm" {
		require.NoError(t, err)
	} else {
		require.ErrorIs(t, err, ErrCopilotRerunConflict)
	}
	got, err := r.CopilotRerunInspect(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, permitRerun)
	require.NoError(t, err)
	if scenario == "cancel-first" {
		require.Equal(t, CopilotRerunCancelled, got.State)
	} else {
		require.Equal(t, CopilotRerunConfirmed, got.State)
	}
}

func TestCopilotRerunConfirmMySQLSeedJSONShapes(t *testing.T) {
	source, _, _ := rerunConfirmFixture(t)
	db := source.database.Database()
	for _, entity := range rerunConfirmMySQLModels() {
		retryApprovalMySQLJSONDefaults(entity)
		require.NoError(t, retryApprovalMySQLValidateJSON(db, entity))
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

func rerunMySQLSourceAttachmentRace(t *testing.T, ctx context.Context, db *gorm.DB, r *Repositories, saved *model.CopilotRerunIntent, in CopilotRerunConfirmInput, scenario string) {
	t.Helper()
	workers := newRerunMySQLWorkers(ctx)
	defer workers.cancel()
	ctx = workers.ctx
	first, firstID := rerunMySQLConnection(t, db)
	second, secondID := rerunMySQLConnection(t, db)
	firstRepo, secondRepo := NewRepositories(statusTestDB{first}, nil), NewRepositories(statusTestDB{second}, nil)
	attachFirst := strings.HasSuffix(scenario, "attachment-first")
	moving := strings.Contains(scenario, "-move-")
	if moving {
		require.NoError(t, r.JobRun.Create(ctx, &model.JobRun{JobRunID: "source-ordinary-child"}))
	}
	held := make(chan struct{})
	var once atomic.Bool
	const name = "rerun-source-membership-holder"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "PipelineRun" {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		for _, arg := range tx.Statement.Vars {
			if id, ok := arg.(string); ok && id == saved.OriginalRunID && once.CompareAndSwap(false, true) {
				close(held)
				select {
				case <-workers.release:
				case <-ctx.Done():
					tx.AddError(ctx.Err())
				}
			}
		}
	}))
	defer func() { workers.finish(); _ = db.Callback().Query().Remove(name) }()
	attach := func(repos *Repositories) error {
		if moving {
			return repos.JobRun.UpdateByJobRunID(ctx, "source-ordinary-child", map[string]any{"pipeline_run_id": saved.OriginalRunID})
		}
		return repos.JobRun.Create(ctx, &model.JobRun{JobRunID: "source-ordinary-child", PipelineRunID: saved.OriginalRunID})
	}
	confirm := func(repos *Repositories) error {
		_, err := repos.CopilotRerunConfirm(ctx, in, permitRerun, testRerunReview)
		return err
	}
	firstDone := workers.start(func() error {
		if attachFirst {
			return attach(firstRepo)
		}
		return confirm(firstRepo)
	})
	select {
	case <-held:
	case err := <-firstDone:
		t.Fatalf("source operation ended before held boundary: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	secondDone := workers.start(func() error {
		if attachFirst {
			return confirm(secondRepo)
		}
		return attach(secondRepo)
	})
	rerunMySQLWaitEdge(t, db, firstID, secondID, secondDone)
	workers.releaseHolder()
	firstErr, secondErr := awaitRetryMySQL(t, firstDone), awaitRetryMySQL(t, secondDone)
	require.NoError(t, firstErr)
	var targetCount, sourceCount int64
	require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", saved.NewRunID).Count(&targetCount).Error)
	require.NoError(t, db.Model(&model.JobRun{}).Where("pipeline_run_id = ?", saved.OriginalRunID).Count(&sourceCount).Error)
	require.EqualValues(t, 2, sourceCount)
	var intent model.CopilotRerunIntent
	require.NoError(t, db.Where("intent_id = ?", saved.IntentID).First(&intent).Error)
	if attachFirst {
		require.ErrorIs(t, secondErr, ErrCopilotRerunUnsupported, "the first source snapshot must see the attachment committed after its locator read")
		require.Zero(t, targetCount)
		require.Equal(t, CopilotRerunPrepared, intent.State)
	} else {
		require.NoError(t, secondErr)
		require.EqualValues(t, 1, targetCount)
		require.Equal(t, CopilotRerunConfirmed, intent.State)
	}
}

func TestCopilotRerunMySQLWorkersJoinBeforeCallbackRemoval(t *testing.T) {
	_, db, _, _ := sourceTestRepository(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	workers := newRerunMySQLWorkers(ctx)
	held := make(chan struct{})
	var active atomic.Int32
	const name = "rerun-cleanup-joined-callback"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, func(*gorm.DB) {
		active.Add(1)
		defer active.Add(-1)
		close(held)
		<-workers.release
	}))
	defer func() { workers.finish(); _ = db.Callback().Query().Remove(name) }()
	failed := workers.start(func() error { return fmt.Errorf("synthetic first-operation failure") })
	require.Error(t, awaitRetryMySQL(t, failed), "an error consumed by the observer must not consume the worker join")
	pending := workers.start(func() error { return db.WithContext(workers.ctx).Find(&[]model.PipelineRun{}).Error })
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal("callback was not reached")
	}
	workers.finish()
	require.Zero(t, active.Load())
	select {
	case <-pending:
	default:
		t.Fatal("worker still using callbacks after join")
	}
	require.NoError(t, db.Callback().Query().Remove(name))
}
