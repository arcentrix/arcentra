package repo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 复用独立数据库选择和真实事务，绝不创建服务、授予权限或触碰外部表
func stepRetryApprovalMySQLFixture(t *testing.T, quorum int) (*stepAttemptFixture, string) {
	t.Helper()
	db := openRetryApprovalMySQL(t)
	cfg, err := drivermysql.ParseDSN(os.Getenv("ARCENTRA_RETRY_APPROVAL_MYSQL_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	entities := []any{&model.Agent{}, &model.PipelineRun{}, &model.Pipeline{}, &model.Project{}, &model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}, &model.JobRunGeneration{}, &model.User{}, &model.TeamMember{}, &model.Team{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Role{}, &model.RoleGrant{}}
	allowed := map[string]bool{}
	for _, entity := range entities {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(entity); err != nil {
			t.Fatal(err)
		}
		allowed[stmt.Table] = true
	}
	tables, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if !allowed[table] {
			t.Fatal("refusing foreign table in dedicated approval schema", table)
		}
		var count int64
		if err := db.WithContext(context.Background()).Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("refusing nonempty approval schema", table, err)
		}
	}
	source := stepRetryApprovalSourceFixture(t, quorum)
	if err := db.WithContext(source.ctx).AutoMigrate(entities...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := len(entities) - 1; i >= 0; i-- {
			if err := db.WithContext(context.Background()).Where("1 = 1").Delete(entities[i]).Error; err != nil {
				t.Error(err)
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
		values := reflect.ValueOf(rows).Elem()
		for i := 0; i < values.Len(); i++ {
			row := values.Index(i).Addr().Interface()
			retryApprovalMySQLJSONDefaults(row)
			if err := retryApprovalMySQLValidateJSON(db, row); err != nil {
				t.Fatal(err)
			}
		}
		if values.Len() > 0 {
			if err := db.WithContext(source.ctx).Create(rows).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	base := *source.generationFixture
	base.ctx = ctx
	base.db = db
	base.jobs = &JobRunRepo{IDatabase: statusTestDB{db}}
	base.steps = &StepRunRepo{IDatabase: statusTestDB{db}}
	f := &stepAttemptFixture{generationFixture: &base, generation: source.generation, session: source.session}
	installRetryApprovalTestAuthority(t, &base, nil)
	if quorum == 2 {
		if err := db.WithContext(ctx).Create(&model.User{UserID: "voter2", IsEnabled: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return f, cfg.DBName
}

func stepRetryApprovalSourceFixture(t *testing.T, quorum int) *stepAttemptFixture {
	t.Helper()
	return newStepAttemptFixtureConfigured(t, "", func(f *generationFixture) {
		policy := retryapproval.Policy{ID: "policy", Candidates: []string{"voter"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300}
		if quorum == 2 {
			policy.Candidates = []string{"voter", "voter2"}
			policy.Mode = "all"
			policy.RequiredApproverCount = 2
		}
		configureGatedPinFixturePolicy(t, f, policy)
	})
}

func stepRetryApprovalStartup(t *testing.T, f *stepAttemptFixture) {
	t.Helper()
	if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f.generationFixture, f.generation, "admit")); err != nil {
		t.Fatal(err)
	}
}
func stepRetryApprovalDenied(t *testing.T, err error) {
	t.Helper()
	var mysqlErr *drivermysql.MySQLError
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &mysqlErr) && (mysqlErr.Number == 1205 || mysqlErr.Number == 1213)) {
		t.Fatalf("expected binding/authority denial, not timeout or deadlock: %v", err)
	}
}
func stepRetryApprovalUnadmitted(t *testing.T, f *stepAttemptFixture, r taskqueue.StepAttemptTransition) {
	t.Helper()
	row := stepAttemptMySQLReadStep(t, f, r.Scope.StepRunID)
	if !emptyStepAdmission(row) || row.Status != model.JobRunStatusQueued {
		t.Fatal("denial partially committed step admission/status")
	}
}

func TestStepRetryApprovalMySQLPendingQuorumAndStartup(t *testing.T) {
	f, _ := stepRetryApprovalMySQLFixture(t, 2)
	request := f.request(t, 0)
	r, a := retryApprovalTestRow(t, f.generationFixture)
	if a.BindingKind != retryapproval.FailedStepKind {
		t.Fatal("wrong approval kind", a.BindingKind)
	}
	evidence, err := retryapproval.ParseEvidence(a.EvidenceJSON)
	if err != nil || evidence.RetryPlan.Version != 2 || evidence.RetryPlan.Policy.Mode != "failed_step" {
		t.Fatal("approval lacks canonical full v2 plan", err)
	}
	wantPlan, err := f.plan.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	gotPlan, err := evidence.RetryPlan.Canonical()
	if err != nil || gotPlan != wantPlan {
		t.Fatal("approval substituted full dispatch plan", err)
	}
	for _, voter := range []string{"", "voter"} {
		if voter != "" {
			if _, err := r.Decide(f.ctx, a.ApprovalID, voter, "approve", ""); err != nil {
				t.Fatal(err)
			}
		}
		_, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
		stepRetryApprovalDenied(t, err)
		stepRetryApprovalUnadmitted(t, f, request)
	}
	if _, err := r.Decide(f.ctx, a.ApprovalID, "voter2", "approve", ""); err != nil {
		t.Fatal(err)
	}
	// Quorum alone is not the immutable startup receipt.
	_, err = f.jobs.ReconcileStepAttempt(f.ctx, request)
	stepRetryApprovalDenied(t, err)
	stepRetryApprovalStartup(t, f)
	first := f.admitStep(t, 0)
	f.finishStep(t, first, model.JobRunStatusFailed)
	next := f.nextRequest(t, first, 1)
	equal, errs := stepAttemptMySQLRunPair(f.ctx, f.jobs, next, next)
	if errs[0] != nil || errs[1] != nil || equal[0] == nil || equal[1] == nil || !equal[0].ExecutionAllowed || !reflect.DeepEqual(equal[0].Admission, equal[1].Admission) {
		t.Fatal("equal approved CAS failed", errs)
	}
	assertStepAttemptMySQLLedger(t, f, next, equal[0].Admission)
	if equal[0].Admission.ExpiresUnixMilli > a.ExpiresAt.UnixMilli() || equal[0].Admission.ExpiresUnixMilli > next.Boundary.ExpiresUnixMilli {
		t.Fatal("step admission extended immutable approval or boundary expiry")
	}
	f.finishStep(t, equal[0].Admission, model.JobRunStatusFailed)
	third := f.nextRequest(t, equal[0].Admission, 2)
	changed := third
	boundary := *third.Boundary
	boundary.BoundaryNonce = uuid.NewString()
	changed.Boundary = &boundary
	changed.RequestDigest, _ = changed.Digest()
	competing, errs := stepAttemptMySQLRunPair(f.ctx, f.jobs, third, changed)
	winners := 0
	for i, err := range errs {
		if err == nil {
			winners++
			if competing[i] == nil || !competing[i].ExecutionAllowed {
				t.Fatal("conflicting CAS winner not executable")
			}
			assertStepAttemptMySQLLedger(t, f, []taskqueue.StepAttemptTransition{third, changed}[i], competing[i].Admission)
		} else {
			stepRetryApprovalDenied(t, err)
		}
	}
	if winners != 1 {
		t.Fatal("conflicting approved CAS must have exactly one winner", errs)
	}
	after, err := r.GetByApprovalID(f.ctx, a.ApprovalID)
	if err != nil || after.EvidenceJSON != a.EvidenceJSON || after.EvidenceHash != a.EvidenceHash || !after.ExpiresAt.Equal(*a.ExpiresAt) {
		t.Fatal("step CAS changed immutable approval evidence", err)
	}

}

func TestStepRetryApprovalMySQLRevocationAndHistoricalACK(t *testing.T) {
	for _, subject := range []string{"actor", "voter"} {
		for _, phase := range []string{"first", "successor", "running"} {
			t.Run(subject+"/"+phase, func(t *testing.T) {
				f, _ := stepRetryApprovalMySQLFixture(t, 1)
				approveRetryApproval(t, f.generationFixture)
				stepRetryApprovalStartup(t, f)
				request := f.request(t, 0)
				var first, known *taskqueue.StepAttemptAdmission
				if phase != "first" {
					first = f.admitStep(t, 0)
					f.finishStep(t, first, model.JobRunStatusFailed)
					request = f.nextRequest(t, first, 1)
				}
				if phase == "running" {
					result, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
					if err != nil {
						t.Fatal(err)
					}
					known = result.Admission
				}
				if err := f.db.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", subject).Update("is_enabled", 0).Error; err != nil {
					t.Fatal(err)
				}
				result, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
				if known == nil {
					stepRetryApprovalDenied(t, err)
					stepRetryApprovalUnadmitted(t, f, request)
				} else {
					if err != nil || result == nil || result.ExecutionAllowed || !reflect.DeepEqual(result.Admission, known) {
						t.Fatal("lost CAS ACK changed or renewed execution", err)
					}
					assertStepAttemptMySQLLedger(t, f, request, known)
					stepRetryApprovalDenied(t, f.reportStep(t, known.Scope.StepRunID, known, model.JobRunStatusRunning, false))
				}
				if first != nil {
					before := stepAttemptMySQLReadStep(t, f, first.Scope.StepRunID)
					if err := stepRetryApprovalHistoricalClaim(f, before, first); err != nil {
						t.Fatal("exact terminal ACK lost", err)
					}
					after := stepAttemptMySQLReadStep(t, f, first.Scope.StepRunID)
					if before.TerminalReportDigest != after.TerminalReportDigest || before.AttemptAdmission != after.AttemptAdmission {
						t.Fatal("terminal replay changed original bytes")
					}
					old, err := f.jobs.ReconcileStepAttempt(f.ctx, f.request(t, 0))
					if err != nil || old == nil || old.ExecutionAllowed || !reflect.DeepEqual(old.Admission, first) {
						t.Fatal("historical first ACK changed", err)
					}
				}
			})
		}
	}
}

func TestStepRetryApprovalMySQLCancellationExactLockOrdering(t *testing.T) {
	for _, cancelFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("cancel_first_%t", cancelFirst), func(t *testing.T) {
			f, schema := stepRetryApprovalMySQLFixture(t, 1)
			approveRetryApproval(t, f.generationFixture)
			stepRetryApprovalStartup(t, f)
			first := f.admitStep(t, 0)
			f.finishStep(t, first, model.JobRunStatusFailed)
			request := f.nextRequest(t, first, 1)
			var admitted *taskqueue.StepAttemptResult
			advance := func(ctx context.Context) error {
				var err error
				admitted, err = f.jobs.ReconcileStepAttempt(ctx, request)
				return err
			}
			cancel := func(ctx context.Context) error {
				return f.jobs.CancelAgentJobIfCurrent(ctx, "job", model.JobRunStatusRunning, map[string]any{"status": model.JobRunStatusCancelled})
			}
			operations := []func(context.Context) error{advance, cancel}
			if cancelFirst {
				operations[0], operations[1] = cancel, advance
			}
			errs := stepAttemptMySQLOrderedRace(t, f, schema, operations[0], operations[1])
			if errs[0] != nil {
				t.Fatal(errs[0])
			}
			if cancelFirst {
				stepRetryApprovalDenied(t, errs[1])
			} else if errs[1] != nil {
				t.Fatal(errs[1])
			}
			old := stepAttemptMySQLReadStep(t, f, first.Scope.StepRunID)
			if old.TerminalReportDigest != request.FailedStepReceiptDigest || old.Status != model.JobRunStatusFailed {
				t.Fatal("cancel changed predecessor receipt")
			}
			if cancelFirst {
				if !emptyStepAdmission(stepAttemptMySQLReadStep(t, f, request.Scope.StepRunID)) {
					t.Fatal("cancel-first persisted admission")
				}
			} else {
				replay, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
				if err != nil || replay == nil || replay.ExecutionAllowed || admitted == nil || !reflect.DeepEqual(replay.Admission, admitted.Admission) {
					t.Fatal("cancel lost historical admission", err)
				}
			}
		})
	}
}

func TestStepRetryApprovalMySQLExpiryAfterAdmissionWriteRollsBack(t *testing.T) {
	for _, bound := range []string{"approval", "boundary"} {
		t.Run(bound, func(t *testing.T) {
			f, _ := stepRetryApprovalMySQLFixture(t, 1)
			if bound == "approval" {
				setRetryApprovalTestExpiry(t, f.generationFixture, time.Now().Add(2*time.Second))
			}
			approveRetryApproval(t, f.generationFixture)
			stepRetryApprovalStartup(t, f)
			first := f.admitStep(t, 0)
			f.finishStep(t, first, model.JobRunStatusFailed)
			request := f.nextRequest(t, first, 1)
			_, approval := retryApprovalTestRow(t, f.generationFixture)
			expiry := *approval.ExpiresAt
			if bound == "boundary" {
				expiry = time.Now().Add(500 * time.Millisecond)
				request.Boundary.ExpiresUnixMilli = expiry.UnixMilli()
				request.RequestDigest, _ = request.Digest()
			}
			name := "test:step-approval-expiry-" + uuid.NewString()
			observed := false
			if err := f.db.Callback().Update().After("gorm:update").Register(name, func(tx *gorm.DB) {
				patch, ok := tx.Statement.Dest.(map[string]any)
				if tx.Error == nil && tx.Statement.Table == (model.StepRun{}).TableName() && ok && patch["attempt_admission"] != nil {
					observed = true
					time.Sleep(time.Until(expiry) + 25*time.Millisecond)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.db.Callback().Update().Remove(name) })
			_, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
			stepRetryApprovalDenied(t, err)
			if !observed {
				t.Fatal("operation never reached successful admission write")
			}
			stepRetryApprovalUnadmitted(t, f, request)
			old := stepAttemptMySQLReadStep(t, f, first.Scope.StepRunID)
			if old.TerminalReportDigest != strings.Repeat("5", 64) {
				t.Fatal("rollback changed predecessor receipt")
			}
		})
	}
}

// 等待的成立条件是目标 JobRun 行的真实服务端等待边，延时仅推进到已知截止时间
func TestStepRetryApprovalMySQLAuthorityChangesAcrossExactJobLock(t *testing.T) {
	for _, change := range []string{"requester", "voter", "approval_expiry"} {
		t.Run(change, func(t *testing.T) {
			f, schema := stepRetryApprovalMySQLFixture(t, 1)
			if change == "approval_expiry" {
				setRetryApprovalTestExpiry(t, f.generationFixture, time.Now().Add(2*time.Second))
			}
			approveRetryApproval(t, f.generationFixture)
			stepRetryApprovalStartup(t, f)
			first := f.admitStep(t, 0)
			f.finishStep(t, first, model.JobRunStatusFailed)
			request := f.nextRequest(t, first, 1)
			_, approval := retryApprovalTestRow(t, f.generationFixture)
			mutate := func(ctx context.Context) error {
				return f.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					var job model.JobRun
					if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(jobSnapshotColumns).Where("job_run_id = ?", "job").First(&job).Error; err != nil {
						return err
					}
					if change == "approval_expiry" {
						time.Sleep(time.Until(*approval.ExpiresAt) + 25*time.Millisecond)
						return nil
					}
					subject := "actor"
					if change == "voter" {
						subject = "voter"
					}
					return tx.Model(&model.User{}).Where("user_id = ?", subject).Update("is_enabled", 0).Error
				})
			}
			admit := func(ctx context.Context) error { _, err := f.jobs.ReconcileStepAttempt(ctx, request); return err }
			errs := stepAttemptMySQLOrderedRace(t, f, schema, mutate, admit)
			if errs[0] != nil {
				t.Fatal(errs[0])
			}
			stepRetryApprovalDenied(t, errs[1])
			stepRetryApprovalUnadmitted(t, f, request)
			old := stepAttemptMySQLReadStep(t, f, first.Scope.StepRunID)
			if old.TerminalReportDigest != request.FailedStepReceiptDigest {
				t.Fatal("waiting admission changed failed predecessor")
			}
		})
	}
}

func TestStepRetryApprovalMySQLRevocationBlocksLaterLogicalStep(t *testing.T) {
	f, _ := stepRetryApprovalMySQLFixture(t, 1)
	approveRetryApproval(t, f.generationFixture)
	stepRetryApprovalStartup(t, f)
	first := f.admitStep(t, 0)
	f.finishStep(t, first, model.JobRunStatusSuccess)
	for _, id := range f.plan.Steps[0].AttemptRunIDs[1:] {
		if err := f.reportStep(t, id, nil, model.JobRunStatusSkipped, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", "voter").Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	request := f.request(t, 1)
	_, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
	stepRetryApprovalDenied(t, err)
	stepRetryApprovalUnadmitted(t, f, request)
	before := stepAttemptMySQLReadStep(t, f, first.Scope.StepRunID)
	if err := stepRetryApprovalHistoricalClaim(f, before, first); err != nil {
		t.Fatal("historical success ACK requires no renewed authority", err)
	}
	old := stepAttemptMySQLReadStep(t, f, first.Scope.StepRunID)
	if old.TerminalReportDigest != strings.Repeat("4", 64) {
		t.Fatal("historical success changed")
	}
}

// 服务层先验证完整终态摘要，再调用此生产认领校验；这里证明撤权不取消历史认领
func stepRetryApprovalHistoricalClaim(f *stepAttemptFixture, row *model.StepRun, a *taskqueue.StepAttemptAdmission) error {
	ctx := agentidentity.WithStepAttemptReport(agentidentity.WithGenerationReport(f.ctx, f.generation.GenerationID, f.generation.BindingDigest), a.BindingDigest)
	return f.steps.ValidateRuntimeStepClaim(ctx, row, a.Scope.StepClaimID, row.Status)
}

// 成功生产者的历史回复不得获取共享运行锁，避免与消费者的制品校验形成逆向锁序
func TestStepRetryApprovalMySQLSuccessfulProducerHistoricalReplyLockOrder(t *testing.T) {
	for _, operation := range []string{"admit", "inspect", "approval_observe", "approval_admit", "generation_admit"} {
		t.Run(operation, func(t *testing.T) {
			producer, schema := stepRetryApprovalMySQLFixture(t, 1)
			approveRetryApproval(t, producer.generationFixture)
			stepRetryApprovalStartup(t, producer)
			var upload *taskqueue.StepAttemptAdmission
			for index, step := range producer.plan.Steps {
				a := producer.admitStep(t, index)
				producer.finishStep(t, a, model.JobRunStatusSuccess)
				if index == len(producer.plan.Steps)-1 {
					upload = a
				}
				for _, id := range step.AttemptRunIDs[1:] {
					if err := producer.reportStep(t, id, nil, model.JobRunStatusSkipped, false); err != nil {
						t.Fatal(err)
					}
				}
			}
			artifact := stepAttemptMySQLArtifact(t, producer, upload.Scope.StepRunID)
			if err := stepAttemptMySQLPublish(producer, producer.ctx, stepAttemptMySQLReadJob(t, producer), artifact); err != nil {
				t.Fatal(err)
			}
			beforeJob := stepAttemptMySQLReadJob(t, producer)
			beforeStep := stepAttemptMySQLReadStep(t, producer, upload.Scope.StepRunID)
			approvals, beforeApproval := retryApprovalTestRow(t, producer.generationFixture)
			var beforeGeneration model.JobRunGeneration
			if err := producer.db.WithContext(producer.ctx).Select(retryApprovalTestColumns(producer.db, &model.JobRunGeneration{})).Where("generation_id = ?", producer.generation.GenerationID).First(&beforeGeneration).Error; err != nil {
				t.Fatal(err)
			}

			consumer := stepRetryApprovalConsumerFixture(t, producer, artifact)
			request := producer.request(t, len(producer.plan.Steps)-1)
			if operation == "inspect" {
				request.Operation = operation
			}
			request.RequestDigest, _ = request.Digest()
			consumerRequest := consumer.request(t, 0)
			var historical, fresh *taskqueue.StepAttemptResult
			approvalRequest := retryApprovalTestRequest(t, producer.generationFixture, producer.generation, "admit")
			generationRequest := producer.admit
			replay := func(ctx context.Context) error {
				var err error
				wantDenial := ""
				switch operation {
				case "approval_observe", "approval_admit":
					current := approvalRequest
					current.Operation = strings.TrimPrefix(operation, "approval_")
					_, err = producer.jobs.ReconcileJobRetryApproval(ctx, current)
					wantDenial = "terminal job cannot request retry approval startup"
				case "generation_admit":
					_, err = producer.jobs.ReconcileJobGeneration(ctx, generationRequest)
					wantDenial = "terminal job cannot admit a generation"
				default:
					historical, err = producer.jobs.ReconcileStepAttempt(ctx, request)
				}
				if wantDenial != "" {
					if err == nil || err.Error() != wantDenial {
						return fmt.Errorf("terminal producer must return precise refusal %q, got %v", wantDenial, err)
					}
					return nil
				}
				return err
			}
			admit := func(ctx context.Context) error {
				var err error
				fresh, err = consumer.jobs.ReconcileStepAttempt(ctx, consumerRequest)
				return err
			}
			// 第二事务已经取得共享 run 的 UPDATE 锁才会继续等待成功生产者的精确 job 行
			sawRunLock := false
			var producerLocks []string
			beforeName := "test:producer-lock-footprint-" + uuid.NewString()
			if err := producer.db.Callback().Query().Before("gorm:query").Register(beforeName, func(tx *gorm.DB) {
				lock, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
				if tx.Statement.Context.Value(stepAttemptMySQLOrderKey{}) == "first" && ok && lock.Strength != "" {
					producerLocks = append(producerLocks, tx.Statement.Table+":"+lock.Strength)
					t.Logf("terminal producer query lock footprint: table=%s strength=%s", tx.Statement.Table, lock.Strength)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = producer.db.Callback().Query().Remove(beforeName) })
			name := "test:consumer-run-lock-" + uuid.NewString()
			if err := producer.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
				lock, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
				if tx.Error != nil && tx.Statement.Context.Value(stepAttemptMySQLOrderKey{}) != nil {
					t.Logf("contended query error: owner=%v table=%s lock=%s error=%v", tx.Statement.Context.Value(stepAttemptMySQLOrderKey{}), tx.Statement.Table, lock.Strength, tx.Error)
				}
				if tx.Error == nil && tx.Statement.Context.Value(stepAttemptMySQLOrderKey{}) == "second" && tx.Statement.Table == (model.PipelineRun{}).TableName() && ok && lock.Strength == "UPDATE" {
					run, exact := tx.Statement.Dest.(*model.PipelineRun)
					if !exact || tx.RowsAffected != 1 || run.RunID != producer.identity.PipelineRunID {
						_ = tx.AddError(fmt.Errorf("consumer did not lock the exact shared run"))
						return
					}
					sawRunLock = true
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = producer.db.Callback().Query().Remove(name) })
			// 已有 helper 在释放生产者前要求 performance_schema 的同连接、同表、同记录等待边
			errs := stepAttemptMySQLOrderedRace(t, producer, schema, replay, admit)
			if errs[0] != nil || errs[1] != nil {
				t.Fatal("historical producer reply and consumer admission must both commit, never deadlock/timeout", errs)
			}
			if !sawRunLock {
				t.Fatal("consumer did not hold the shared run lock before producer-row contention")
			}
			if !reflect.DeepEqual(producerLocks, []string{(model.JobRun{}).TableName() + ":UPDATE"}) {
				t.Fatalf("terminal producer must lock only its original parent, never shared generation/step/run/agent rows: %v", producerLocks)
			}

			if (operation == "admit" || operation == "inspect") && (historical == nil || historical.ExecutionAllowed || !reflect.DeepEqual(historical.Admission, upload)) {
				t.Fatal("producer historical reply changed bytes or renewed execution")
			}
			if fresh == nil || !fresh.ExecutionAllowed || fresh.Admission == nil {
				t.Fatal("consumer lost fresh approved admission")
			}
			assertStepAttemptMySQLLedger(t, consumer, consumerRequest, fresh.Admission)
			afterJob := stepAttemptMySQLReadJob(t, producer)
			afterStep := stepAttemptMySQLReadStep(t, producer, upload.Scope.StepRunID)
			if !reflect.DeepEqual(beforeJob, afterJob) || !reflect.DeepEqual(beforeStep, afterStep) {
				t.Fatal("producer historical reply changed durable job or step")
			}
			afterApproval, err := approvals.GetByApprovalID(producer.ctx, beforeApproval.ApprovalID)
			if err != nil {
				t.Fatal(err)
			}
			var afterGeneration model.JobRunGeneration
			if err := producer.db.WithContext(producer.ctx).Select(retryApprovalTestColumns(producer.db, &model.JobRunGeneration{})).Where("generation_id = ?", producer.generation.GenerationID).First(&afterGeneration).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeApproval, afterApproval) || !reflect.DeepEqual(beforeGeneration, afterGeneration) {
				t.Fatal("terminal reply changed durable approval or generation")
			}
			assertStepAttemptMySQLPublication(t, producer, model.JobRunStatusSuccess, artifact)
		})
	}
}

func stepRetryApprovalConsumerFixture(t *testing.T, producer *stepAttemptFixture, artifact *model.ProjectArtifact) *stepAttemptFixture {
	t.Helper()
	ctx, db, jobs := producer.ctx, producer.db, producer.jobs
	if err := jobs.Create(ctx, &model.JobRun{JobRunID: "consumer", PipelineID: "pipeline", PipelineRunID: "run", Status: model.JobRunStatusQueued}); err != nil {
		t.Fatal(err)
	}
	consumer := populateStepAttemptFixtureForJobConfigured(t, ctx, db, jobs, "", "consumer", func(f *generationFixture) {
		f.plan.RetryApprovalRequired = true
		f.plan.RetryApprovalTimeoutSeconds = 300
		plan, err := f.plan.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		digest, err := f.plan.Digest("pipeline", "run", "consumer", "agent", f.identity.RuntimeBindingDigest)
		if err != nil {
			t.Fatal(err)
		}
		size := artifact.Size
		manifest, err := taskqueue.NewJobInputManifest("", map[string]string{"build/out.txt": artifact.StoragePath}, map[string]taskqueue.ArtifactReference{"build/out.txt": {StorageID: artifact.StorageID, Fingerprint: artifact.StorageFingerprint, SHA256: artifact.SHA256, Size: &size}})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := manifest.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "consumer").Updates(map[string]any{"job_name": "build", "retry_approval_required": true, "retry_plan": plan, "retry_plan_digest": digest, "input_expectation": encoded}).Error; err != nil {
			t.Fatal(err)
		}
		f.manifest = manifest
		f.admit.RetryPlanDigest = digest
		f.admit.RequestDigest, _ = f.admit.Digest()
	})
	job, err := jobs.GetByJobRunID(ctx, "consumer")
	if err != nil {
		t.Fatal(err)
	}
	approval := &ApprovalRepo{IDatabase: statusTestDB{db}}
	if _, err := approval.Decide(ctx, job.RetryApprovalID, "voter", "approve", ""); err != nil {
		t.Fatal(err)
	}
	startup := retryapproval.Request{Version: 1, Operation: "admit", Identity: consumer.identity, GenerationID: consumer.generation.GenerationID, GenerationBindingDigest: consumer.generation.BindingDigest, RetryPlanDigest: job.RetryPlanDigest, InputManifestDigest: job.InputManifestDigest}
	if _, err := jobs.ReconcileJobRetryApproval(ctx, startup); err != nil {
		t.Fatal(err)
	}
	return consumer
}
