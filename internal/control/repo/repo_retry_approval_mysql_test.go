package repo

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	drivermysql "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// 仅显式选择的本机专用空数据库可执行，默认不访问任何 MySQL 服务
// ARCENTRA_RETRY_APPROVAL_MYSQL_DSN 的数据库名必须以 arcentra_retry_approval_test 开头
func openRetryApprovalMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	required := os.Getenv("ARCENTRA_REQUIRE_RETRY_APPROVAL_MYSQL")
	if required != "" && required != "1" {
		t.Fatal("ARCENTRA_REQUIRE_RETRY_APPROVAL_MYSQL must be 1 or unset")
	}
	dsn := os.Getenv("ARCENTRA_RETRY_APPROVAL_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required MySQL approval acceptance DSN is missing")
		}
		t.Skip("set a local disposable ARCENTRA_RETRY_APPROVAL_MYSQL_DSN to run real MySQL acceptance")
	}
	cfg, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid MySQL acceptance DSN")
	}
	if !strings.HasPrefix(cfg.DBName, "arcentra_retry_approval_test") {
		t.Fatal("MySQL acceptance requires its dedicated disposable database name")
	}
	if cfg.Net != "unix" {
		host, _, err := net.SplitHostPort(cfg.Addr)
		if err != nil || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
			t.Fatal("MySQL acceptance only permits explicit loopback or unix socket connections")
		}
	}
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 15 * time.Second
	cfg.WriteTimeout = 15 * time.Second
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["transaction_isolation"] = "'REPEATABLE-READ'"
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("local MySQL acceptance connection failed:", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = pool.Close() })
	return db
}
func retryApprovalTestColumns(db *gorm.DB, entity any) []string {
	stmt := &gorm.Statement{DB: db}
	if stmt.Parse(entity) != nil {
		return nil
	}
	columns := make([]string, 0, len(stmt.Schema.Fields))
	for _, field := range stmt.Schema.Fields {
		if field.DBName != "" {
			columns = append(columns, field.DBName)
		}
	}
	return columns
}
func mysqlRetryApprovalFixture(t *testing.T) (*generationFixture, *gorm.DB) {
	t.Helper()
	db := openRetryApprovalMySQL(t)
	other := openRetryApprovalMySQL(t)
	source := gatedPinFixture(t)
	entities := []any{&model.Agent{}, &model.PipelineRun{}, &model.Pipeline{}, &model.Project{}, &model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}, &model.JobRunGeneration{}, &model.User{}, &model.TeamMember{}, &model.Team{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Role{}, &model.RoleGrant{}}
	for _, entity := range entities {
		if db.Migrator().HasTable(entity) {
			var n int64
			if err := db.WithContext(source.ctx).Model(entity).Count(&n).Error; err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatal("MySQL acceptance refuses a database containing existing rows")
			}
		}
	}
	if err := db.WithContext(source.ctx).AutoMigrate(entities...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := len(entities) - 1; i >= 0; i-- {
			if err := db.WithContext(context.Background()).Where("1 = 1").Delete(entities[i]).Error; err != nil {
				t.Error("acceptance fixture cleanup:", err)
			}
		}
	})
	for _, entity := range entities {
		if !source.db.Migrator().HasTable(entity) {
			continue
		}
		rows := reflect.New(reflect.SliceOf(reflect.TypeOf(entity).Elem())).Interface()
		if err := source.db.WithContext(source.ctx).Select(retryApprovalTestColumns(source.db, entity)).Find(rows).Error; err != nil {
			t.Fatal(err)
		}
		if reflect.ValueOf(rows).Elem().Len() != 0 {
			if err := db.WithContext(source.ctx).Create(rows).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	f := &generationFixture{ctx: ctx, db: db, jobs: &JobRunRepo{IDatabase: statusTestDB{db}}, steps: &StepRunRepo{IDatabase: statusTestDB{db}}, identity: source.identity, manifest: source.manifest, admit: source.admit, plan: source.plan}
	return f, other
}
func mysqlRetryApprovalAuthority(t *testing.T, f *generationFixture, other *gorm.DB) {
	t.Helper()
	installRetryApprovalTestAuthority(t, f, nil)
	authorize, err := retryApprovalAuthorizer(f.db)
	if err != nil {
		t.Fatal(err)
	}
	repos := &Repositories{database: statusTestDB{other}}
	if err := SetRetryApprovalAuthorizer(repos, authorize); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetRetryApprovalAuthorizer(repos, nil) })
}
func awaitRetryMySQL(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("MySQL acceptance operation did not finish")
		return context.DeadlineExceeded
	}
}
func assertRetryMySQLWaiting(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatal("expected operation to wait for held MySQL row lock:", err)
	case <-time.After(75 * time.Millisecond):
	}
}

func TestRetryApprovalMySQLUniquePinQuorumReopenAndACK(t *testing.T) {
	f, other := mysqlRetryApprovalFixture(t)
	// 两位不同候选人的唯一投票在独立连接上聚合为一个完整法定人数
	var run model.PipelineRun
	if err := f.db.WithContext(f.ctx).Select("run_id", "retry_approval_context").Where("run_id = ?", "run").First(&run).Error; err != nil {
		t.Fatal(err)
	}
	c, err := retryapproval.ParseRunContext(run.RetryApprovalContext)
	if err != nil {
		t.Fatal(err)
	}
	c.Policy.Candidates = []string{"voter", "voter2"}
	c.Policy.RequiredApproverCount = 2
	c.Policy.Mode = "all"
	c.Policy.Fingerprint, _ = c.Policy.Digest()
	body, _ := c.Canonical()
	digest, _ := c.Digest()
	if err := f.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Where("run_id = ?", "run").Updates(map[string]any{"retry_approval_context": body, "retry_approval_context_digest": digest}).Error; err != nil {
		t.Fatal(err)
	}
	g := f.admission(t)
	jobs2 := &JobRunRepo{IDatabase: statusTestDB{other}}
	pin1, pin2 := make(chan error, 1), make(chan error, 1)
	safe.Go(func() { _, err := f.jobs.GetOrRecordJobInputs(f.ctx, f.identity, f.manifest); pin1 <- err })
	safe.Go(func() { _, err := jobs2.GetOrRecordJobInputs(f.ctx, f.identity, f.manifest); pin2 <- err })
	if err := awaitRetryMySQL(t, pin1); err != nil {
		t.Fatal(err)
	}
	if err := awaitRetryMySQL(t, pin2); err != nil {
		t.Fatal(err)
	}
	mysqlRetryApprovalAuthority(t, f, other)
	if err := f.db.WithContext(f.ctx).Create(&model.User{UserID: "voter2", IsEnabled: 1}).Error; err != nil {
		t.Fatal(err)
	}
	r, a := retryApprovalTestRow(t, f)
	r2 := &ApprovalRepo{IDatabase: statusTestDB{other}}
	vote1, vote2 := make(chan error, 1), make(chan error, 1)
	safe.Go(func() { _, err := r.Decide(f.ctx, a.ApprovalID, "voter", "approve", ""); vote1 <- err })
	safe.Go(func() { _, err := r2.Decide(f.ctx, a.ApprovalID, "voter2", "approve", ""); vote2 <- err })
	if err := awaitRetryMySQL(t, vote1); err != nil {
		t.Fatal(err)
	}
	if err := awaitRetryMySQL(t, vote2); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetByApprovalID(f.ctx, a.ApprovalID)
	if err != nil || got.Status != model.ApprovalStatusApproved || got.ApprovedCount != 2 {
		t.Fatal("concurrent quorum failed", err)
	}
	request := retryApprovalTestRequest(t, f, g, "admit")
	admitted, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := jobs2.ReconcileJobRetryApproval(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	x, _ := admitted.Admission.Canonical()
	y, _ := reopened.Admission.Canonical()
	if x != y {
		t.Fatal("connection reopen renewed startup receipt")
	}
	if _, err := r2.Decide(f.ctx, a.ApprovalID, "voter", "approve", "lost ACK"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Decide(f.ctx, a.ApprovalID, "voter", "reject", ""); err == nil {
		t.Fatal("opposing vote accepted")
	}
	var approvals, votes, generations int64
	f.db.WithContext(f.ctx).Model(&model.ApprovalRequest{}).Count(&approvals)
	f.db.WithContext(f.ctx).Model(&model.ApprovalDecision{}).Count(&votes)
	f.db.WithContext(f.ctx).Model(&model.JobRunGeneration{}).Count(&generations)
	if approvals != 1 || votes != 2 || generations != 1 {
		t.Fatalf("replay duplicated durable rows: %d %d %d", approvals, votes, generations)
	}
}

func TestRetryApprovalMySQLVoteVersusCancellation(t *testing.T) {
	f, other := mysqlRetryApprovalFixture(t)
	g := f.admission(t)
	f.pin(t)
	mysqlRetryApprovalAuthority(t, f, other)
	r, a := retryApprovalTestRow(t, f)
	jobs2 := &JobRunRepo{IDatabase: statusTestDB{other}}
	vote, cancel := make(chan error, 1), make(chan error, 1)
	safe.Go(func() { _, err := r.Decide(f.ctx, a.ApprovalID, "voter", "approve", ""); vote <- err })
	safe.Go(func() {
		cancel <- jobs2.CancelAgentJobIfCurrent(f.ctx, "job", model.JobRunStatusRunning, map[string]any{"status": model.JobRunStatusCancelled})
	})
	voteErr := awaitRetryMySQL(t, vote)
	if err := awaitRetryMySQL(t, cancel); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetByApprovalID(f.ctx, a.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.ApprovalStatusCanceled && got.Status != model.ApprovalStatusApproved {
		t.Fatal("race left invalid verdict")
	}
	if voteErr == nil && got.Status != model.ApprovalStatusApproved {
		t.Fatal("committed approval rewritten by cancellation")
	}
	if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f, g, "admit")); err == nil {
		t.Fatal("cancellation lost to startup")
	}
}

func TestRetryApprovalMySQLExpiryAcrossHeldJobAndApprovalLocks(t *testing.T) {
	for _, target := range []string{"job", "approval"} {
		for _, operation := range []string{"vote", "startup"} {
			t.Run(target+"/"+operation, func(t *testing.T) {
				f, other := mysqlRetryApprovalFixture(t)
				g := f.admission(t)
				f.pin(t)
				mysqlRetryApprovalAuthority(t, f, other)
				if operation == "startup" {
					approveRetryApproval(t, f)
				}
				setRetryApprovalTestExpiry(t, f, time.Now().Add(350*time.Millisecond))
				r, a := retryApprovalTestRow(t, f)
				request := retryApprovalTestRequest(t, f, g, "admit")
				holder := other.WithContext(f.ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
				if holder.Error != nil {
					t.Fatal(holder.Error)
				}
				defer holder.Rollback()
				if target == "job" {
					var row model.JobRun
					if err := holder.Clauses(clause.Locking{Strength: "UPDATE"}).Select("job_run_id").Where("job_run_id = ?", "job").First(&row).Error; err != nil {
						t.Fatal(err)
					}
				} else {
					var row model.ApprovalRequest
					if err := holder.Clauses(clause.Locking{Strength: "UPDATE"}).Select("approval_id").Where("approval_id = ?", a.ApprovalID).First(&row).Error; err != nil {
						t.Fatal(err)
					}
				}
				done := make(chan error, 1)
				safe.Go(func() {
					if operation == "vote" {
						_, err := r.Decide(f.ctx, a.ApprovalID, "voter", "approve", "")
						done <- err
					} else {
						_, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request)
						done <- err
					}
				})
				assertRetryMySQLWaiting(t, done)
				time.Sleep(time.Until(*a.ExpiresAt) + 30*time.Millisecond)
				if err := holder.Commit().Error; err != nil {
					t.Fatal(err)
				}
				if err := awaitRetryMySQL(t, done); err == nil {
					t.Fatal("late operation committed after held lock")
				}
				if operation == "vote" {
					var n int64
					f.db.WithContext(f.ctx).Model(&model.ApprovalDecision{}).Count(&n)
					if n != 0 {
						t.Fatal("late vote retained")
					}
				} else {
					var row model.JobRunGeneration
					f.db.WithContext(f.ctx).Select(generationColumns).Where("generation_id = ?", g.GenerationID).First(&row)
					if row.RetryApprovalAdmission != "" {
						t.Fatal("late admission receipt retained")
					}
				}
			})
		}
	}
}

func TestRetryApprovalMySQLCurrentReadsDefeatRepeatableReadSnapshot(t *testing.T) {
	f, other := mysqlRetryApprovalFixture(t)
	f.admission(t)
	f.pin(t)
	mysqlRetryApprovalAuthority(t, f, other)
	approveRetryApproval(t, f)
	err := f.db.WithContext(f.ctx).Transaction(func(tx *gorm.DB) error {
		var stale model.User
		if err := tx.Select("user_id", "is_enabled").Where("user_id = ?", "voter").First(&stale).Error; err != nil {
			return err
		}
		if stale.IsEnabled != 1 {
			return fmt.Errorf("fixture voter disabled")
		}
		if err := other.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", "voter").Update("is_enabled", 0).Error; err != nil {
			return err
		}
		var job model.JobRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(jobSnapshotColumns).Where("job_run_id = ?", "job").First(&job).Error; err != nil {
			return err
		}
		if err := validateRetryApprovalExecution(f.ctx, tx, &job, false); err == nil {
			return fmt.Errorf("stale RR permission snapshot authorized execution")
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetryApprovalMySQLRevocationLinearizesBeforeAdmission(t *testing.T) {
	f, other := mysqlRetryApprovalFixture(t)
	g := f.admission(t)
	f.pin(t)
	mysqlRetryApprovalAuthority(t, f, other)
	approveRetryApproval(t, f)
	request := retryApprovalTestRequest(t, f, g, "admit")
	holder := other.WithContext(f.ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if holder.Error != nil {
		t.Fatal(holder.Error)
	}
	defer holder.Rollback()
	if err := holder.Model(&model.User{}).Where("user_id = ?", "voter").Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	safe.Go(func() { _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request); done <- err })
	assertRetryMySQLWaiting(t, done)
	if err := holder.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := awaitRetryMySQL(t, done); err == nil {
		t.Fatal("admission ignored preceding committed revocation")
	}
	var row model.JobRunGeneration
	f.db.WithContext(f.ctx).Select(generationColumns).Where("generation_id = ?", g.GenerationID).First(&row)
	if row.RetryApprovalAdmission != "" {
		t.Fatal("revoked admission left a receipt")
	}
}

func TestRetryApprovalMySQLAdmissionHoldsAuthorityUntilCommit(t *testing.T) {
	f, other := mysqlRetryApprovalFixture(t)
	g := f.admission(t)
	f.pin(t)
	mysqlRetryApprovalAuthority(t, f, other)
	approveRetryApproval(t, f)
	request := retryApprovalTestRequest(t, f, g, "admit")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	name := "test:hold-authorized-admission"
	if err := f.db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "job_run_generation" {
			once.Do(func() { close(entered) })
			select {
			case <-release:
			case <-f.ctx.Done():
				tx.AddError(f.ctx.Err())
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Update().Remove(name) })
	done := make(chan error, 1)
	safe.Go(func() { _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request); done <- err })
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal("startup never reached commit boundary")
	}
	revoked := make(chan error, 1)
	safe.Go(func() {
		revoked <- other.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", "voter").Update("is_enabled", 0).Error
	})
	assertRetryMySQLWaiting(t, revoked)
	close(release)
	if err := awaitRetryMySQL(t, done); err != nil {
		t.Fatal(err)
	}
	if err := awaitRetryMySQL(t, revoked); err != nil {
		t.Fatal(err)
	}
	if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request); err == nil {
		t.Fatal("later startup ignored committed revocation")
	}
}

func TestRetryApprovalMySQLRollbackLeavesNoVoteOrAdmission(t *testing.T) {
	f, other := mysqlRetryApprovalFixture(t)
	g := f.admission(t)
	f.pin(t)
	mysqlRetryApprovalAuthority(t, f, other)
	r, a := retryApprovalTestRow(t, f)
	name := "test:rollback-vote"
	if err := f.db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "approval_request" {
			if patch, ok := tx.Statement.Dest.(map[string]any); ok && patch["approved_count"] != nil {
				tx.AddError(fmt.Errorf("injected quorum write failure"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Decide(f.ctx, a.ApprovalID, "voter", "approve", ""); err == nil {
		t.Fatal("failed vote committed")
	}
	if err := f.db.Callback().Update().Remove(name); err != nil {
		t.Fatal(err)
	}
	var count int64
	f.db.WithContext(f.ctx).Model(&model.ApprovalDecision{}).Count(&count)
	if count != 0 {
		t.Fatal("rolled back vote retained")
	}
	approveRetryApproval(t, f)
	name = "test:rollback-admission"
	if err := f.db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "job_run_generation" {
			tx.AddError(fmt.Errorf("injected startup write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Update().Remove(name) })
	if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f, g, "admit")); err == nil {
		t.Fatal("failed admission committed")
	}
	var row model.JobRunGeneration
	f.db.WithContext(f.ctx).Select(generationColumns).Where("generation_id = ?", g.GenerationID).First(&row)
	if row.RetryApprovalAdmission != "" {
		t.Fatal("rollback left admission receipt")
	}
}

func mysqlRetryApprovalArtifact(t *testing.T, f *generationFixture, expiry time.Time) {
	t.Helper()
	hash := strings.Repeat("d", 64)
	key := "artifacts/run/producer/producer-step/" + hash + "/result.txt"
	size := int64(42)
	for _, entity := range []any{&model.JobRun{JobRunID: "producer", JobName: "producer", PipelineID: "pipeline", PipelineRunID: "run", Status: model.JobRunStatusSuccess}, &model.StepRun{StepRunID: "producer-step", JobRunID: "producer", PipelineID: "pipeline", PipelineRunID: "run", Status: model.JobRunStatusSuccess}, &model.ProjectArtifact{ArtifactID: "artifact", ProjectID: "project", PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "producer", StepRunID: "producer-step", Name: "result.txt", StorageID: "store", StorageFingerprint: strings.Repeat("b", 64), StoragePath: key, SHA256: hash, Size: size, ExpiresAt: &expiry}} {
		if err := f.db.WithContext(f.ctx).Create(entity).Error; err != nil {
			t.Fatal(err)
		}
	}
	refs := map[string]taskqueue.ArtifactReference{"producer/result.txt": {StorageID: "store", Fingerprint: strings.Repeat("b", 64), SHA256: hash, Size: &size, ExpiresAt: expiry.Unix()}}
	manifest, err := taskqueue.NewJobInputManifest("", map[string]string{"producer/result.txt": key}, refs)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := manifest.Canonical()
	if err := f.db.WithContext(f.ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("input_expectation", encoded).Error; err != nil {
		t.Fatal(err)
	}
	f.manifest = manifest
}

func TestRetryApprovalMySQLArtifactExpiresWhilePermissionLockWaits(t *testing.T) {
	for _, operation := range []string{"vote", "startup", "running", "advance"} {
		t.Run(operation, func(t *testing.T) {
			f, other := mysqlRetryApprovalFixture(t)
			expiry := time.Now().Add(4 * time.Second).Truncate(time.Second)
			mysqlRetryApprovalArtifact(t, f, expiry)
			g := f.admission(t)
			f.pin(t)
			mysqlRetryApprovalAuthority(t, f, other)
			if err := f.db.WithContext(f.ctx).Create(&model.RoleGrant{GrantID: "held-grant", SubjectType: "user", SubjectID: "actor", RoleID: "role", ScopeType: "project", ScopeID: "project", IsEnabled: 1}).Error; err != nil {
				t.Fatal(err)
			}
			if operation != "vote" {
				approveRetryApproval(t, f)
			}
			request := retryApprovalTestRequest(t, f, g, "admit")
			if operation == "running" || operation == "advance" {
				if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request); err != nil {
					t.Fatal(err)
				}
			}
			var advance taskqueue.JobGenerationTransition
			if operation == "advance" {
				f.failedRound(t, g)
				advance = f.advance(t, g)
			}
			original, err := retryApprovalAuthorizer(f.db)
			if err != nil {
				t.Fatal(err)
			}
			repositories := &Repositories{database: statusTestDB{f.db}}
			if err := SetRetryApprovalAuthorizer(repositories, func(ctx context.Context, view RetryApprovalAuthorizationView, evidence *retryapproval.Evidence, actor, action string) error {
				if _, err := view.LiveRepositories().RoleGrant.ListAllEnabled(ctx); err != nil {
					return err
				}
				return original(ctx, view, evidence, actor, action)
			}); err != nil {
				t.Fatal(err)
			}
			holder := other.WithContext(f.ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
			if holder.Error != nil {
				t.Fatal(holder.Error)
			}
			defer holder.Rollback()
			if err := holder.Model(&model.RoleGrant{}).Where("grant_id = ?", "held-grant").Update("granted_by", "lock-holder").Error; err != nil {
				t.Fatal(err)
			}
			r, a := retryApprovalTestRow(t, f)
			done := make(chan error, 1)
			safe.Go(func() {
				switch operation {
				case "vote":
					_, err := r.Decide(f.ctx, a.ApprovalID, "voter", "approve", "")
					done <- err
				case "startup":
					_, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request)
					done <- err
				case "running":
					done <- f.report(t, g, "first-1", model.JobRunStatusRunning, false)
				case "advance":
					_, err := f.jobs.ReconcileJobGeneration(f.ctx, advance)
					done <- err
				}
			})
			assertRetryMySQLWaiting(t, done)
			time.Sleep(time.Until(expiry) + 30*time.Millisecond)
			if err := holder.Commit().Error; err != nil {
				t.Fatal(err)
			}
			if err := awaitRetryMySQL(t, done); err == nil {
				t.Fatal("expired immutable input admitted after permission lock wait")
			}
			if operation == "vote" {
				var n int64
				f.db.WithContext(f.ctx).Model(&model.ApprovalDecision{}).Count(&n)
				if n != 0 {
					t.Fatal("expired-input vote retained")
				}
			}
			if operation == "running" {
				step, err := f.steps.GetByStepRunID(f.ctx, "first-1")
				if err != nil || step.Status == model.JobRunStatusRunning {
					t.Fatal("expired-input RUNNING committed", err)
				}
			}
			if operation == "advance" {
				var n int64
				f.db.WithContext(f.ctx).Model(&model.JobRunGeneration{}).Count(&n)
				if n != 1 {
					t.Fatal("expired-input successor committed")
				}
			}
		})
	}
}
