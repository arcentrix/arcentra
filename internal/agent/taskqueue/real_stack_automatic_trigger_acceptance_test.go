// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/process"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/admission"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/trigger"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/safe"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	mysqldriver "github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type realStackWebhookFixture struct {
	fixture    *controllerWorkerFixture
	service    *controlservice.ScmService
	repository string
	secret     string
	body       []byte
	headers    map[string]string
	commit     string
	requestID  string
}

// realStackSanitizeGit 隔离全部继承的 Git 配置、目录、凭据和对象路径环境
// 生产定义加载也继承这个环境；测试结束后 testing 恢复原始环境
func realStackSanitizeGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("signed webhook acceptance requires the local Git fixture")
	}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, value)
			if err := os.Unsetenv(key); err != nil {
				t.Fatal("cannot isolate inherited Git environment")
			}
		}
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
}

// realStackSubmitSignedWebhook 使用真实签名验证、Git 定义加载及 Process 入场
// 不替换 SCM provider，不注册外部 webhook，不访问外部 SCM 或真实凭据
func realStackSubmitSignedWebhook(t *testing.T, f *controllerWorkerFixture, jobs ...*spec.Job) (*model.PipelineRun, *realStackWebhookFixture) {
	t.Helper()
	realStackSanitizeGit(t)
	directory, _, _ := controllerCheckoutRepository(t, f.ctx)
	projects := repo.NewProjectRepo(workerTestDatabase{f.db})
	w := &realStackWebhookFixture{fixture: f, repository: directory, secret: id.GetUUID() + id.GetUUID()}
	w.service = controlservice.NewScmService(projects, f.repos.Pipeline)
	w.service.SetEngine(f.engine)
	if err := projects.Update(f.ctx, f.projectID, map[string]any{
		"repo_type": model.RepoTypeGitHub, "repo_url": directory, "default_branch": "main",
		"is_enabled": 1, "status": model.ProjectStatusActive, "auth_type": model.AuthTypeNone, "credential": "", "webhook_secret": w.secret,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.repos.Pipeline.Update(f.ctx, f.pipelineID, map[string]any{
		"repo_url": directory, "default_branch": "main", "pipeline_file_path": "pipeline.json",
	}); err != nil {
		t.Fatal(err)
	}
	definition := &spec.Pipeline{Namespace: "controller-agent", Triggers: []*spec.Trigger{{Type: trigger.TriggerTypeEvent, Options: spec.MapAsStruct(map[string]any{
		trigger.OptionEventType: "push", trigger.OptionBranch: "main",
	})}}}
	for _, job := range jobs {
		copy := proto.Clone(job).(*spec.Job)
		if copy.Env == nil {
			copy.Env = make(map[string]string)
		}
		copy.Env["ROUNDTRIP_VALUE"] = "controller-agent-artifact"
		definition.Jobs = append(definition.Jobs, copy)
	}
	w.writeDefinition(t, definition)
	// A signature over different bytes must fail before any run is created.
	if _, err := w.service.HandleWebhook(f.ctx, f.projectID, w.headers, append(append([]byte(nil), w.body...), ' ')); err == nil {
		t.Fatal("production GitHub verifier accepted altered signed bytes")
	}
	if realStackAutomaticRunCount(t, f) != 0 {
		t.Fatal("invalid webhook signature created a run")
	}
	run := w.deliver(t)
	if run.StartTime == nil || run.Status != model.PipelineStatusRunning && run.Status != model.PipelineStatusSuccess {
		t.Fatalf("valid signed automatic run was not durably accepted: status=%d", run.Status)
	}
	if duplicate := w.deliver(t); duplicate.RunID != run.RunID {
		t.Fatal("webhook redelivery after acceptance created another run")
	}
	if realStackAutomaticRunCount(t, f) != 1 {
		t.Fatal("webhook redelivery after acceptance created another request identity")
	}
	return run, w
}

func (w *realStackWebhookFixture) writeDefinition(t *testing.T, definition *spec.Pipeline) {
	t.Helper()
	content, err := spec.MarshalProtoByFormat(definition, pipelinev1.SpecFormat_SPEC_FORMAT_JSON, "pipeline.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.repository, "pipeline.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	controllerCheckoutGit(t, w.fixture.ctx, w.repository, "add", "pipeline.json")
	controllerCheckoutGit(t, w.fixture.ctx, w.repository, "commit", "-m", "synthetic automatic trigger definition")
	w.commit = controllerCheckoutGit(t, w.fixture.ctx, w.repository, "rev-parse", "HEAD")
	if len(w.commit) != 40 {
		t.Fatal("automatic definition fixture lacks a complete Git commit")
	}
	occurred := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	w.body, err = sonic.Marshal(map[string]any{
		"ref": "refs/heads/main", "head_commit": map[string]any{"id": w.commit, "timestamp": occurred.Format(time.RFC3339)},
	})
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(w.secret))
	_, _ = mac.Write(w.body)
	w.headers = map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": "sha256=" + hex.EncodeToString(mac.Sum(nil))}
	w.requestID = fmt.Sprintf("event:%s:push:%s:%d", w.fixture.pipelineID, w.commit, occurred.Unix())
}

func (w *realStackWebhookFixture) deliver(t *testing.T) *model.PipelineRun {
	t.Helper()
	f := w.fixture
	events, err := w.service.HandleWebhook(f.ctx, f.projectID, w.headers, w.body)
	if err != nil || len(events) != 1 {
		t.Fatalf("production signed webhook service: events=%d error=%v", len(events), err)
	}
	run, err := f.repos.Pipeline.GetRunByRequestID(f.ctx, f.pipelineID, w.requestID)
	if err != nil || run == nil {
		t.Fatalf("signed webhook did not persist its request identity: %v", err)
	}
	if run.PipelineID != f.pipelineID || run.RequestID != w.requestID || run.TriggerType != int(pipelinev1.TriggerType_TRIGGER_TYPE_EVENT) || run.TriggeredBy != "webhook" || run.Branch != "main" || run.CommitSha != w.commit || run.DefinitionCommitSha != w.commit || run.DefinitionPath != "pipeline.json" || run.AllowDeployments || run.RetryApprovalContextDigest != "" {
		t.Fatal("signed webhook lost its immutable definition or automatic request identity")
	}
	var count int64
	if err := f.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Where("pipeline_id = ? AND request_id = ?", f.pipelineID, w.requestID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("webhook delivery has %d request rows: %v", count, err)
	}
	return run
}

type realStackAutomaticEvidence struct {
	Run       *model.PipelineRun
	Jobs      []*model.JobRun
	Steps     []model.StepRun
	Artifacts []model.ProjectArtifact
}

func realStackAutomaticRunCount(t *testing.T, f *controllerWorkerFixture) int64 {
	t.Helper()
	var count int64
	if err := f.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Where("pipeline_id = ?", f.pipelineID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func realStackReadAutomaticEvidence(t *testing.T, f *controllerWorkerFixture, runID string) realStackAutomaticEvidence {
	t.Helper()
	var evidence realStackAutomaticEvidence
	var err error
	evidence.Run, err = f.repos.Pipeline.GetRun(f.ctx, runID)
	if err != nil || evidence.Run == nil {
		t.Fatalf("load automatic run evidence: %v", err)
	}
	evidence.Jobs, err = f.repos.JobRun.ListByPipelineRunID(f.ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	evidence.Steps, _, err = f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: runID, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	evidence.Artifacts, _, err = f.repos.Artifact.List(f.ctx, repo.ArtifactQuery{ProjectID: f.projectID, RunID: runID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(evidence.Jobs, func(i, j int) bool { return evidence.Jobs[i].JobRunID < evidence.Jobs[j].JobRunID })
	sort.Slice(evidence.Steps, func(i, j int) bool { return evidence.Steps[i].StepRunID < evidence.Steps[j].StepRunID })
	sort.Slice(evidence.Artifacts, func(i, j int) bool { return evidence.Artifacts[i].ArtifactID < evidence.Artifacts[j].ArtifactID })
	return evidence
}

func realStackAssertWebhookTerminalReplay(t *testing.T, w *realStackWebhookFixture, runID string) {
	t.Helper()
	f := w.fixture
	count := realStackAutomaticRunCount(t, f)
	before := realStackReadAutomaticEvidence(t, f, runID)
	if before.Run.Status != model.PipelineStatusSuccess || len(before.Jobs) != 2 || len(before.Steps) != 4 || len(before.Artifacts) != 1 {
		t.Fatal("terminal webhook replay requires the completed artifact DAG")
	}
	if replay := w.deliver(t); replay.RunID != runID {
		t.Fatal("terminal webhook redelivery created another run")
	}
	after := realStackReadAutomaticEvidence(t, f, runID)
	if !reflect.DeepEqual(before, after) || realStackAutomaticRunCount(t, f) != count {
		t.Fatal("terminal webhook replay changed persisted run, jobs, steps or artifacts")
	}
	for _, job := range after.Jobs {
		realStackAssertOnce(t, f, runID, job.JobRunID)
	}
}

func realStackAssertWebhookRejected(t *testing.T, w *realStackWebhookFixture) {
	t.Helper()
	f := w.fixture
	w.writeDefinition(t, &spec.Pipeline{Namespace: "controller-agent", Triggers: []*spec.Trigger{{Type: trigger.TriggerTypeEvent, Options: spec.MapAsStruct(map[string]any{
		trigger.OptionEventType: "push", trigger.OptionBranch: "main",
	})}}})
	run := w.deliver(t)
	if run.Status != model.PipelineStatusFailed || run.StartTime != nil || run.EndTime == nil || run.CompletedJobs != 0 || run.FailedJobs != 0 || run.RunningJobs != 0 {
		t.Fatal("schema-rejected automatic run was not terminalized before execution")
	}
	before := realStackReadAutomaticEvidence(t, f, run.RunID)
	count := realStackAutomaticRunCount(t, f)
	if len(before.Jobs) != 0 || len(before.Steps) != 0 || len(before.Artifacts) != 0 {
		t.Fatal("schema-rejected automatic run created execution or artifact records")
	}
	if replay := w.deliver(t); replay.RunID != run.RunID {
		t.Fatal("rejected webhook redelivery created another run")
	}
	if !reflect.DeepEqual(before, realStackReadAutomaticEvidence(t, f, run.RunID)) || realStackAutomaticRunCount(t, f) != count {
		t.Fatal("rejected webhook redelivery changed terminal rejection evidence")
	}
	realStackAssertNoAutomaticDispatch(t, f, run.RunID)
}

func realStackAssertNoAutomaticDispatch(t *testing.T, f *controllerWorkerFixture, runID string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.conf.Agent.WorkspaceDir, runID)); !os.IsNotExist(err) {
		t.Fatal("rejected automatic run created an Agent workspace")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, task := range f.tasks {
		var payload taskpayload.JobRunTaskPayload
		if err := sonic.Unmarshal(task.Payload, &payload); err != nil {
			t.Fatal("cannot inspect recorded Process dispatch")
		}
		if payload.PipelineRunID == runID {
			t.Fatal("rejected automatic run published a job task")
		}
	}
}

type realStackAutomaticOrderKey struct{}

type realStackAutomaticOrder struct{ order, runID string }

type realStackAutomaticTransition struct {
	changed bool
	err     error
}

// realStackAutomaticRepository 只标记并观察生产事务，不替换认领或拒绝实现
type realStackAutomaticRepository struct {
	*repo.PipelineRepo
	observed *repo.PipelineRepo
	order    string
	result   chan realStackAutomaticTransition
}

// StartAutomaticRunIfPending 观察带精确运行标记的生产认领事务
func (r *realStackAutomaticRepository) StartAutomaticRunIfPending(ctx context.Context, run *model.PipelineRun) (bool, error) {
	changed, err := r.observed.StartAutomaticRunIfPending(context.WithValue(ctx, realStackAutomaticOrderKey{}, realStackAutomaticOrder{r.order, run.RunID}), run)
	r.result <- realStackAutomaticTransition{changed: changed, err: err}
	return changed, err
}

// RejectPendingAutomaticRun 观察带精确运行标记的生产拒绝事务
func (r *realStackAutomaticRepository) RejectPendingAutomaticRun(ctx context.Context, run *model.PipelineRun) (bool, error) {
	changed, err := r.observed.RejectPendingAutomaticRun(context.WithValue(ctx, realStackAutomaticOrderKey{}, realStackAutomaticOrder{r.order, run.RunID}), run)
	r.result <- realStackAutomaticTransition{changed: changed, err: err}
	return changed, err
}

type realStackAutomaticLock struct {
	EngineLockID string
	IndexName    string
	LockData     *string
}

type realStackAutomaticLockOwner struct {
	threadID uint64
	lockIDs  []string
	err      error
}

func realStackAutomaticConnection(ctx context.Context, tx *gorm.DB) (uint64, error) {
	var row struct{ ThreadID uint64 }
	// NewDB clears the parent WHERE/FOR UPDATE clauses while retaining its connection.
	err := tx.Session(&gorm.Session{NewDB: true, Context: ctx}).
		Table("performance_schema.threads").Select("THREAD_ID AS thread_id").
		Where("PROCESSLIST_ID = CONNECTION_ID()").Take(&row).Error
	if err == nil && row.ThreadID == 0 {
		err = fmt.Errorf("automatic admission connection has no observable MySQL thread")
	}
	return row.ThreadID, err
}

func realStackAutomaticTargetLocks(locks []realStackAutomaticLock, rowID uint64, runID string) ([]string, error) {
	if rowID == 0 || runID == "" || len(locks) == 0 || len(locks) > 2 {
		return nil, fmt.Errorf("automatic admission must hold a bounded exact target-row lock set")
	}
	primary := strconv.FormatUint(rowID, 10)
	seen, indexes := make(map[string]bool), make(map[string]bool)
	var result []string
	for _, lock := range locks {
		if lock.EngineLockID == "" || seen[lock.EngineLockID] || indexes[lock.IndexName] || lock.LockData == nil {
			return nil, fmt.Errorf("automatic admission row lock is missing or ambiguous")
		}
		switch lock.IndexName {
		case "PRIMARY":
			if *lock.LockData != primary {
				return nil, fmt.Errorf("automatic admission primary lock belongs to another run")
			}
		case "uk_pipeline_run_business_id":
			key, row, ok := strings.Cut(*lock.LockData, ", ")
			if !ok || row != primary || (key != runID && key != "'"+runID+"'" && key != strconv.Quote(runID)) {
				return nil, fmt.Errorf("automatic admission business-key lock belongs to another run")
			}
		default:
			return nil, fmt.Errorf("unexpected automatic admission record-lock index")
		}
		seen[lock.EngineLockID], indexes[lock.IndexName] = true, true
		result = append(result, lock.EngineLockID)
	}
	if !indexes["PRIMARY"] {
		return nil, fmt.Errorf("automatic admission did not expose its exact primary row lock")
	}
	return result, nil
}

// realStackAutomaticContention 要求 MySQL 实测第二个连接等待首事务的同一运行行
// 两个独立 Process 的本地互斥锁不能串行化这个边界；测试不以 sleep 推断竞争
func realStackAutomaticContention(t *testing.T, f *controllerWorkerFixture, observed *gorm.DB, schema string, run *model.PipelineRun, first, second func() error) [2]error {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
	defer cancel()
	var target model.PipelineRun
	if err := f.db.WithContext(ctx).Select("id", "run_id").Where("run_id = ?", run.RunID).First(&target).Error; err != nil || target.ID == 0 || target.RunID != run.RunID {
		t.Fatalf("cannot identify exact automatic admission row: %v", err)
	}
	locked, competing := make(chan realStackAutomaticLockOwner, 1), make(chan realStackAutomaticLockOwner, 1)
	release := make(chan struct{})
	var lockOnce, competingOnce, releaseOnce sync.Once
	var wait sync.WaitGroup
	beforeName, afterName := "test:auto-before-"+id.GetUUID(), "test:auto-after-"+id.GetUUID()
	isRunLock := func(tx *gorm.DB) bool {
		lock, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
		return tx.Statement.Table == (model.PipelineRun{}).TableName() && ok && lock.Strength == "UPDATE"
	}
	defer func() {
		releaseOnce.Do(func() { close(release) })
		cancel()
		wait.Wait()
		if err := observed.Callback().Query().Remove(beforeName); err != nil {
			t.Error(err)
		}
		if err := observed.Callback().Query().Remove(afterName); err != nil {
			t.Error(err)
		}
	}()
	if err := observed.Callback().Query().Before("gorm:query").Register(beforeName, func(tx *gorm.DB) {
		if isRunLock(tx) && tx.Statement.Context.Value(realStackAutomaticOrderKey{}) == (realStackAutomaticOrder{"second", target.RunID}) {
			competingOnce.Do(func() {
				threadID, err := realStackAutomaticConnection(ctx, tx)
				if err != nil {
					_ = tx.AddError(err)
				}
				competing <- realStackAutomaticLockOwner{threadID: threadID, err: err}
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := observed.Callback().Query().After("gorm:query").Register(afterName, func(tx *gorm.DB) {
		if tx.Error == nil && isRunLock(tx) && tx.Statement.Context.Value(realStackAutomaticOrderKey{}) == (realStackAutomaticOrder{"first", target.RunID}) {
			lockOnce.Do(func() {
				owner := realStackAutomaticLockOwner{}
				stored, ok := tx.Statement.Dest.(*model.PipelineRun)
				if !ok || tx.RowsAffected != 1 || stored.RunID != target.RunID {
					owner.err = fmt.Errorf("automatic admission did not lock exactly the target run")
				} else {
					owner.threadID, owner.err = realStackAutomaticConnection(ctx, tx)
				}
				if owner.err == nil {
					var locks []realStackAutomaticLock
					owner.err = tx.Session(&gorm.Session{NewDB: true, Context: ctx}).
						Table("performance_schema.data_locks").Select("ENGINE_LOCK_ID AS engine_lock_id", "INDEX_NAME AS index_name", "LOCK_DATA AS lock_data").
						Where("ENGINE = ? AND THREAD_ID = ? AND OBJECT_SCHEMA = ? AND OBJECT_NAME = ?", "INNODB", owner.threadID, schema, (model.PipelineRun{}).TableName()).
						Where("LOCK_TYPE = ? AND LOCK_STATUS = ? AND LOCK_MODE LIKE ?", "RECORD", "GRANTED", "X%").Find(&locks).Error
					if owner.err == nil {
						owner.lockIDs, owner.err = realStackAutomaticTargetLocks(locks, target.ID, target.RunID)
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
	var results [2]error
	wait.Add(1)
	safe.Go(func() { defer wait.Done(); results[0] = first() })
	var owner, contender realStackAutomaticLockOwner
	select {
	case owner = <-locked:
		if owner.err != nil {
			t.Fatalf("cannot inspect automatic admission row locks: %v", owner.err)
		}
	case <-ctx.Done():
		t.Fatal("first Process did not acquire the real MySQL automatic admission lock")
	}
	wait.Add(1)
	safe.Go(func() { defer wait.Done(); results[1] = second() })
	select {
	case contender = <-competing:
		if contender.err != nil || contender.threadID == 0 || contender.threadID == owner.threadID {
			t.Fatalf("competing Process lacks a distinct MySQL connection: %v", contender.err)
		}
	case <-ctx.Done():
		t.Fatal("second Process never attempted the real MySQL automatic admission lock")
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var edges []struct{ RequestingLockID string }
		err := f.db.WithContext(ctx).Table("performance_schema.data_lock_waits AS waits").
			Select("waits.REQUESTING_ENGINE_LOCK_ID AS requesting_lock_id").
			Joins("JOIN performance_schema.data_locks AS requested ON requested.ENGINE = waits.ENGINE AND requested.ENGINE_LOCK_ID = waits.REQUESTING_ENGINE_LOCK_ID AND requested.THREAD_ID = waits.REQUESTING_THREAD_ID").
			Joins("JOIN performance_schema.data_locks AS blocking ON blocking.ENGINE = waits.ENGINE AND blocking.ENGINE_LOCK_ID = waits.BLOCKING_ENGINE_LOCK_ID AND blocking.THREAD_ID = waits.BLOCKING_THREAD_ID").
			Where("waits.ENGINE = ? AND waits.REQUESTING_THREAD_ID = ? AND waits.BLOCKING_THREAD_ID = ?", "INNODB", contender.threadID, owner.threadID).
			Where("requested.OBJECT_SCHEMA = ? AND requested.OBJECT_NAME = ? AND requested.LOCK_TYPE = ? AND requested.LOCK_STATUS = ?", schema, (model.PipelineRun{}).TableName(), "RECORD", "WAITING").
			Where("blocking.OBJECT_SCHEMA = ? AND blocking.OBJECT_NAME = ? AND blocking.LOCK_TYPE = ? AND blocking.LOCK_STATUS = ?", schema, (model.PipelineRun{}).TableName(), "RECORD", "GRANTED").
			Where("requested.LOCK_MODE LIKE ? AND blocking.LOCK_MODE LIKE ?", "X%", "X%").
			Where("requested.INDEX_NAME = blocking.INDEX_NAME AND requested.LOCK_DATA = blocking.LOCK_DATA").
			Where("blocking.ENGINE_LOCK_ID IN ?", owner.lockIDs).Limit(1).Find(&edges).Error
		if err != nil {
			t.Fatalf("cannot inspect automatic admission MySQL contention: %v", err)
		}
		if len(edges) == 1 && edges[0].RequestingLockID != "" {
			t.Log("observed two independent Process connections contending for the exact automatic PipelineRun record lock")
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("MySQL never reported the expected automatic admission row-lock wait")
		case <-ticker.C:
		}
	}
	releaseOnce.Do(func() { close(release) })
	wait.Wait()
	return results
}

func realStackAssertAutomaticContention(t *testing.T, f *controllerWorkerFixture, settings *realStackSettings, definitionCommit string, acceptFirst, acceptSecond bool) {
	t.Helper()
	config, err := mysqldriver.ParseDSN(settings.dsn)
	if err != nil || f.db.Dialector.Name() != "mysql" {
		t.Fatal("automatic contention requires the already guarded real MySQL fixture")
	}
	pool, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// Reuse the guarded connection pool, with an isolated callback registry used
	// only by these two admission transactions, never by concurrent Agent queries.
	observed, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: pool, SkipInitializeWithVersion: true}), &gorm.Config{Logger: f.db.Logger, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	runID := id.GetUild()
	run := &model.PipelineRun{
		RunID: runID, PipelineID: f.pipelineID, RequestID: "event:" + f.pipelineID + ":race:" + runID,
		TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_EVENT), TriggeredBy: "webhook", Status: model.PipelineStatusPending,
		DefinitionPath: "pipeline.json", DefinitionCommitSha: definitionCommit, Branch: "main", CommitSha: definitionCommit,
		Env: `{"AUTOMATIC_FIXTURE":"row-lock","SECOND":"value"}`,
	}
	if err := f.repos.Pipeline.CreateRun(f.ctx, run); err != nil {
		t.Fatal(err)
	}
	makeProcess := func(order string) (*process.Process, *realStackAutomaticRepository) {
		repository := &realStackAutomaticRepository{
			PipelineRepo: repo.NewPipelineRepo(workerTestDatabase{f.db}).(*repo.PipelineRepo),
			observed:     repo.NewPipelineRepo(workerTestDatabase{observed}).(*repo.PipelineRepo), order: order, result: make(chan realStackAutomaticTransition, 1),
		}
		repositories := *f.repos
		repositories.Pipeline = repository
		engine := process.NewProcess(&repositories, nil, &controllerWorkerRecordingQueue{TaskQueue: f.producer, fixture: f}, nil, &log.Logger{SugaredLogger: zap.NewNop().Sugar()}, nil, nil)
		t.Cleanup(engine.Stop)
		return engine, repository
	}
	first, firstRepo := makeProcess("first")
	second, secondRepo := makeProcess("second")
	valid := &spec.Pipeline{Namespace: "controller-agent", Jobs: []*spec.Job{{Name: "automatic-contention-winner", Steps: []*spec.Step{controllerWorkerShell("once", "printf x >> executions")}}}}
	invalid := &spec.Pipeline{Namespace: "controller-agent"}
	firstSpec, secondSpec := invalid, invalid
	if acceptFirst {
		firstSpec = valid
	}
	if acceptSecond {
		secondSpec = valid
	}
	firstRun, secondRun := *run, *run
	results := realStackAutomaticContention(t, f, observed, config.DBName, run,
		func() error { return first.Submit(&firstRun, firstSpec) },
		func() error { return second.Submit(&secondRun, secondSpec) },
	)
	for _, result := range results {
		var mysqlErr *mysqldriver.MySQLError
		if errors.Is(result, context.DeadlineExceeded) || errors.Is(result, context.Canceled) || errors.As(result, &mysqlErr) {
			t.Fatalf("database or timeout failure is not an admission outcome: %v", result)
		}
	}
	firstTransition, secondTransition := <-firstRepo.result, <-secondRepo.result
	if firstTransition.err != nil || secondTransition.err != nil || !firstTransition.changed || secondTransition.changed {
		t.Fatal("competing Process transitions did not confirm exactly the first transaction as winner")
	}
	if acceptFirst {
		if results[0] != nil || results[1] == nil || admission.IsRejected(results[1]) == acceptSecond {
			t.Fatalf("accepted winner/loser submit outcomes: %v", results)
		}
		f.waitRun(run.RunID, model.PipelineStatusSuccess)
		evidence := realStackReadAutomaticEvidence(t, f, run.RunID)
		if evidence.Run.StartTime == nil || evidence.Run.EndTime == nil || len(evidence.Jobs) != 1 || len(evidence.Steps) != 1 || len(evidence.Artifacts) != 0 || evidence.Jobs[0].Status != model.JobRunStatusSuccess || evidence.Steps[0].Status != 4 {
			t.Fatal("accepted automatic winner did not complete exactly one real Agent job")
		}
		realStackAssertOnce(t, f, run.RunID, evidence.Jobs[0].JobRunID)
		f.mu.Lock()
		// The original real-stack scenario also publishes one duplicate task.
		minimum := int64(len(f.tasks) + 1)
		f.mu.Unlock()
		assertControllerQueueCommitted(t, f, minimum)
	} else {
		if !admission.IsRejected(results[0]) || results[1] == nil || admission.IsRejected(results[1]) {
			t.Fatalf("reject-before-accept submit outcomes: %v", results)
		}
		evidence := realStackReadAutomaticEvidence(t, f, run.RunID)
		if evidence.Run.Status != model.PipelineStatusFailed || evidence.Run.StartTime != nil || evidence.Run.EndTime == nil || len(evidence.Jobs) != 0 || len(evidence.Steps) != 0 || len(evidence.Artifacts) != 0 {
			t.Fatal("rejected automatic winner was revived or created execution evidence")
		}
		realStackAssertNoAutomaticDispatch(t, f, run.RunID)
	}
}

// TestControllerWorkerSignedWebhookAdmission 在 SQLite/MockCluster/内存制品夹具验证共享入口
// 这只是本地装配回归；不作为真实 MySQL、Kafka 或 S3 验收证据
func TestControllerWorkerSignedWebhookAdmission(t *testing.T) {
	f := newControllerWorkerFixture(t, false, controllerWorkerTLS(t))
	// Deliberately inherited Git overrides must not affect production definition loading.
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "must-not-be-used"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "never")
	run, webhook := realStackSubmitSignedWebhook(t, f,
		&spec.Job{Name: "build", Steps: []*spec.Step{
			controllerWorkerShell("produce", `printf x >> executions; mkdir -p dist; printf '%s' "$ROUNDTRIP_VALUE" > dist/result.txt`),
			{Name: "publish", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"paths": []any{"dist"}})},
		}},
		&spec.Job{Name: "consume", DependsOn: []string{"build"}, Steps: []*spec.Step{
			{Name: "fetch", Uses: "builtin:artifacts", Action: "download", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"job": "build", "paths": []any{"dist/*.txt"}})},
			controllerWorkerShell("verify", `test "$(cat dist/result.txt)" = "$ROUNDTRIP_VALUE" && printf x >> executions`),
		}},
	)
	f.waitRun(run.RunID, model.PipelineStatusSuccess)
	f.wait("local signed-webhook terminal receipts acknowledged", func() bool {
		f.worker.reports.mu.Lock()
		defer f.worker.reports.mu.Unlock()
		return len(f.worker.reports.entries) == 0
	})
	realStackAssertWebhookTerminalReplay(t, webhook, run.RunID)
	realStackAssertWebhookRejected(t, webhook)
}

// TestRealStackAutomaticTargetLocks 验证记录锁证明拒绝缺失、含混或其他运行的锁
func TestRealStackAutomaticTargetLocks(t *testing.T) {
	primary, business, other := "42", "'automatic-run', 42", "43"
	for _, tc := range []struct {
		name  string
		locks []realStackAutomaticLock
		valid bool
	}{
		{name: "exact_primary", locks: []realStackAutomaticLock{{"primary-lock", "PRIMARY", &primary}}, valid: true},
		{name: "exact_primary_and_business", locks: []realStackAutomaticLock{{"primary-lock", "PRIMARY", &primary}, {"business-lock", "uk_pipeline_run_business_id", &business}}, valid: true},
		{name: "missing"},
		{name: "other_row", locks: []realStackAutomaticLock{{"primary-lock", "PRIMARY", &other}}},
		{name: "opaque", locks: []realStackAutomaticLock{{"primary-lock", "PRIMARY", nil}}},
		{name: "business_only", locks: []realStackAutomaticLock{{"business-lock", "uk_pipeline_run_business_id", &business}}},
		{name: "duplicate_lock", locks: []realStackAutomaticLock{{"same-lock", "PRIMARY", &primary}, {"same-lock", "uk_pipeline_run_business_id", &business}}},
		{name: "other_index", locks: []realStackAutomaticLock{{"other-lock", "unrelated-index", &primary}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			locks, err := realStackAutomaticTargetLocks(tc.locks, 42, "automatic-run")
			if (err == nil) != tc.valid || tc.valid && len(locks) != len(tc.locks) {
				t.Fatalf("exact target-lock proof: locks=%d error=%v", len(locks), err)
			}
		})
	}
}
