// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/grpc/interceptor"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/logstream"
	kafkaconfig "github.com/arcentrix/arcentra/pkg/mq/kafka"
	"github.com/arcentrix/arcentra/pkg/nova"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bytedance/sonic"
	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
	mysqldriver "github.com/go-sql-driver/mysql"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type realStackSettings struct {
	dsn, brokers, endpoint, accessKey, secretKey string
}

func realStackEnvironment(getenv func(string) string) (*realStackSettings, error) {
	enabled, required := getenv("ARCENTRA_REAL_STACK"), getenv("ARCENTRA_REQUIRE_REAL_STACK")
	if required != "" && required != "1" {
		return nil, fmt.Errorf("ARCENTRA_REQUIRE_REAL_STACK must be 1 when set")
	}
	if enabled == "" && required != "1" {
		return nil, nil
	}
	if enabled != "1" {
		return nil, fmt.Errorf("ARCENTRA_REAL_STACK=1 is required for real-stack acceptance")
	}
	settings := &realStackSettings{}
	for name, destination := range map[string]*string{
		"ARCENTRA_TEST_MYSQL_DSN":     &settings.dsn,
		"ARCENTRA_TEST_KAFKA_BROKERS": &settings.brokers,
		"ARCENTRA_TEST_S3_ENDPOINT":   &settings.endpoint,
		"ARCENTRA_TEST_S3_ACCESS_KEY": &settings.accessKey,
		"ARCENTRA_TEST_S3_SECRET_KEY": &settings.secretKey,
	} {
		*destination = strings.TrimSpace(getenv(name))
		if *destination == "" {
			return nil, fmt.Errorf("%s is required; real-stack acceptance never falls back to mocks", name)
		}
	}
	return settings, nil
}

func TestRealStackAcceptanceGate(t *testing.T) {
	for _, tc := range []struct {
		name        string
		env         map[string]string
		wantError   bool
		wantEnabled bool
	}{
		{name: "ordinary_local_run_skips"},
		{name: "dedicated_entry_cannot_skip", env: map[string]string{"ARCENTRA_REQUIRE_REAL_STACK": "1"}, wantError: true},
		{name: "opt_in_missing_dependencies_fails", env: map[string]string{"ARCENTRA_REAL_STACK": "1"}, wantError: true},
		{name: "invalid_opt_in_fails", env: map[string]string{"ARCENTRA_REAL_STACK": "true"}, wantError: true},
		{name: "invalid_required_flag_fails", env: map[string]string{"ARCENTRA_REQUIRE_REAL_STACK": "true"}, wantError: true},
		{name: "complete_opt_in_enables", env: map[string]string{
			"ARCENTRA_REAL_STACK": "1", "ARCENTRA_REQUIRE_REAL_STACK": "1",
			"ARCENTRA_TEST_MYSQL_DSN": "synthetic-dsn", "ARCENTRA_TEST_KAFKA_BROKERS": "127.0.0.1:19092",
			"ARCENTRA_TEST_S3_ENDPOINT": "http://127.0.0.1:19000", "ARCENTRA_TEST_S3_ACCESS_KEY": "synthetic-key", "ARCENTRA_TEST_S3_SECRET_KEY": "synthetic-secret",
		}, wantEnabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, err := realStackEnvironment(func(name string) string { return tc.env[name] })
			if (err != nil) != tc.wantError || (settings != nil) != tc.wantEnabled {
				t.Fatalf("gate result settings=%v error=%v", settings != nil, err)
			}
		})
	}
}

// TestRealStackAcceptance 验收真实 MySQL、Kafka、S3 与经认证 TLS gRPC 的完整作业闭环
// 仅显式 opt-in 运行；专用 CI 同时设置 ARCENTRA_REQUIRE_REAL_STACK=1 防止误跳过
// 使用独占的临时 Kafka 和 MySQL schema；全局 Agent 存储/鉴权配置禁止并行运行
func TestRealStackAcceptance(t *testing.T) {
	settings, err := realStackEnvironment(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if settings == nil {
		t.Skip("real dependencies are opt-in: set ARCENTRA_REAL_STACK=1 and ARCENTRA_TEST_* (see docs/real-stack-acceptance.md)")
	}
	if testing.Short() {
		t.Fatal("real-stack acceptance must not be run with -short")
	}
	setupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db := realStackDatabase(t, setupCtx, settings.dsn)
	objectStore, storageConfig, reference := realStackStorage(t, setupCtx, settings)
	for _, address := range strings.Split(settings.brokers, ",") {
		realStackLoopback(t, strings.TrimSpace(address), "Kafka")
	}
	publisher, err := executor.NewReliableKafkaLogPublisher(kafkaconfig.Config{BootstrapServers: settings.brokers, SecurityProtocol: "PLAINTEXT"}, "real-stack-"+id.GetUild(), logstream.SpoolConfig{Directory: filepath.Join(t.TempDir(), "logs"), MaxDiskUsageMB: 8}, "")
	if err != nil {
		t.Fatalf("create real Kafka log publisher: %v", err)
	}
	t.Cleanup(publisher.Close)
	options := controllerWorkerTLS(t)
	options.backend = &controllerWorkerBackend{
		db: db, brokers: settings.brokers, store: objectStore, storageConfig: storageConfig,
		reference: reference, logPublisher: publisher,
		prepareTopics: func(t *testing.T, ctx context.Context, agentID string) {
			realStackTopics(t, ctx, settings.brokers, agentID)
		},
		authentication: realStackAuthentication,
	}
	var f *controllerWorkerFixture
	t.Cleanup(func() {
		if f != nil {
			realStackCleanupDatabase(t, f, storageConfig.StorageID)
		}
	})
	f = newControllerWorkerFixture(t, true, options)
	if f.store != nil || f.db.Dialector.Name() != "mysql" || !f.client.ControllerAuthenticated() {
		t.Fatal("acceptance fixture did not use the required real backends and trusted TLS")
	}
	streamService := controlservice.NewStreamService(nil, db, controlservice.KafkaSettings{BootstrapServers: settings.brokers, SecurityProtocol: "PLAINTEXT"})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := streamService.Close(ctx); err != nil {
			t.Errorf("close production Kafka log consumer: %v", err)
		}
	})
	realStackRejectInvalidToken(t, f)
	f.mu.Lock()
	f.loseStepTerminalACK = true
	f.mu.Unlock()
	run := f.submit(
		&spec.Job{Name: "build", Steps: []*spec.Step{
			controllerWorkerShell("produce", `printf x >> executions; mkdir -p dist; printf '%s' "$ROUNDTRIP_VALUE" > dist/result.txt; printf 'acceptance-build-stdout\n'; printf 'acceptance-build-stderr\n' >&2`),
			{Name: "publish", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"paths": []any{"dist"}, "expireIn": "7d"})},
		}},
		&spec.Job{Name: "consume", DependsOn: []string{"build"}, Steps: []*spec.Step{
			{Name: "fetch", Uses: "builtin:artifacts", Action: "download", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"job": "build", "paths": []any{"dist/*.txt"}})},
			controllerWorkerShell("verify", `test "$(cat dist/result.txt)" = "$ROUNDTRIP_VALUE" && printf x >> executions && printf 'acceptance-consume-stdout\n'`),
		}},
	)
	completed := f.waitRun(run.RunID, model.PipelineStatusSuccess)
	if completed.CompletedJobs != 2 || completed.FailedJobs != 0 || completed.EndTime == nil {
		t.Fatalf("pipeline completion was not persisted in MySQL: %+v", completed)
	}
	jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("expected two real dispatched jobs, got %d: %v", len(jobs), err)
	}
	byName := make(map[string]*model.JobRun)
	for _, job := range jobs {
		byName[job.JobName] = job
		if job.Status != model.JobRunStatusSuccess || job.AgentID != f.conf.Agent.ID || job.StartTime == nil || job.EndTime == nil {
			t.Fatalf("job terminal state/identity missing: %+v", job)
		}
		f.assertReceipt("job", job.JobRunID, job.ClaimID, job.TerminalReportDigest, run.RunID, job.JobRunID)
		realStackAssertOnce(t, f, run.RunID, job.JobRunID)
	}
	steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID, PageSize: 100})
	if err != nil || count != 4 {
		t.Fatalf("expected four persisted attempts, got %d: %v", count, err)
	}
	var upload model.StepRun
	for _, step := range steps {
		if step.Status != 4 || step.Attempt != 1 || step.AgentID != f.conf.Agent.ID || step.ExitCode == nil || *step.ExitCode != 0 {
			t.Fatalf("step terminal identity/result missing: %+v", step)
		}
		f.assertReceipt("step", step.StepRunID, step.ClaimID, step.TerminalReportDigest, run.RunID, step.JobRunID)
		if step.Name == "publish" {
			upload = step
		}
	}
	entries, count, err := f.repos.Artifact.List(f.ctx, repo.ArtifactQuery{ProjectID: f.projectID, Limit: 20})
	if err != nil || count != 1 {
		t.Fatalf("expected one real artifact metadata row, got %d: %v", count, err)
	}
	entry := entries[0]
	build, consume := byName["build"], byName["consume"]
	if build == nil || consume == nil {
		t.Fatal("controller did not persist both DAG jobs")
	}
	const artifactContent = "controller-agent-artifact"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(artifactContent)))
	key := fmt.Sprintf("artifacts/%s/%s/%s/%s/dist/result.txt", run.RunID, build.JobRunID, upload.StepRunID, digest)
	if entry.ArtifactID != artifactutil.ID(f.projectID, run.RunID, build.JobRunID, upload.StepRunID, "dist/result.txt") || entry.StorageID != reference.StorageID || entry.StorageFingerprint != reference.Fingerprint || entry.StorageType != "s3" || entry.StoragePath != key || entry.SHA256 != digest || entry.Size != int64(len(artifactContent)) || entry.ExpiresAt == nil {
		t.Fatalf("artifact lost producing attempt, storage identity, or digest: %+v", entry)
	}
	reader, err := objectStore.DownloadStream(f.ctx, key)
	if err != nil {
		t.Fatalf("read uploaded object from real S3: %v", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, 1024))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(data, []byte(artifactContent)) {
		t.Fatalf("real S3 bytes mismatch: read=%v close=%v bytes=%q", readErr, closeErr, data)
	}
	if got := artifactTestRead(t, filepath.Join(f.conf.Agent.WorkspaceDir, run.RunID, consume.JobRunID), "dist/result.txt"); got != artifactContent {
		t.Fatalf("dependent Agent job did not download actual S3 bytes: %q", got)
	}
	realStackWaitLogs(t, f, steps, map[string]string{"acceptance-build-stdout": "stdout", "acceptance-build-stderr": "stderr", "acceptance-consume-stdout": "stdout"})
	f.mu.Lock()
	lostACKs := f.lostACKs
	jobReplays, stepReplays := len(f.receipts["job/"+build.JobRunID]), 0
	for _, step := range steps {
		if n := len(f.receipts["step/"+step.StepRunID]); n > stepReplays {
			stepReplays = n
		}
	}
	duplicate := f.tasks[build.JobRunID]
	f.mu.Unlock()
	if lostACKs != 2 || jobReplays < 2 || stepReplays < 2 {
		t.Fatalf("committed terminal ACK loss did not replay identically: lost=%d job=%d step=%d", lostACKs, jobReplays, stepReplays)
	}
	if len(duplicate.Payload) == 0 {
		t.Fatal("missing original Process-produced Kafka payload")
	}
	if _, err := f.producer.Enqueue(&duplicate, nova.Queue(taskpayload.AgentQueue(f.conf.Agent.ID))); err != nil {
		t.Fatalf("redeliver original task through real Kafka: %v", err)
	}
	// 后续任务完成和 offset 前进证明重复消息被处理，不能靠 sleep 猜测
	failed := f.submit(&spec.Job{Name: "expected-failure", Steps: []*spec.Step{controllerWorkerShell("fail", "printf x >> executions; printf 'acceptance-expected-failure\\n' >&2; exit 7")}})
	f.waitRun(failed.RunID, model.PipelineStatusFailed)
	failedJobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, failed.RunID)
	if err != nil || len(failedJobs) != 1 || failedJobs[0].Status != model.JobRunStatusFailed {
		t.Fatalf("shell failure did not reach MySQL terminal state: %+v %v", failedJobs, err)
	}
	failedSteps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: failed.RunID})
	if err != nil || count != 1 || failedSteps[0].Status != 5 || failedSteps[0].ExitCode == nil || *failedSteps[0].ExitCode != 7 {
		t.Fatalf("shell failure exit code was not preserved: %+v %v", failedSteps, err)
	}
	f.assertReceipt("job", failedJobs[0].JobRunID, failedJobs[0].ClaimID, failedJobs[0].TerminalReportDigest, failed.RunID, failedJobs[0].JobRunID)
	f.assertReceipt("step", failedSteps[0].StepRunID, failedSteps[0].ClaimID, failedSteps[0].TerminalReportDigest, failed.RunID, failedJobs[0].JobRunID)
	realStackWaitLogs(t, f, failedSteps, map[string]string{"acceptance-expected-failure": "stderr"})
	assertControllerQueueCommitted(t, f, 4)
	realStackAssertOnce(t, f, run.RunID, build.JobRunID)
	f.wait("all real-stack terminal receipts acknowledged", func() bool {
		f.worker.reports.mu.Lock()
		defer f.worker.reports.mu.Unlock()
		return len(f.worker.reports.entries) == 0
	})
	t.Log("real-stack accepted: MySQL migration, real Kafka dispatch/log persistence, authenticated TLS gRPC, S3 artifact DAG, success/failure terminal replay and duplicate suppression")
}

func realStackLoopback(t *testing.T, address, service string) {
	t.Helper()
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		t.Fatalf("%s acceptance endpoint must be a numeric loopback host:port; use disposable local services", service)
	}
}

func realStackDatabase(t *testing.T, ctx context.Context, dsn string) *gorm.DB {
	t.Helper()
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid ARCENTRA_TEST_MYSQL_DSN; expected a disposable acceptance schema")
	}
	if cfg.Net != "tcp" || !strings.HasPrefix(cfg.DBName, "arcentra_acceptance_") {
		t.Fatal("MySQL acceptance DSN must use tcp and an arcentra_acceptance_<suffix> schema; the browser fresh-install schema is reserved")
	}
	realStackLoopback(t, cfg.Addr, "MySQL")
	cfg.ParseTime, cfg.Loc = true, time.UTC
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 10*time.Second, 10*time.Second
	cfg.Collation = "utf8mb4_0900_ai_ci"
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connect real MySQL 8 acceptance database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := sqlDB.PingContext(ctx); err != nil {
		t.Fatalf("real MySQL is unavailable: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := repo.MigrateSchema(ctx, db.Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci")); err != nil {
			t.Fatalf("real MySQL migration pass %d: %v", i+1, err)
		}
	}
	var agents int64
	if err := db.WithContext(ctx).Model(&model.Agent{}).Count(&agents).Error; err != nil {
		t.Fatal(err)
	}
	if agents != 0 {
		t.Fatal("acceptance schema contains existing Agents; recreate the disposable real-stack schema before running")
	}
	return db
}

func realStackStorage(t *testing.T, ctx context.Context, cfg *realStackSettings) (*storage.S3Storage, *model.StorageConfig, taskpayload.ArtifactReference) {
	t.Helper()
	endpoint, err := url.Parse(cfg.endpoint)
	if err != nil || endpoint == nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" {
		t.Fatal("S3 acceptance endpoint must be http://numeric-loopback:port without credentials, path, query, or fragment")
	}
	realStackLoopback(t, endpoint.Host, "S3")
	bucket := "arcentra-acceptance-" + strings.ToLower(id.GetUild())
	config := &storage.Storage{Provider: storage.S3, Endpoint: cfg.endpoint, AccessKey: cfg.accessKey, SecretKey: cfg.secretKey, Bucket: bucket, Region: "us-east-1", BasePath: "acceptance"}
	provider, err := storage.NewStorage(config)
	if err != nil {
		t.Fatalf("construct production S3 provider: %v", err)
	}
	store, ok := provider.(*storage.S3Storage)
	if !ok {
		t.Fatal("acceptance requires the production S3 provider")
	}
	if _, err := store.Client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create disposable real S3 bucket: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		objects, err := store.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
		if err != nil {
			t.Errorf("list acceptance bucket during cleanup: %v", err)
			return
		}
		for _, object := range objects.Contents {
			if _, err := store.Client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: object.Key}); err != nil {
				t.Errorf("delete acceptance object: %v", err)
			}
		}
		if _, err := store.Client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}); err != nil {
			t.Errorf("delete acceptance bucket: %v", err)
		}
	})
	encoded, err := sonic.Marshal(model.S3Config{Endpoint: config.Endpoint, AccessKey: config.AccessKey, SecretKey: config.SecretKey, Bucket: config.Bucket, Region: config.Region, BasePath: config.BasePath})
	if err != nil {
		t.Fatal(err)
	}
	storageID := "real-stack-" + id.GetUild()
	return store, &model.StorageConfig{StorageID: storageID, StorageType: storage.S3, IsEnabled: 1, Config: encoded}, taskpayload.ArtifactReference{StorageID: storageID, Fingerprint: artifactutil.StorageFingerprint(storage.S3, config.Endpoint, config.Bucket, config.Region, config.BasePath, false)}
}

func realStackTopics(t *testing.T, ctx context.Context, brokers, agentID string) {
	t.Helper()
	admin, err := ckafka.NewAdminClient(&ckafka.ConfigMap{"bootstrap.servers": brokers, "socket.timeout.ms": 5000})
	if err != nil {
		t.Fatalf("create real Kafka admin: %v", err)
	}
	defer admin.Close()
	var topics []ckafka.TopicSpecification
	var names []string
	for _, prefix := range []string{taskpayload.AgentTopicPrefix(agentID), taskpayload.AgentControlTopicPrefix(agentID)} {
		for _, suffix := range []string{"_PRIORITY_NORMAL", "_PRIORITY_HIGH", "_PRIORITY_LOW", "_TASKS_DELAY_0"} {
			name := strings.ToUpper(prefix + suffix)
			names = append(names, name)
			topics = append(topics, ckafka.TopicSpecification{Topic: name, NumPartitions: 1, ReplicationFactor: 1})
		}
	}
	topics = append(topics, ckafka.TopicSpecification{Topic: "BUILD_LOGS", NumPartitions: 1, ReplicationFactor: 1})
	createCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	results, err := admin.CreateTopics(createCtx, topics, ckafka.SetAdminOperationTimeout(15*time.Second))
	if err != nil || len(results) != len(topics) {
		t.Fatalf("real Kafka topic creation failed (check advertised.listeners): results=%d error=%v", len(results), err)
	}
	for _, result := range results {
		if result.Error.Code() != ckafka.ErrNoError && !(result.Topic == "BUILD_LOGS" && result.Error.Code() == ckafka.ErrTopicAlreadyExists) {
			t.Fatalf("create real Kafka topic %s: %v", result.Topic, result.Error)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cleanupAdmin, err := ckafka.NewAdminClient(&ckafka.ConfigMap{"bootstrap.servers": brokers, "socket.timeout.ms": 5000})
		if err != nil {
			t.Errorf("create Kafka cleanup admin: %v", err)
			return
		}
		defer cleanupAdmin.Close()
		results, err := cleanupAdmin.DeleteTopics(ctx, names, ckafka.SetAdminOperationTimeout(10*time.Second))
		if err != nil {
			t.Errorf("delete acceptance Kafka topics: %v", err)
		}
		for _, result := range results {
			if result.Error.Code() != ckafka.ErrNoError {
				t.Errorf("delete acceptance Kafka topic %s: %v", result.Topic, result.Error)
			}
		}
	})
}

func realStackAuthentication(t *testing.T, ctx context.Context, repos *repo.Repositories, agentID string) (string, grpc.UnaryServerInterceptor) {
	t.Helper()
	encoded, err := sonic.Marshal(map[string]string{"salt": id.GetUUID(), "secret_key": id.GetUUID()})
	if err != nil {
		t.Fatal(err)
	}
	settings := repo.NewSettingRepo(repos.Agent.(*repo.AgentRepo).IDatabase, nil)
	if err := settings.Upsert(ctx, &model.Setting{Name: consts.SettingNameAgentSecretKey, Value: encoded}); err != nil {
		t.Fatalf("seed disposable Agent signing configuration: %v", err)
	}
	settingService := controlservice.NewSettingService(settings)
	agentService := controlservice.NewAgentService(repos.Agent, repos.StepRun, settingService, repos.JobRun)
	interceptor.SetTokenVerifier(interceptor.NewAgentTokenVerifier(agentService, repos.Agent, settingService, nil))
	token, err := agentService.GenerateAgentToken(ctx, agentID)
	if err != nil {
		t.Fatalf("generate ephemeral Agent credential with production service: %v", err)
	}
	return token, interceptor.AuthUnaryInterceptor()
}

func realStackRejectInvalidToken(t *testing.T, f *controllerWorkerFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "bearer synthetic-invalid-token"))
	_, err := f.client.AgentClient.ReportJobRunStatus(ctx, &agentv1.ReportJobRunStatusRequest{AgentId: f.conf.Agent.ID, JobRunId: "must-not-exist", Status: 3})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("real authenticated TLS endpoint accepted invalid Agent token: %v", err)
	}
}

func realStackAssertOnce(t *testing.T, f *controllerWorkerFixture, runID, jobID string) {
	t.Helper()
	if got := artifactTestRead(t, filepath.Join(f.conf.Agent.WorkspaceDir, runID, jobID), "executions"); got != "x" {
		t.Fatalf("real Kafka redelivery repeated or lost shell command: %q", got)
	}
}

func realStackWaitLogs(t *testing.T, f *controllerWorkerFixture, steps []model.StepRun, expected map[string]string) {
	t.Helper()
	var stepIDs []string
	for _, step := range steps {
		stepIDs = append(stepIDs, step.StepRunID)
	}
	f.wait("production Kafka log consumer persists stdout/stderr in MySQL", func() bool {
		rows, err := f.repos.Log.ListByStepRunIDs(f.ctx, stepIDs, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		found := make(map[string]bool)
		for _, row := range rows {
			if row.LogID == "" || row.AgentID != f.conf.Agent.ID || row.LineNumber <= 0 {
				t.Fatalf("log lost persisted identity/ordering: %+v", row)
			}
			for marker, stream := range expected {
				if strings.Contains(row.Content, marker) && row.Stream == stream {
					found[marker] = true
				}
			}
		}
		return len(found) == len(expected)
	})
}

func realStackCleanupDatabase(t *testing.T, f *controllerWorkerFixture, storageID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// 仅清理本次唯一 Pipeline 和 Agent 的记录，不删除 schema 或其他测试的数据
	var stepIDs []string
	if err := f.db.WithContext(ctx).Model(&model.StepRun{}).Where("pipeline_id = ?", f.pipelineID).Pluck("step_run_id", &stepIDs).Error; err != nil {
		t.Errorf("find acceptance log owners for cleanup: %v", err)
		return
	}
	if len(stepIDs) > 0 {
		if err := f.db.WithContext(ctx).Where("step_run_id IN ?", stepIDs).Delete(&model.StepRunLog{}).Error; err != nil {
			t.Errorf("delete acceptance logs: %v", err)
		}
	}
	for _, entity := range []any{&model.ProjectArtifact{}, &model.StepRun{}, &model.JobRun{}, &model.PipelineRun{}, &model.Pipeline{}} {
		if err := f.db.WithContext(ctx).Where("pipeline_id = ?", f.pipelineID).Delete(entity).Error; err != nil {
			t.Errorf("delete acceptance %T: %v", entity, err)
		}
	}
	for _, item := range []struct {
		entity        any
		column, value string
	}{
		{&model.Project{}, "project_id", f.projectID},
		{&model.Agent{}, "agent_id", f.conf.Agent.ID},
		{&model.StorageConfig{}, "storage_id", storageID},
	} {
		if err := f.db.WithContext(ctx).Where(item.column+" = ?", item.value).Delete(item.entity).Error; err != nil {
			t.Errorf("delete acceptance %T: %v", item.entity, err)
		}
	}
}
