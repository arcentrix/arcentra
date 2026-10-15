package repo

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// 此测试只在显式提供空白专用数据库时运行，不创建数据库、修改权限或启动 MySQL
func TestStepAttemptMySQLExplicitOptInCASAndCancellation(t *testing.T) {
	if os.Getenv("ARCENTRA_STEP_ATTEMPT_MYSQL_TEST") != "1" {
		t.Skip("explicit disposable MySQL opt-in absent; no MySQL acceptance claim")
	}
	config, err := drivermysql.ParseDSN(os.Getenv("ARCENTRA_STEP_ATTEMPT_MYSQL_DSN"))
	if err != nil || !strings.HasPrefix(config.DBName, "arcentra_step_attempt_test_") || !config.ParseTime {
		t.Fatal("requires parseTime=true and a dedicated database name beginning arcentra_step_attempt_test_")
	}
	config.Timeout, config.ReadTimeout, config.WriteTimeout = 5*time.Second, 10*time.Second, 10*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db, err := gorm.Open(mysql.Open(config.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("cannot open disposable MySQL fixture")
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	tables, err := db.WithContext(ctx).Migrator().GetTables()
	if err != nil {
		t.Fatal("cannot inspect disposable MySQL fixture")
	}
	if len(tables) != 0 {
		t.Fatal("disposable MySQL fixture must have no existing tables; refusing foreign or previous fixture data")
	}
	if err := db.WithContext(ctx).AutoMigrate(&model.Agent{}, &model.Pipeline{}, &model.PipelineRun{}, &model.Project{}, &model.JobRun{}, &model.StepRun{}, &model.JobRunGeneration{}, &model.ProjectArtifact{}); err != nil {
		t.Fatal(err)
	}
	jobs := &JobRunRepo{IDatabase: statusTestDB{db}}
	// 每个 JSON 列采用对应生产形状；步骤创建复用生产规范化，策略不能用空对象替代
	rows := []any{
		&model.Agent{AgentID: "agent", IsEnabled: 1, Labels: datatypes.JSON(`{}`), RuntimeCapabilities: datatypes.JSON(`{}`)},
		&model.PipelineRun{RunID: "run", PipelineID: "pipeline", Status: model.PipelineStatusRunning, Env: "{}"},
		&model.Pipeline{PipelineID: "pipeline", ProjectID: "project", Metadata: "{}"},
		&model.Project{ProjectID: "project", BuildConfig: datatypes.JSON(`{}`), EnvVars: datatypes.JSON(`{}`), Settings: datatypes.JSON(`{}`)},
	}
	for _, row := range rows {
		if err := db.WithContext(ctx).Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	newFixture := func(t *testing.T, jobID string) *stepAttemptFixture {
		t.Helper()
		if err := jobs.Create(ctx, &model.JobRun{JobRunID: jobID, PipelineID: "pipeline", PipelineRunID: "run", Status: 1}); err != nil {
			t.Fatal(err)
		}
		return populateStepAttemptFixtureForJob(t, ctx, db, jobs, "", jobID)
	}
	t.Run("equal_and_conflicting_CAS_current_reports_and_upload_winner", func(t *testing.T) {
		assertStepAttemptCASReportsAndPublication(t, newFixture(t, "cas"))
	})
	for _, cancelFirst := range []bool{true, false} {
		name := "advance_before_cancel"
		if cancelFirst {
			name = "cancel_before_advance"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, name)
			first := f.admitStep(t, 0)
			f.finishStep(t, first, model.JobRunStatusFailed)
			request := f.nextRequest(t, first, 1)
			var admitted *taskqueue.StepAttemptResult
			advance := func(ctx context.Context) error {
				var err error
				admitted, err = jobs.ReconcileStepAttempt(ctx, request)
				return err
			}
			cancelJob := func(ctx context.Context) error {
				return jobs.CancelAgentJobIfCurrent(ctx, f.identity.JobRunID, model.JobRunStatusRunning, map[string]any{"status": model.JobRunStatusCancelled})
			}
			ordered := []func(context.Context) error{advance, cancelJob}
			if cancelFirst {
				ordered[0], ordered[1] = cancelJob, advance
			}
			errs := stepAttemptMySQLOrderedRace(t, f, config.DBName, ordered[0], ordered[1])
			if errs[0] != nil || (cancelFirst && errs[1] == nil) || (!cancelFirst && errs[1] != nil) {
				t.Fatalf("unexpected cancellation/advance ordering: %v", errs)
			}
			old := stepAttemptMySQLReadStep(t, f, first.Scope.StepRunID)
			if old.Status != model.JobRunStatusFailed || old.TerminalReportDigest != request.FailedStepReceiptDigest {
				t.Fatal("cancellation overwrote committed predecessor failure")
			}
			successor := stepAttemptMySQLReadStep(t, f, request.Scope.StepRunID)
			if successor.Status != model.JobRunStatusCancelled || successor.CancellationClaimID != f.identity.ClaimID {
				t.Fatal("cancellation did not close the successor under the original claim")
			}
			if cancelFirst {
				if admitted != nil || !emptyStepAdmission(successor) {
					t.Fatal("cancel-first partially persisted a successor admission")
				}
				if _, err := jobs.ReconcileStepAttempt(f.ctx, request); err == nil {
					t.Fatal("cancel-first replay admitted the successor")
				}
			} else {
				if admitted == nil || admitted.Admission == nil {
					t.Fatal("advance-first lost its admission")
				}
				assertStepAttemptMySQLLedger(t, f, request, admitted.Admission)
				replay, err := jobs.ReconcileStepAttempt(f.ctx, request)
				if err != nil || replay == nil || replay.ExecutionAllowed || !reflect.DeepEqual(replay.Admission, admitted.Admission) {
					t.Fatal("cancellation renewed or removed the winning admission")
				}
				if err := f.reportStep(t, request.Scope.StepRunID, admitted.Admission, model.JobRunStatusRunning, false); err == nil {
					t.Fatal("cancelled winning admission regained RUNNING")
				}
				reportCtx := agentidentity.WithStepAttemptReport(agentidentity.WithGenerationReport(f.ctx, f.generation.GenerationID, f.generation.BindingDigest), admitted.Admission.BindingDigest)
				if err := f.steps.CommitCancellationReceipt(reportCtx, successor, request.Scope.StepClaimID, strings.Repeat("6", 64)); err != nil {
					t.Fatal(err)
				}
				if row := stepAttemptMySQLReadStep(t, f, request.Scope.StepRunID); row.TerminalReportDigest != strings.Repeat("6", 64) || !sameStepAdmission(row, successor) {
					t.Fatal("cancellation receipt changed the winning admission")
				}
			}
			if _, err := jobs.ReconcileStepAttempt(f.ctx, f.request(t, 1)); err == nil {
				t.Fatal("cancelled job admitted a later logical step")
			}
			assertStepAttemptMySQLPublication(t, f, model.JobRunStatusCancelled, nil)
		})
	}
	for _, cancelFirst := range []bool{true, false} {
		name := "publication_before_cancel"
		if cancelFirst {
			name = "cancel_before_publication"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, name)
			for index, planned := range f.plan.Steps {
				f.finishStep(t, f.admitStep(t, index), model.JobRunStatusSuccess)
				for _, id := range planned.AttemptRunIDs[1:] {
					if err := f.reportStep(t, id, nil, model.JobRunStatusSkipped, false); err != nil {
						t.Fatal(err)
					}
				}
			}
			job := stepAttemptMySQLReadJob(t, f)
			artifact := stepAttemptMySQLArtifact(t, f, f.plan.Steps[2].AttemptRunIDs[0])
			publish := func(ctx context.Context) error { return stepAttemptMySQLPublish(f, ctx, job, artifact) }
			cancelJob := func(ctx context.Context) error {
				return jobs.CancelAgentJobIfCurrent(ctx, f.identity.JobRunID, model.JobRunStatusRunning, map[string]any{"status": model.JobRunStatusCancelled})
			}
			ordered := []func(context.Context) error{publish, cancelJob}
			if cancelFirst {
				ordered[0], ordered[1] = cancelJob, publish
			}
			errs := stepAttemptMySQLOrderedRace(t, f, config.DBName, ordered[0], ordered[1])
			if errs[0] != nil || errs[1] == nil {
				t.Fatalf("publication/cancellation must have exactly the forced first winner: %v", errs)
			}
			if cancelFirst {
				assertStepAttemptMySQLPublication(t, f, model.JobRunStatusCancelled, nil)
			} else {
				assertStepAttemptMySQLPublication(t, f, model.JobRunStatusSuccess, artifact)
			}
		})
	}
	t.Log("disposable MySQL protocol fixture passed; retained scoped synthetic rows for audit; no container runtime acceptance")
}

// SQLite 覆盖同一场景的夹具构造和断言，实际 MySQL 锁语义仍须显式验收
func assertStepAttemptCASReportsAndPublication(t *testing.T, f *stepAttemptFixture) {
	t.Helper()
	jobs := f.jobs
	var err error
	first := f.admitStep(t, 0)
	f.finishStep(t, first, model.JobRunStatusFailed)
	request := f.nextRequest(t, first, 1)
	equal, errs := stepAttemptMySQLRunPair(f.ctx, jobs, request, request)
	if errs[0] != nil || errs[1] != nil || equal[0] == nil || equal[1] == nil || !equal[0].ExecutionAllowed || !equal[1].ExecutionAllowed || !reflect.DeepEqual(equal[0].Admission, equal[1].Admission) {
		t.Fatalf("MySQL equal CAS did not preserve one executable response: %v", errs)
	}
	second := equal[0].Admission
	assertStepAttemptMySQLLedger(t, f, request, second)
	f.finishStep(t, second, model.JobRunStatusFailed)
	thirdRequest := f.nextRequest(t, second, 2)
	conflict := thirdRequest
	boundary := *thirdRequest.Boundary
	boundary.BoundaryNonce = uuid.NewString()
	conflict.Boundary = &boundary
	conflict.RequestDigest, err = conflict.Digest()
	if err != nil {
		t.Fatal(err)
	}
	competing, errs := stepAttemptMySQLRunPair(f.ctx, jobs, thirdRequest, conflict)
	winner := -1
	for i, err := range errs {
		if err == nil {
			if winner >= 0 || competing[i] == nil || !competing[i].ExecutionAllowed {
				t.Fatal("MySQL conflicting CAS did not produce exactly one executable winner")
			}
			winner = i
		}
	}
	if winner < 0 {
		t.Fatalf("MySQL conflicting CAS had no winner: %v", errs)
	}
	third := competing[winner].Admission
	selected := []taskqueue.StepAttemptTransition{thirdRequest, conflict}[winner]
	assertStepAttemptMySQLLedger(t, f, selected, third)
	if third.Scope.GenerationID != first.Scope.GenerationID || third.Scope.RuntimeClaimID != first.Scope.RuntimeClaimID || third.Scope.Session != first.Scope.Session {
		t.Fatal("MySQL retry replaced the original generation or session")
	}
	for _, stale := range []*taskqueue.StepAttemptAdmission{first, second} {
		if err := f.reportStep(t, stale.Scope.StepRunID, stale, model.JobRunStatusRunning, false); err == nil {
			t.Fatal("historical attempt regained RUNNING")
		}
		if err := f.reportStep(t, third.Scope.StepRunID, stale, model.JobRunStatusRunning, false); err == nil {
			t.Fatal("old admission started current attempt")
		}
	}
	untouched := stepAttemptMySQLReadStep(t, f, third.Scope.StepRunID)
	if untouched.Status != model.JobRunStatusQueued || untouched.ClaimID != "" || untouched.TerminalReportDigest != "" {
		t.Fatal("rejected stale report changed current attempt")
	}
	f.finishStep(t, third, model.JobRunStatusSuccess)
	current := stepAttemptMySQLReadStep(t, f, third.Scope.StepRunID)
	if current.Status != model.JobRunStatusSuccess || current.ClaimID != third.Scope.StepClaimID || current.GenerationID != third.Scope.GenerationID || current.GenerationBindingDigest != third.Scope.GenerationBindingDigest || current.TerminalReportDigest != strings.Repeat("4", 64) || current.RetryableShellExit {
		t.Fatal("current successful receipt was not durably bound to its exact admission")
	}
	for index := 1; index < len(f.plan.Steps); index++ {
		f.finishStep(t, f.admitStep(t, index), model.JobRunStatusSuccess)
		for _, id := range f.plan.Steps[index].AttemptRunIDs[1:] {
			if err := f.reportStep(t, id, nil, model.JobRunStatusSkipped, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	job := stepAttemptMySQLReadJob(t, f)
	for _, id := range []string{first.Scope.StepRunID, second.Scope.StepRunID, third.Scope.StepRunID, f.plan.Steps[2].AttemptRunIDs[1], f.plan.Steps[2].AttemptRunIDs[2]} {
		artifact := stepAttemptMySQLArtifact(t, f, id)
		if err := stepAttemptMySQLPublish(f, f.ctx, job, artifact); err == nil {
			t.Fatalf("non-upload, stale or future artifact winner %s accepted", id)
		}
		assertStepAttemptMySQLPublication(t, f, model.JobRunStatusRunning, nil)
	}
	artifact := stepAttemptMySQLArtifact(t, f, f.plan.Steps[2].AttemptRunIDs[0])
	if err := NewArtifactRepo(statusTestDB{f.db}).Upsert(f.ctx, artifact); err == nil {
		t.Fatal("generic artifact shortcut bypassed final publication")
	}
	if err := stepAttemptMySQLPublish(f, f.ctx, job, artifact); err != nil {
		t.Fatal(err)
	}
	assertStepAttemptMySQLPublication(t, f, model.JobRunStatusSuccess, artifact)
	replay, err := jobs.ReconcileStepAttempt(f.ctx, selected)
	if err != nil || replay == nil || replay.ExecutionAllowed || !reflect.DeepEqual(replay.Admission, third) {
		t.Fatal("completed job renewed or replaced the current admission")
	}
}

func stepAttemptMySQLRunPair(ctx context.Context, jobs *JobRunRepo, left, right taskqueue.StepAttemptTransition) ([]*taskqueue.StepAttemptResult, []error) {
	results := make([]*taskqueue.StepAttemptResult, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index, request := range []taskqueue.StepAttemptTransition{left, right} {
		wait.Add(1)
		safe.Go(func() {
			defer wait.Done()
			<-start
			results[index], errs[index] = jobs.ReconcileStepAttempt(ctx, request)
		})
	}
	close(start)
	wait.Wait()
	return results, errs
}

type stepAttemptMySQLOrderKey struct{}

type stepAttemptMySQLHeldLock struct {
	EngineLockID string
	IndexName    string
	LockData     *string
}

type stepAttemptMySQLLockOwner struct {
	threadID uint64
	lockIDs  []string
	err      error
}

func stepAttemptMySQLMetadataRead(ctx context.Context, tx *gorm.DB) *gorm.DB {
	return tx.Session(&gorm.Session{NewDB: true, Context: ctx})
}

func stepAttemptMySQLConnectionThread(ctx context.Context, tx *gorm.DB) (uint64, error) {
	var row struct{ ThreadID uint64 }
	err := stepAttemptMySQLMetadataRead(ctx, tx).
		Table("performance_schema.threads").Select("THREAD_ID AS thread_id").
		Where("PROCESSLIST_ID = CONNECTION_ID()").Take(&row).Error
	if err == nil && row.ThreadID == 0 {
		err = fmt.Errorf("current MySQL connection has no performance_schema thread")
	}
	return row.ThreadID, err
}

// 唯一已锁定主键证明这些实际记录锁来自目标行，拒绝混入其他行或索引的锁
func stepAttemptMySQLTargetLockIDs(locks []stepAttemptMySQLHeldLock, rowID uint64, jobID string) ([]string, error) {
	if rowID == 0 || len(locks) == 0 || len(locks) > 2 {
		return nil, fmt.Errorf("target row must have a bounded primary/business-key record lock set")
	}
	if jobID == "" || strings.IndexFunc(jobID, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) >= 0 {
		return nil, fmt.Errorf("record-lock proof requires an unambiguous synthetic business key")
	}
	seen, indexes := map[string]bool{}, map[string]bool{}
	ids := make([]string, 0, len(locks))
	for _, lock := range locks {
		if lock.EngineLockID == "" || seen[lock.EngineLockID] || indexes[lock.IndexName] || lock.LockData == nil || *lock.LockData == "" {
			return nil, fmt.Errorf("target row lock identity is missing, duplicate or opaque")
		}
		switch lock.IndexName {
		case "PRIMARY":
			if *lock.LockData != strconv.FormatUint(rowID, 10) {
				return nil, fmt.Errorf("held primary record lock belongs to another job row")
			}
		case "uk_job_run_business_id":
			key, primary, ok := strings.Cut(*lock.LockData, ", ")
			if !ok || primary != strconv.FormatUint(rowID, 10) || (key != jobID && key != "'"+jobID+"'" && key != strconv.Quote(jobID)) {
				return nil, fmt.Errorf("held business-key record lock does not identify the exact target job and primary key: %q", *lock.LockData)
			}
		default:
			return nil, fmt.Errorf("unexpected target job record lock index")
		}
		seen[lock.EngineLockID], indexes[lock.IndexName] = true, true
		ids = append(ids, lock.EngineLockID)
	}
	if !indexes["PRIMARY"] {
		return nil, fmt.Errorf("exact target primary record lock was not observed")
	}
	return ids, nil
}

func stepAttemptMySQLLockWaitProbe(ctx context.Context, db *gorm.DB, schema string, requesting, blocking uint64, targetLockIDs []string) *gorm.DB {
	return db.WithContext(ctx).Table("performance_schema.data_lock_waits AS waits").
		Select("waits.REQUESTING_ENGINE_LOCK_ID AS requesting_lock_id").
		Joins("JOIN performance_schema.data_locks AS requested ON requested.ENGINE = waits.ENGINE AND requested.ENGINE_LOCK_ID = waits.REQUESTING_ENGINE_LOCK_ID AND requested.THREAD_ID = waits.REQUESTING_THREAD_ID").
		Joins("JOIN performance_schema.data_locks AS blocking ON blocking.ENGINE = waits.ENGINE AND blocking.ENGINE_LOCK_ID = waits.BLOCKING_ENGINE_LOCK_ID AND blocking.THREAD_ID = waits.BLOCKING_THREAD_ID").
		Where("waits.ENGINE = ? AND waits.REQUESTING_THREAD_ID = ? AND waits.BLOCKING_THREAD_ID = ?", "INNODB", requesting, blocking).
		Where("requested.OBJECT_SCHEMA = ? AND requested.OBJECT_NAME = ? AND requested.LOCK_TYPE = ? AND requested.LOCK_STATUS = ?", schema, (model.JobRun{}).TableName(), "RECORD", "WAITING").
		Where("blocking.OBJECT_SCHEMA = ? AND blocking.OBJECT_NAME = ? AND blocking.LOCK_TYPE = ? AND blocking.LOCK_STATUS = ?", schema, (model.JobRun{}).TableName(), "RECORD", "GRANTED").
		Where("requested.LOCK_MODE LIKE ? AND blocking.LOCK_MODE LIKE ?", "X%", "X%").
		Where("requested.INDEX_NAME = blocking.INDEX_NAME AND requested.LOCK_DATA = blocking.LOCK_DATA").
		Where("blocking.ENGINE_LOCK_ID IN ?", targetLockIDs).Limit(1)
}

// 首事务持续持锁，直到服务端确认竞争连接正在等待此事务的同一目标行记录锁
func stepAttemptMySQLOrderedRace(t *testing.T, f *stepAttemptFixture, schema string, first, second func(context.Context) error) []error {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
	defer cancel()
	var target model.JobRun
	if err := f.db.WithContext(ctx).Select("id", "job_run_id").Where("job_run_id = ?", f.identity.JobRunID).First(&target).Error; err != nil || target.ID == 0 || target.JobRunID != f.identity.JobRunID {
		t.Fatalf("cannot identify exact target job row: %v", err)
	}
	locked, competing := make(chan stepAttemptMySQLLockOwner, 1), make(chan stepAttemptMySQLLockOwner, 1)
	release := make(chan struct{})
	var lockOnce, competingOnce, releaseOnce sync.Once
	var wait sync.WaitGroup
	beforeName, afterName := "test:mysql-before-"+uuid.NewString(), "test:mysql-after-"+uuid.NewString()
	isJobLock := func(tx *gorm.DB) bool {
		lock, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
		return tx.Statement.Table == (model.JobRun{}).TableName() && ok && lock.Strength == "UPDATE"
	}
	defer func() {
		releaseOnce.Do(func() { close(release) })
		cancel()
		wait.Wait()
		if err := f.db.Callback().Query().Remove(beforeName); err != nil {
			t.Error(err)
		}
		if err := f.db.Callback().Query().Remove(afterName); err != nil {
			t.Error(err)
		}
	}()
	if err := f.db.Callback().Query().Before("gorm:query").Register(beforeName, func(tx *gorm.DB) {
		if isJobLock(tx) && tx.Statement.Context.Value(stepAttemptMySQLOrderKey{}) == "second" {
			competingOnce.Do(func() {
				threadID, err := stepAttemptMySQLConnectionThread(ctx, tx)
				if err != nil {
					_ = tx.AddError(err)
				}
				competing <- stepAttemptMySQLLockOwner{threadID: threadID, err: err}
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Callback().Query().After("gorm:query").Register(afterName, func(tx *gorm.DB) {
		if tx.Error == nil && isJobLock(tx) && tx.Statement.Context.Value(stepAttemptMySQLOrderKey{}) == "first" {
			lockOnce.Do(func() {
				owner := stepAttemptMySQLLockOwner{}
				job, ok := tx.Statement.Dest.(*model.JobRun)
				if !ok || tx.RowsAffected != 1 || job.JobRunID != target.JobRunID {
					owner.err = fmt.Errorf("first locking read did not select exactly the target job")
				} else {
					owner.threadID, owner.err = stepAttemptMySQLConnectionThread(ctx, tx)
				}
				if owner.err == nil {
					var locks []stepAttemptMySQLHeldLock
					owner.err = stepAttemptMySQLMetadataRead(ctx, tx).
						Table("performance_schema.data_locks").Select("ENGINE_LOCK_ID AS engine_lock_id", "INDEX_NAME AS index_name", "LOCK_DATA AS lock_data").
						Where("ENGINE = ? AND THREAD_ID = ? AND OBJECT_SCHEMA = ? AND OBJECT_NAME = ?", "INNODB", owner.threadID, schema, (model.JobRun{}).TableName()).
						Where("LOCK_TYPE = ? AND LOCK_STATUS = ? AND LOCK_MODE LIKE ?", "RECORD", "GRANTED", "X%").Find(&locks).Error
					if owner.err == nil {
						owner.lockIDs, owner.err = stepAttemptMySQLTargetLockIDs(locks, target.ID, target.JobRunID)
					}
				}
				locked <- owner
				if owner.err != nil {
					_ = tx.AddError(owner.err)
					return
				}
				select {
				case <-release:
				case <-ctx.Done():
					_ = tx.AddError(ctx.Err())
				}
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	errs := make([]error, 2)
	wait.Add(1)
	safe.Go(func() {
		defer wait.Done()
		errs[0] = first(context.WithValue(ctx, stepAttemptMySQLOrderKey{}, "first"))
	})
	var owner, contender stepAttemptMySQLLockOwner
	select {
	case owner = <-locked:
		if owner.err != nil {
			t.Fatalf("cannot inspect first transaction's exact target-row locks: %v", owner.err)
		}
	case <-ctx.Done():
		t.Fatal("first transaction did not acquire the real MySQL parent-row lock")
	}
	wait.Add(1)
	safe.Go(func() {
		defer wait.Done()
		errs[1] = second(context.WithValue(ctx, stepAttemptMySQLOrderKey{}, "second"))
	})
	select {
	case contender = <-competing:
		if contender.err != nil || contender.threadID == 0 || contender.threadID == owner.threadID {
			t.Fatalf("competing transaction lacks a distinct observable connection: %v", contender.err)
		}
	case <-ctx.Done():
		t.Fatal("competing transaction did not reach its parent-row locking query")
	}
	// 轮询只限频；通过条件必须是服务端实测的等待边，而非经过的时间或查询启动信号
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var edges []struct{ RequestingLockID string }
		if err := stepAttemptMySQLLockWaitProbe(ctx, f.db, schema, contender.threadID, owner.threadID, owner.lockIDs).Find(&edges).Error; err != nil {
			t.Fatalf("cannot inspect real MySQL lock waits: %v", err)
		}
		if len(edges) == 1 && edges[0].RequestingLockID != "" {
			t.Logf("observed MySQL waiter thread %d blocked by thread %d on %s.job_run row %d (%s)", contender.threadID, owner.threadID, schema, target.ID, target.JobRunID)
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("server never reported the exact competing connection waiting on the held target job row")
		case <-ticker.C:
		}
	}
	releaseOnce.Do(func() { close(release) })
	wait.Wait()
	return errs
}

func stepAttemptMySQLReadStep(t *testing.T, f *stepAttemptFixture, id string) *model.StepRun {
	t.Helper()
	step, err := f.steps.GetByStepRunID(f.ctx, id)
	if err != nil || step == nil {
		t.Fatalf("read retained MySQL step: %v", err)
	}
	return step
}

func stepAttemptMySQLReadJob(t *testing.T, f *stepAttemptFixture) *model.JobRun {
	t.Helper()
	job, err := f.jobs.GetByJobRunID(f.ctx, f.identity.JobRunID)
	if err != nil || job == nil {
		t.Fatalf("read retained MySQL job: %v", err)
	}
	return job
}

func assertStepAttemptMySQLLedger(t *testing.T, f *stepAttemptFixture, request taskqueue.StepAttemptTransition, admission *taskqueue.StepAttemptAdmission) {
	t.Helper()
	if admission == nil {
		t.Fatal("missing winning admission")
	}
	row := stepAttemptMySQLReadStep(t, f, request.Scope.StepRunID)
	encoded, err := request.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	body, err := admission.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if row.AttemptAdmissionRequest != encoded || row.AttemptAdmissionRequestDigest != request.RequestDigest || row.AttemptAdmission != body {
		t.Fatal("persisted MySQL CAS ledger differs from the exact winning request and response")
	}
}

func stepAttemptMySQLArtifact(t *testing.T, f *stepAttemptFixture, stepID string) *model.ProjectArtifact {
	t.Helper()
	hash := strings.Repeat("b", 64)
	key, err := artifactutil.RemotePath(f.identity.PipelineRunID, f.identity.JobRunID, stepID, hash, "out.txt")
	if err != nil {
		t.Fatal(err)
	}
	return &model.ProjectArtifact{ProjectID: "project", PipelineID: f.identity.PipelineID, PipelineRunID: f.identity.PipelineRunID, JobRunID: f.identity.JobRunID, StepRunID: stepID, Name: "out.txt", StorageType: "s3", StorageID: "storage", StorageFingerprint: strings.Repeat("a", 64), StoragePath: key, SHA256: hash, Size: 1}
}

func stepAttemptMySQLPublish(f *stepAttemptFixture, ctx context.Context, job *model.JobRun, artifact *model.ProjectArtifact) error {
	ctx = agentidentity.WithGenerationReport(ctx, f.generation.GenerationID, f.generation.BindingDigest)
	return f.jobs.PatchReportedStatusAndArtifacts(ctx, job, map[string]any{"status": model.JobRunStatusSuccess, "claim_id": f.identity.ClaimID, "terminal_report_digest": strings.Repeat("4", 64)}, []*model.ProjectArtifact{artifact})
}

func assertStepAttemptMySQLPublication(t *testing.T, f *stepAttemptFixture, status int, artifact *model.ProjectArtifact) {
	t.Helper()
	job := stepAttemptMySQLReadJob(t, f)
	if job.Status != status || job.CurrentGeneration != 1 || job.CurrentGenerationID != f.generation.GenerationID || job.ClaimID != f.identity.ClaimID {
		t.Fatal("publication/cancellation changed job outcome or original generation/claim")
	}
	var stored []model.ProjectArtifact
	if err := f.db.WithContext(f.ctx).Select(artifactColumns).Where("job_run_id = ?", f.identity.JobRunID).Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if artifact == nil {
		if len(stored) != 0 || job.TerminalReportDigest != "" {
			t.Fatal("rejected publication partially committed a job receipt or artifact")
		}
		return
	}
	if len(stored) != 1 || job.TerminalReportDigest != strings.Repeat("4", 64) {
		t.Fatal("atomic final publication did not retain exactly one artifact and the final receipt")
	}
	got := stored[0]
	if got.ArtifactID != artifact.ArtifactID || got.ProjectID != artifact.ProjectID || got.PipelineID != artifact.PipelineID || got.PipelineRunID != artifact.PipelineRunID || got.JobRunID != artifact.JobRunID || got.StepRunID != artifact.StepRunID || got.Name != artifact.Name || got.SHA256 != artifact.SHA256 || got.Size != artifact.Size || got.StorageType != artifact.StorageType || got.StorageID != artifact.StorageID || got.StorageFingerprint != artifact.StorageFingerprint || got.StoragePath != artifact.StoragePath {
		t.Fatal("persisted artifact differs from the exact admitted upload winner and storage binding")
	}
}

// DryRun 禁止连接数据库，仅校验等待边探针的只读语句与绑定范围
func TestStepAttemptMySQLLockWaitProbeIsScopedAndReadOnly(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN: "fixture@tcp(127.0.0.1:1)/arcentra_step_attempt_test_shape", SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	ctx := context.WithValue(context.Background(), stepAttemptMySQLOrderKey{}, "probe-shape")
	// 模拟刚执行过 FOR UPDATE 的事务语句，元数据读不能继承其 SQL、条件或行锁
	var previous model.JobRun
	parent := db.Table("job_run").Clauses(clause.Locking{Strength: "UPDATE"}).Select("job_run_id").Where("job_run_id = ?", "target-job").Find(&previous)
	var thread struct{ ThreadID uint64 }
	metadata := stepAttemptMySQLMetadataRead(ctx, parent).Table("performance_schema.threads").Select("THREAD_ID AS thread_id").Where("PROCESSLIST_ID = CONNECTION_ID()").Take(&thread)
	metadataSQL := metadata.Statement.SQL.String()
	if metadata.Error != nil || strings.Contains(metadataSQL, "job_run") || strings.Contains(metadataSQL, "FOR UPDATE") || !strings.Contains(metadataSQL, "PROCESSLIST_ID = CONNECTION_ID()") || metadata.Statement.Context != ctx || metadata.Statement.ConnPool != parent.Statement.ConnPool {
		t.Fatalf("metadata query must retain the exact connection without inheriting the locking statement: %s (%v)", metadataSQL, metadata.Error)
	}
	schema := "arcentra_step_attempt_test_bound' OR 1=1 --"
	var edges []struct{ RequestingLockID string }
	query := stepAttemptMySQLLockWaitProbe(ctx, db, schema, 202, 101, []string{"held-primary", "held-business"}).Find(&edges)
	if query.Error != nil {
		t.Fatal(query.Error)
	}
	sql := query.Statement.SQL.String()
	for _, required := range []string{
		"SELECT waits.REQUESTING_ENGINE_LOCK_ID AS requesting_lock_id FROM performance_schema.data_lock_waits AS waits",
		"JOIN performance_schema.data_locks AS requested", "JOIN performance_schema.data_locks AS blocking",
		"requested.ENGINE = waits.ENGINE", "blocking.ENGINE = waits.ENGINE",
		"requested.ENGINE_LOCK_ID = waits.REQUESTING_ENGINE_LOCK_ID", "blocking.ENGINE_LOCK_ID = waits.BLOCKING_ENGINE_LOCK_ID",
		"requested.THREAD_ID = waits.REQUESTING_THREAD_ID", "blocking.THREAD_ID = waits.BLOCKING_THREAD_ID",
		"waits.ENGINE = ? AND waits.REQUESTING_THREAD_ID = ? AND waits.BLOCKING_THREAD_ID = ?",
		"requested.OBJECT_SCHEMA = ? AND requested.OBJECT_NAME = ? AND requested.LOCK_TYPE = ? AND requested.LOCK_STATUS = ?",
		"blocking.OBJECT_SCHEMA = ? AND blocking.OBJECT_NAME = ? AND blocking.LOCK_TYPE = ? AND blocking.LOCK_STATUS = ?",
		"requested.LOCK_MODE LIKE ? AND blocking.LOCK_MODE LIKE ?",
		"requested.INDEX_NAME = blocking.INDEX_NAME AND requested.LOCK_DATA = blocking.LOCK_DATA",
		"blocking.ENGINE_LOCK_ID IN (?,?)", "LIMIT ?",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("lock wait probe omitted required scope %q: %s", required, sql)
		}
	}
	if !strings.HasPrefix(sql, "SELECT ") || strings.Contains(sql, schema) || strings.Contains(sql, "FOR UPDATE") || query.Statement.Context != ctx {
		t.Fatal("wait probe must be context-bound, read-only, unlocked and parameterized")
	}
	want := []any{"INNODB", uint64(202), uint64(101), schema, "job_run", "RECORD", "WAITING", schema, "job_run", "RECORD", "GRANTED", "X%", "X%", "held-primary", "held-business", 1}
	if !reflect.DeepEqual(query.Statement.Vars, want) {
		t.Fatalf("wait probe parameters changed: %#v", query.Statement.Vars)
	}
}

func TestStepAttemptMySQLTargetLockProofRejectsForeignOrOpaqueRows(t *testing.T) {
	data := func(value string) *string { return &value }
	primary := stepAttemptMySQLHeldLock{EngineLockID: "primary-lock", IndexName: "PRIMARY", LockData: data("42")}
	business := stepAttemptMySQLHeldLock{EngineLockID: "business-lock", IndexName: "uk_job_run_business_id", LockData: data("target-job, 42")}
	for _, locks := range [][]stepAttemptMySQLHeldLock{
		{primary}, {primary, business},
		{primary, {EngineLockID: "quoted-business", IndexName: business.IndexName, LockData: data("'target-job', 42")}},
		{primary, {EngineLockID: "double-quoted-business", IndexName: business.IndexName, LockData: data(`"target-job", 42`)}},
	} {
		if ids, err := stepAttemptMySQLTargetLockIDs(locks, 42, "target-job"); err != nil || len(ids) != len(locks) {
			t.Fatalf("exact target primary record proof rejected: %v", err)
		}
	}
	for name, locks := range map[string][]stepAttemptMySQLHeldLock{
		"foreign-business":         {primary, {EngineLockID: "foreign-business", IndexName: business.IndexName, LockData: data("other-job, 42")}},
		"foreign-business-primary": {primary, {EngineLockID: "foreign-row", IndexName: business.IndexName, LockData: data("target-job, 43")}},
		"ambiguous-business":       {primary, {EngineLockID: "ambiguous", IndexName: business.IndexName, LockData: data("target-job, 42, 43")}},
		"empty":                    nil,
		"missing-primary":          {business},
		"foreign-primary":          {{EngineLockID: "foreign", IndexName: "PRIMARY", LockData: data("43")}},
		"opaque-primary":           {{EngineLockID: "opaque", IndexName: "PRIMARY"}},
		"empty-id":                 {{IndexName: "PRIMARY", LockData: data("42")}},
		"duplicate-id":             {primary, {EngineLockID: primary.EngineLockID, IndexName: business.IndexName, LockData: business.LockData}},
		"duplicate-index":          {primary, {EngineLockID: "another-primary", IndexName: "PRIMARY", LockData: data("42")}},
		"unexpected-index":         {primary, {EngineLockID: "foreign-index", IndexName: "idx_job_run_pipeline_run", LockData: data("run, 42")}},
		"too-many-records":         {primary, business, {EngineLockID: "extra-row", IndexName: "PRIMARY", LockData: data("43")}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := stepAttemptMySQLTargetLockIDs(locks, 42, "target-job"); err == nil {
				t.Fatal("foreign, unbound or ambiguous row-lock proof accepted")
			}
		})
	}
	if _, err := stepAttemptMySQLTargetLockIDs([]stepAttemptMySQLHeldLock{primary}, 0, "target-job"); err == nil {
		t.Fatal("unknown target primary key accepted")
	}
	for _, jobID := range []string{"", "target, job", "'target-job'", "target\njob"} {
		if _, err := stepAttemptMySQLTargetLockIDs([]stepAttemptMySQLHeldLock{primary}, 42, jobID); err == nil {
			t.Fatal("ambiguous synthetic business key accepted")
		}
	}
}
