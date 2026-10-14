// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	controlconfig "github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/process"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	kafkaconfig "github.com/arcentrix/arcentra/pkg/mq/kafka"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/safe"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// controllerWorkerStorage 只替换对象存储传输，保留真实制品步骤和元数据持久化
// 读写计数和对象快照受锁保护，供异步工作器集成测试使用
type controllerWorkerStorage struct {
	storage.IStorage
	mu                 sync.Mutex
	objects            map[string][]byte
	uploads, downloads int
}

func (s *controllerWorkerStorage) UploadStream(ctx context.Context, key string, input io.ReadSeeker, size int64, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := io.ReadAll(input)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return fmt.Errorf("artifact stream size mismatch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = data
	s.uploads++
	return nil
}

func (s *controllerWorkerStorage) DownloadStream(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("artifact object %s not found", key)
	}
	s.downloads++
	return io.NopCloser(bytes.NewReader(bytes.Clone(data))), nil
}

type controllerWorkerReceipt struct {
	claim   string
	message proto.Message
	payload []byte
}

type controllerWorkerFixtureOptions struct {
	serverTLS *tls.Config
	clientTLS agentgrpc.ClientTLSConfig
	backend   *controllerWorkerBackend
	// 仅供显式验收测试配置真实运行时，不改变普通集成测试的启动路径
	configureAgent func(*config.AgentConfig)
	startWorker    func(context.Context, *config.AgentConfig, *agentgrpc.ClientWrapper, *executor.Manager) (nova.TaskQueue, error)
	processConfig  *controlconfig.AppConfig
	timeout        time.Duration
}

// controllerWorkerBackend 让 opt-in 验收复用同一执行路径，外部依赖必须完整提供
// 零值保持现有快速集成测试的 SQLite/MockCluster/内存对象存储
// 接口仅用于测试夹具注入，生产实现不因此改变
type controllerWorkerBackend struct {
	db             *gorm.DB
	brokers        string
	prepareTopics  func(*testing.T, context.Context, string)
	store          storage.IStorage
	storageConfig  *model.StorageConfig
	reference      taskpayload.ArtifactReference
	logPublisher   executor.LogPublisher
	authentication func(*testing.T, context.Context, *repo.Repositories, string) (string, grpc.UnaryServerInterceptor)
}

type controllerWorkerFixture struct {
	t                   *testing.T
	ctx                 context.Context
	waitTimeout         time.Duration
	db                  *gorm.DB
	client              *agentgrpc.ClientWrapper
	producer            nova.TaskQueue
	tasks               map[string]nova.Task
	beforeTerminal      func(context.Context, proto.Message) error
	observeTerminal     func(context.Context, proto.Message, any, error)
	observeRequest      func(context.Context, any)
	repos               *repo.Repositories
	engine              *process.Process
	worker              *workerQueues
	conf                *config.AgentConfig
	store               *controllerWorkerStorage
	projectID           string
	pipelineID          string
	mu                  sync.Mutex
	receipts            map[string][]controllerWorkerReceipt
	loseProducerACK     bool
	loseStepTerminalACK bool
	lostACKs            int
	beforeStepClaim     func(context.Context)
	loseStepClaimACK    bool
	stepClaimACKLost    chan struct{}
	lostStepClaimACKs   int
}

// newControllerWorkerFixture 使用预先批准的固定身份；不模拟或覆盖动态注册流程
// 仅创建 Project、Pipeline、Agent 和 Storage，所有 JobRun/StepRun 必须由 Submit 创建
func newControllerWorkerFixture(t *testing.T, loseProducerACK bool, options ...controllerWorkerFixtureOptions) *controllerWorkerFixture {
	t.Helper()
	var fixtureOptions controllerWorkerFixtureOptions
	if len(options) > 0 {
		fixtureOptions = options[0]
	}
	backend := fixtureOptions.backend
	if testing.Short() {
		if backend != nil {
			t.Fatal("real-stack acceptance cannot run with -short")
		}
		t.Skip("controller/Agent Kafka wire-protocol integration test")
	}
	timeout, waitTimeout := 45*time.Second, 25*time.Second
	if backend != nil {
		if backend.db == nil || backend.brokers == "" || backend.prepareTopics == nil || backend.store == nil || backend.storageConfig == nil || backend.reference.StorageID == "" || backend.reference.Fingerprint == "" || backend.logPublisher == nil || backend.authentication == nil || fixtureOptions.serverTLS == nil || !fixtureOptions.clientTLS.Enabled {
			t.Fatal("real-stack fixture requires every real dependency and authenticated TLS; no mock fallback")
		}
		timeout, waitTimeout = 3*time.Minute, 60*time.Second
	}
	if fixtureOptions.timeout > 0 {
		timeout = fixtureOptions.timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	root := t.TempDir()
	var db *gorm.DB
	var err error
	if backend != nil {
		db = backend.db
	} else {
		db, err = gorm.Open(sqlite.Open(filepath.Join(root, "controller.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if backend == nil {
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.WithContext(ctx).AutoMigrate(&model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.StepRunLog{}, &model.ProjectArtifact{}, &model.Agent{}, &model.StorageConfig{}); err != nil {
		t.Fatal(err)
	}
	adapter := workerTestDatabase{db}
	repos := &repo.Repositories{
		Pipeline: repo.NewPipelineRepo(adapter), JobRun: repo.NewJobRunRepo(adapter), StepRun: repo.NewStepRunRepo(adapter),
		Log: repo.NewLogRepository(db), Artifact: repo.NewArtifactRepo(adapter), Agent: repo.NewAgentRepo(adapter, nil), Storage: repo.NewStorageRepo(adapter, nil),
	}
	f := &controllerWorkerFixture{t: t, ctx: ctx, waitTimeout: waitTimeout, db: db, tasks: make(map[string]nova.Task), repos: repos, projectID: id.GetUild(), pipelineID: id.GetUild(), receipts: make(map[string][]controllerWorkerReceipt), loseProducerACK: loseProducerACK}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(os.TempDir(), "pipelines", f.pipelineID)) })
	if err := db.WithContext(ctx).Create(&model.Project{ProjectID: f.projectID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repos.Pipeline.Create(ctx, &model.Pipeline{PipelineID: f.pipelineID, ProjectID: f.projectID, Name: "controller-agent-roundtrip", Metadata: "{}", IsEnabled: 1}); err != nil {
		t.Fatal(err)
	}
	agentID := "roundtrip-" + id.GetUild()
	now := time.Now()
	if err := repos.Agent.Create(ctx, &model.Agent{AgentID: agentID, AgentName: "fixed local test identity", Status: 1, IsEnabled: 1, LastHeartbeat: &now, RegisteredBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	storageConfig := &model.StorageConfig{StorageID: "rpc-store", StorageType: "s3", IsEnabled: 1, Config: []byte(`{"endpoint":"https://storage.invalid","bucket":"artifacts","region":"test","basePath":"root","useTLS":true}`)}
	if backend == nil {
		f.store = &controllerWorkerStorage{objects: make(map[string][]byte)}
	}
	var objectStore storage.IStorage = f.store
	reference := artifactRPCReference()
	var brokers string
	authToken := "roundtrip-test-token"
	var authentication grpc.UnaryServerInterceptor
	if backend != nil {
		f.store = nil
		storageConfig, objectStore, reference = backend.storageConfig, backend.store, backend.reference
		brokers = backend.brokers
		backend.prepareTopics(t, ctx, agentID)
		authToken, authentication = backend.authentication(t, ctx, repos, agentID)
		if authToken == "" || authentication == nil {
			t.Fatal("real-stack authentication was not configured")
		}
	} else {
		cluster, err := ckafka.NewMockCluster(1)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cluster.Close)
		brokers = cluster.BootstrapServers()
		for _, prefix := range []string{taskpayload.AgentTopicPrefix(agentID), taskpayload.AgentControlTopicPrefix(agentID)} {
			for _, suffix := range []string{"_PRIORITY_NORMAL", "_PRIORITY_HIGH", "_PRIORITY_LOW", "_TASKS_DELAY_0"} {
				if err := cluster.CreateTopic(strings.ToUpper(prefix+suffix), 1, 1); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := repos.Storage.Create(ctx, storageConfig); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverOptions := []grpc.ServerOption{}
	if fixtureOptions.serverTLS != nil {
		serverOptions = append(serverOptions, grpc.Creds(credentials.NewTLS(fixtureOptions.serverTLS)))
	}
	interceptors := []grpc.UnaryServerInterceptor{}
	if authentication != nil {
		interceptors = append(interceptors, authentication)
	}
	interceptors = append(interceptors, func(ctx context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		if authentication == nil {
			if values := md.Get("authorization"); len(values) != 1 || values[0] != "bearer "+authToken {
				return nil, status.Error(codes.Unauthenticated, "fixed test token required")
			}
			ctx = agentidentity.WithIdentity(ctx, agentidentity.Identity{ID: agentID})
		}
		var terminalMessage proto.Message
		f.mu.Lock()
		observeRequest := f.observeRequest
		f.mu.Unlock()
		if observeRequest != nil {
			observeRequest(ctx, request)
		}
		switch req := request.(type) {
		case *agentv1.ReportJobRunStatusRequest:
			if req.Status >= 4 {
				terminalMessage = req
			}
		case *agentv1.ReportStepRunStatusRequest:
			if req.Status >= 4 {
				terminalMessage = req
			}
		}
		f.mu.Lock()
		beforeTerminal, observeTerminal := f.beforeTerminal, f.observeTerminal
		f.mu.Unlock()
		if terminalMessage != nil && beforeTerminal != nil {
			if err := beforeTerminal(ctx, terminalMessage); err != nil {
				if observeTerminal != nil {
					observeTerminal(ctx, terminalMessage, nil, err)
				}
				return nil, err
			}
		}
		if req, ok := request.(*agentv1.ReportStepRunStatusRequest); ok && int32(req.Status) == 3 {
			f.mu.Lock()
			hook := f.beforeStepClaim
			f.mu.Unlock()
			if hook != nil {
				hook(ctx)
			}
		}
		response, err := handler(ctx, request)
		if terminalMessage != nil && observeTerminal != nil {
			observeTerminal(ctx, terminalMessage, response, err)
		}
		if err != nil {
			t.Logf("control service rejected %T: %v", request, err)
			return response, err
		}
		if req, ok := request.(*agentv1.ReportStepRunStatusRequest); ok && int32(req.Status) == 3 {
			f.mu.Lock()
			lose := f.loseStepClaimACK
			if lose {
				f.lostStepClaimACKs++
				if f.lostStepClaimACKs == 1 {
					close(f.stepClaimACKLost)
				}
			}
			f.mu.Unlock()
			if lose {
				return nil, status.Error(codes.Unavailable, "step claim committed but ACK lost")
			}
		}
		var key string
		var message proto.Message
		producer := false
		switch req := request.(type) {
		case *agentv1.ReportJobRunStatusRequest:
			if req.Status >= 4 {
				key, message, producer = "job/"+req.JobRunId, req, len(req.Artifacts) > 0
			}
		case *agentv1.ReportStepRunStatusRequest:
			if req.Status >= 4 {
				key, message = "step/"+req.StepRunId, req
			}
		}
		if message != nil {
			claims := md.Get(executionClaimMetadataKey)
			if len(claims) != 1 {
				return nil, status.Error(codes.InvalidArgument, "execution claim missing")
			}
			payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
			if err != nil {
				return nil, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.receipts[key] = append(f.receipts[key], controllerWorkerReceipt{claim: claims[0], message: proto.Clone(message), payload: payload})
			// 服务端已提交事务后才丢弃 ACK；真实工作器负责从持久收据恢复
			_, stepTerminal := message.(*agentv1.ReportStepRunStatusRequest)
			if producer && f.loseProducerACK || stepTerminal && f.loseStepTerminalACK {
				if producer {
					f.loseProducerACK = false
				} else {
					f.loseStepTerminalACK = false
				}
				f.lostACKs++
				return nil, status.Error(codes.Unavailable, "committed terminal acknowledgement lost")
			}
		}
		return response, nil
	})
	serverOptions = append(serverOptions, grpc.ChainUnaryInterceptor(interceptors...))
	server := grpc.NewServer(serverOptions...)
	agentv1.RegisterAgentServiceServer(server, controlservice.NewAgentServiceImpl(controlservice.NewAgentService(repos.Agent, repos.StepRun, nil, repos.JobRun), repos.Storage))
	safe.Go(func() { _ = server.Serve(listener) })
	t.Cleanup(server.Stop)
	client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: listener.Addr().String(), Token: authToken, ReadWriteTimeout: 2, TLS: fixtureOptions.clientTLS})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	f.client = client
	f.conf = &config.AgentConfig{
		Grpc:         config.GrpcConfig{ServerAddr: listener.Addr().String(), Token: authToken, TLS: fixtureOptions.clientTLS},
		Agent:        config.AgentInfo{ID: agentID, Mode: "baremetal", WorkspaceDir: filepath.Join(root, "agent"), JobTimeout: 30},
		MessageQueue: config.MqConfig{Kafka: kafkaconfig.Config{BootstrapServers: brokers, SecurityProtocol: "PLAINTEXT"}},
		TaskQueue:    nova.TaskQueueConfig{MessageFormat: "json", MessageCodec: "json", DelaySlotCount: 1, DelaySlotDuration: 3600, SessionTimeout: 6000, MaxPollInterval: 120000},
	}
	if fixtureOptions.configureAgent != nil {
		fixtureOptions.configureAgent(f.conf)
	}
	SetStorage(objectStore, reference)
	t.Cleanup(func() { SetStorage(nil) })
	manager := executor.NewExecutorManager()
	manager.Register(executor.NewShellExecutor())
	if backend != nil {
		manager.SetLogPublisher(backend.logPublisher)
	} else {
		manager.SetLogPublisher(&capturedLogs{})
	}
	startWorker := StartWorker
	if fixtureOptions.startWorker != nil {
		startWorker = fixtureOptions.startWorker
	}
	queue, err := startWorker(ctx, f.conf, client, manager)
	if err != nil {
		t.Fatal(err)
	}
	f.worker = queue.(*workerQueues)
	t.Cleanup(func() {
		if err := f.worker.Stop(); err != nil {
			t.Error(err)
		}
	})
	producer, err := process.ProvideTaskQueueProducer(&controlconfig.AppConfig{
		MessageQueue: controlconfig.MessageQueueConfig{Kafka: kafkaconfig.Config{BootstrapServers: brokers, SecurityProtocol: "PLAINTEXT"}},
		TaskQueue:    nova.TaskQueueConfig{MessageFormat: "sonic", MessageCodec: "sonic"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Stop() })
	f.producer = producer
	f.engine = process.NewProcess(repos, nil, &controllerWorkerRecordingQueue{TaskQueue: producer, fixture: f}, nil, &log.Logger{SugaredLogger: zap.NewNop().Sugar()}, fixtureOptions.processConfig, nil)
	t.Cleanup(f.engine.Stop)
	return f
}

func (f *controllerWorkerFixture) submit(jobs ...*spec.Job) *model.PipelineRun {
	f.t.Helper()
	run := &model.PipelineRun{RunID: id.GetUild(), PipelineID: f.pipelineID, Status: model.PipelineStatusPending, Env: `{"ROUNDTRIP_VALUE":"controller-agent-artifact"}`, TotalJobs: len(jobs)}
	if err := f.repos.Pipeline.CreateRun(f.ctx, run); err != nil {
		f.t.Fatal(err)
	}
	if err := f.engine.Submit(run, &spec.Pipeline{Namespace: "controller-agent", Jobs: jobs}); err != nil {
		f.t.Fatal(err)
	}
	return run
}

func (f *controllerWorkerFixture) wait(description string, predicate func() bool) {
	f.t.Helper()
	deadline := time.NewTimer(f.waitTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !predicate() {
		select {
		case <-f.ctx.Done():
			f.t.Fatalf("%s: %v", description, f.ctx.Err())
		case <-deadline.C:
			f.t.Fatalf("timed out waiting for %s", description)
		case <-ticker.C:
		}
	}
}

func (f *controllerWorkerFixture) waitRun(runID string, wanted int) *model.PipelineRun {
	f.t.Helper()
	var result *model.PipelineRun
	f.wait("pipeline terminal status", func() bool {
		var err error
		result, err = f.repos.Pipeline.GetRun(f.ctx, runID)
		if err != nil {
			f.t.Fatal(err)
		}
		if result.Status == wanted {
			return true
		}
		if result.Status == model.PipelineStatusFailed || result.Status == model.PipelineStatusSuccess || result.Status == model.PipelineStatusCancelled {
			jobs, _ := f.repos.JobRun.ListByPipelineRunID(f.ctx, runID)
			for _, job := range jobs {
				f.t.Logf("job %s status=%d error=%s", job.JobName, job.Status, job.ErrorMessage)
			}
			f.t.Fatalf("pipeline status=%d want=%d", result.Status, wanted)
		}
		return false
	})
	return result
}

func controllerWorkerShell(name, command string) *spec.Step {
	return &spec.Step{Name: name, Uses: "shell", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"run": command})}
}

func (f *controllerWorkerFixture) assertReceipt(kind, runID, claim, digest, pipelineRunID, jobRunID string) {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	receipts := f.receipts[kind+"/"+runID]
	if len(receipts) == 0 || claim == "" || len(digest) != 64 {
		f.t.Fatalf("missing committed %s identity: run=%s claim=%q digest=%q receipts=%d", kind, runID, claim, digest, len(receipts))
	}
	original := receipts[0]
	for _, receipt := range receipts {
		if receipt.claim != claim || !bytes.Equal(receipt.payload, original.payload) {
			f.t.Fatalf("%s replay changed execution identity or terminal payload", kind)
		}
	}
	hash := sha256.New()
	for _, value := range []string{"arcentra-terminal-report-v1", string(original.message.ProtoReflect().Descriptor().FullName()), claim, f.pipelineID, pipelineRunID, jobRunID} {
		_, _ = hash.Write(binary.BigEndian.AppendUint64(nil, uint64(len(value))))
		_, _ = hash.Write([]byte(value))
	}
	_, _ = hash.Write(original.payload)
	if expected := hex.EncodeToString(hash.Sum(nil)); digest != expected {
		f.t.Fatalf("%s persisted digest=%s want=%s", kind, digest, expected)
	}
}

func TestControllerWorkerArtifactDAGRoundTrip(t *testing.T) {
	for _, name := range []string{"success", "committed_job_ack_loss", "committed_step_ack_loss", "committed_both_ack_loss"} {
		t.Run(name, func(t *testing.T) {
			f := newControllerWorkerFixture(t, name == "committed_job_ack_loss" || name == "committed_both_ack_loss")
			f.mu.Lock()
			f.loseStepTerminalACK = name == "committed_step_ack_loss" || name == "committed_both_ack_loss"
			f.mu.Unlock()
			run := f.submit(
				&spec.Job{Name: "build", Steps: []*spec.Step{
					controllerWorkerShell("produce", `printf x >> executions; mkdir -p dist; printf '%s' "$ROUNDTRIP_VALUE" > dist/result.txt`),
					{Name: "publish", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"paths": []any{"dist"}, "expireIn": "7d"})},
				}},
				&spec.Job{Name: "consume", DependsOn: []string{"build"}, Steps: []*spec.Step{
					{Name: "fetch", Uses: "builtin:artifacts", Action: "download", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"job": "build", "paths": []any{"dist/*.txt"}})},
					controllerWorkerShell("verify", `test "$(cat dist/result.txt)" = "$ROUNDTRIP_VALUE" && printf x >> executions`),
				}},
			)
			completed := f.waitRun(run.RunID, model.PipelineStatusSuccess)
			if completed.CompletedJobs != 2 || completed.FailedJobs != 0 || completed.EndTime == nil {
				t.Fatalf("pipeline completion not persisted: %+v", completed)
			}
			jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
			if err != nil || len(jobs) != 2 {
				t.Fatalf("jobs=%d error=%v", len(jobs), err)
			}
			byName := make(map[string]model.JobRun)
			for _, job := range jobs {
				byName[job.JobName] = *job
				if job.Status != model.JobRunStatusSuccess || job.AgentID != f.conf.Agent.ID || job.PipelineID != f.pipelineID || job.StartTime == nil || job.EndTime == nil {
					t.Fatalf("job lost controller/Agent identity: %+v", job)
				}
				f.assertReceipt("job", job.JobRunID, job.ClaimID, job.TerminalReportDigest, run.RunID, job.JobRunID)
				if marker := artifactTestRead(t, filepath.Join(f.conf.Agent.WorkspaceDir, run.RunID, job.JobRunID), "executions"); marker != "x" {
					t.Fatalf("%s command repeated or missing: %q", job.JobName, marker)
				}
			}
			steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID, PageSize: 100})
			if err != nil || count != 4 {
				t.Fatalf("steps=%d error=%v", count, err)
			}
			var upload model.StepRun
			for _, step := range steps {
				if step.Status != 4 || step.Attempt != 1 || step.PreviousStepRunID != "" || step.AgentID != f.conf.Agent.ID || step.PipelineID != f.pipelineID || step.ExitCode == nil || *step.ExitCode != 0 {
					t.Fatalf("step lost execution/attempt identity: %+v", step)
				}
				f.assertReceipt("step", step.StepRunID, step.ClaimID, step.TerminalReportDigest, run.RunID, step.JobRunID)
				if step.Name == "publish" {
					upload = step
				}
			}
			rows, count, err := f.repos.Artifact.List(f.ctx, repo.ArtifactQuery{ProjectID: f.projectID, Limit: 20})
			if err != nil || count != 1 {
				t.Fatalf("artifact rows=%d error=%v", count, err)
			}
			entry := rows[0]
			build := byName["build"]
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte("controller-agent-artifact")))
			key := fmt.Sprintf("artifacts/%s/%s/%s/%s/dist/result.txt", run.RunID, build.JobRunID, upload.StepRunID, digest)
			ref := artifactRPCReference()
			if entry.ArtifactID != artifactutil.ID(f.projectID, run.RunID, build.JobRunID, upload.StepRunID, "dist/result.txt") || entry.PipelineID != f.pipelineID || entry.PipelineRunID != run.RunID || entry.JobRunID != build.JobRunID || entry.StepRunID != upload.StepRunID || upload.JobRunID != build.JobRunID || entry.Name != "dist/result.txt" || entry.SHA256 != digest || entry.Size != int64(len("controller-agent-artifact")) || entry.StorageID != ref.StorageID || entry.StorageFingerprint != ref.Fingerprint || entry.StorageType != "s3" || entry.StoragePath != key || entry.ExpiresAt == nil || entry.ExpiresAt.Before(time.Now().Add(6*24*time.Hour)) {
				t.Fatalf("artifact did not preserve exact producing attempt/storage identity: %+v", entry)
			}
			var uris map[string]string
			var references map[string]taskpayload.ArtifactReference
			if err := sonic.UnmarshalString(build.ArtifactURIs, &uris); err != nil {
				t.Fatal(err)
			}
			if err := sonic.UnmarshalString(build.ArtifactReferences, &references); err != nil {
				t.Fatal(err)
			}
			if len(uris) != 1 || uris[entry.Name] != key || references[entry.Name].StorageID != ref.StorageID || references[entry.Name].Fingerprint != ref.Fingerprint || references[entry.Name].ExpiresAt != entry.ExpiresAt.Unix() {
				t.Fatalf("controller artifact backflow metadata mismatch: %v %v", uris, references)
			}
			consumerWorkspace := filepath.Join(f.conf.Agent.WorkspaceDir, run.RunID, byName["consume"].JobRunID)
			if got := artifactTestRead(t, consumerWorkspace, "dist/result.txt"); got != "controller-agent-artifact" {
				t.Fatalf("downstream DAG did not consume controller artifact: %q", got)
			}
			f.wait("terminal outbox drain", func() bool {
				f.worker.reports.mu.Lock()
				defer f.worker.reports.mu.Unlock()
				return len(f.worker.reports.entries) == 0
			})
			f.store.mu.Lock()
			if f.store.uploads != 1 || f.store.downloads < 1 || string(f.store.objects[key]) != "controller-agent-artifact" || len(f.store.objects) != 1 {
				t.Errorf("unexpected storage roundtrip: uploads=%d downloads=%d objects=%d", f.store.uploads, f.store.downloads, len(f.store.objects))
			}
			f.store.mu.Unlock()
			// Recheck after drain so every replay is compared with the persisted digest.
			for _, job := range jobs {
				f.assertReceipt("job", job.JobRunID, job.ClaimID, job.TerminalReportDigest, run.RunID, job.JobRunID)
			}
			for _, step := range steps {
				f.assertReceipt("step", step.StepRunID, step.ClaimID, step.TerminalReportDigest, run.RunID, step.JobRunID)
			}
			f.mu.Lock()
			jobReplayed := len(f.receipts["job/"+build.JobRunID]) >= 2
			stepReplayed := false
			for _, step := range steps {
				stepReplayed = stepReplayed || len(f.receipts["step/"+step.StepRunID]) >= 2
			}
			wantLost := 0
			if name == "committed_job_ack_loss" || name == "committed_both_ack_loss" {
				wantLost++
				if !jobReplayed {
					t.Error("committed job ACK loss did not replay")
				}
			}
			if name == "committed_step_ack_loss" || name == "committed_both_ack_loss" {
				wantLost++
				if !stepReplayed {
					t.Error("committed step ACK loss did not replay")
				}
			}
			if f.lostACKs != wantLost {
				t.Errorf("lost ACKs=%d want=%d", f.lostACKs, wantLost)
			}
			f.mu.Unlock()
		})
	}
}

func TestControllerWorkerCancelRunUsesIndependentControlQueue(t *testing.T) {
	for _, attempts := range []int{1, 2} {
		t.Run(fmt.Sprintf("attempts_%d", attempts), func(t *testing.T) {
			testControllerWorkerCancellation(t, attempts)
		})
	}
}

func testControllerWorkerCancellation(t *testing.T, attempts int) {
	f := newControllerWorkerFixture(t, false)
	busyJob := &spec.Job{Name: "busy", Steps: []*spec.Step{
		controllerWorkerShell("wait", "printf x > started; sleep 30 & wait; printf unexpected > after-cancel"),
		controllerWorkerShell("later", "printf unexpected > later"),
	}}
	if attempts > 1 {
		busyJob.Retry = &spec.Retry{Mode: "whole_job", MaxAttempts: int32(attempts), Idempotent: true, AllowedExitCodes: []int32{9}}
	}
	run := f.submit(
		busyJob,
		&spec.Job{Name: "dependent", DependsOn: []string{"busy"}, Steps: []*spec.Step{controllerWorkerShell("never", "printf unexpected > dependent")}},
	)
	var busy model.JobRun
	f.wait("Agent shell start", func() bool {
		jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) != 1 {
			return false
		}
		busy = *jobs[0]
		_, err = os.Stat(filepath.Join(f.conf.Agent.WorkspaceDir, run.RunID, busy.JobRunID, "started"))
		return err == nil && busy.Status == model.JobRunStatusRunning
	})
	if busy.AgentID != f.conf.Agent.ID || busy.ClaimID == "" {
		t.Fatalf("busy job was not claimed by the selected Agent: %+v", busy)
	}
	if err := f.engine.CancelRun(run.RunID); err != nil {
		t.Fatal(err)
	}
	f.waitRun(run.RunID, model.PipelineStatusCancelled)
	f.wait("independent Agent control consumer stops the running shell", func() bool {
		_, running := runningJobRunCancel.Load(busy.JobRunID)
		return !running
	})
	jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
	if err != nil || len(jobs) != 1 || jobs[0].Status != model.JobRunStatusCancelled {
		t.Fatalf("cancelled DAG dispatched downstream or lost status: jobs=%+v error=%v", jobs, err)
	}
	steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID})
	if err != nil || count != int64(2*attempts) {
		t.Fatalf("cancelled steps=%d error=%v", count, err)
	}
	for _, step := range steps {
		if step.Status != 6 || step.EndTime == nil || step.JobRunID != busy.JobRunID || step.AgentID != f.conf.Agent.ID {
			t.Fatalf("step cancellation not persisted: %+v", step)
		}
	}
	workspace := filepath.Join(f.conf.Agent.WorkspaceDir, run.RunID, busy.JobRunID)
	for _, name := range []string{"after-cancel", "later"} {
		if _, err := os.Stat(filepath.Join(workspace, name)); !os.IsNotExist(err) {
			t.Fatalf("cancelled command continued: %s (%v)", name, err)
		}
	}
	// 取消不能留下永远无法确认的终态，使同一 Agent 的后续任务停滞
	next := f.submit(&spec.Job{Name: "after-cancel", Steps: []*spec.Step{controllerWorkerShell("healthy", "printf x > executions")}})
	f.waitRun(next.RunID, model.PipelineStatusSuccess)
	f.wait("cancelled terminal outbox drain", func() bool {
		f.worker.reports.mu.Lock()
		defer f.worker.reports.mu.Unlock()
		return len(f.worker.reports.entries) == 0
	})

}

func TestControllerWorkerCancellationDuringStepClaim(t *testing.T) {
	for _, window := range []string{"before_commit", "committed_ack_loss"} {
		t.Run(window, func(t *testing.T) {
			f := newControllerWorkerFixture(t, false)
			reached, release := make(chan struct{}), make(chan struct{})
			var releaseOnce, reachedOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			f.mu.Lock()
			if window == "before_commit" {
				f.beforeStepClaim = func(ctx context.Context) {
					reachedOnce.Do(func() { close(reached) })
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
			} else {
				f.loseStepClaimACK, f.stepClaimACKLost = true, reached
			}
			f.mu.Unlock()
			run := f.submit(&spec.Job{Name: "claim-race", Steps: []*spec.Step{controllerWorkerShell("never", "printf unexpected > executed")}})
			select {
			case <-reached:
			case <-time.After(20 * time.Second):
				t.Fatal("step claim did not reach injected window")
			}
			if err := f.engine.CancelRun(run.RunID); err != nil {
				t.Fatal(err)
			}
			f.waitRun(run.RunID, model.PipelineStatusCancelled)
			jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
			if err != nil || len(jobs) != 1 {
				t.Fatalf("jobs=%d error=%v", len(jobs), err)
			}
			job := jobs[0]
			f.wait("control topic cancellation during step claim", func() bool { _, ok := cancelledJobs.Load(job.JobRunID); return ok })
			releaseOnce.Do(func() { close(release) })
			f.wait("cancelled claim receipt drain", func() bool {
				stored, err := f.repos.JobRun.GetByJobRunID(f.ctx, job.JobRunID)
				if err != nil {
					t.Fatal(err)
				}
				if stored.TerminalReportDigest == "" {
					return false
				}
				f.worker.reports.mu.Lock()
				defer f.worker.reports.mu.Unlock()
				return len(f.worker.reports.entries) == 0
			})
			steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID})
			if err != nil || count != 1 || steps[0].Status != 6 || len(steps[0].TerminalReportDigest) != 64 {
				t.Fatalf("step claim cancellation lost: %+v %v", steps, err)
			}
			if window == "before_commit" && steps[0].ClaimID != "" {
				t.Fatal("unstarted step acquired execution ownership")
			}
			if window == "committed_ack_loss" && steps[0].ClaimID != agentidentity.StepClaimID(job.ClaimID, steps[0].StepRunID) {
				t.Fatal("lost claim ACK changed the original step claim")
			}
			if _, err := os.Stat(filepath.Join(f.conf.Agent.WorkspaceDir, run.RunID, job.JobRunID, "executed")); !os.IsNotExist(err) {
				t.Fatalf("unconfirmed step command executed: %v", err)
			}
			f.mu.Lock()
			f.beforeStepClaim, f.loseStepClaimACK = nil, false
			f.mu.Unlock()
			next := f.submit(&spec.Job{Name: "healthy", Steps: []*spec.Step{controllerWorkerShell("once", "printf x > executions")}})
			f.waitRun(next.RunID, model.PipelineStatusSuccess)
		})
	}
}

func TestJobDerivedStepClaimSurvivesFreshReceiptContext(t *testing.T) {
	outbox := &terminalOutbox{binding: terminalBinding("original-agent", "original-controller")}
	reportCtx := withTerminalOutbox(context.Background(), outbox)
	ctx := withJobExecutionClaim(reportCtx, "derived-claim-job")
	claim := executionClaimID(ctx, "step", "derived-claim-step")
	t.Cleanup(func() {
		completeExecutionClaim(reportCtx, "job", "derived-claim-job")
		completeExecutionClaim(reportCtx, "step", "derived-claim-step")
	})
	if got := executionClaimID(reportCtx, "step", "derived-claim-step"); got != claim {
		t.Fatalf("pending receipt replay changed derived claim: %s != %s", got, claim)
	}
	otherCtx := withTerminalOutbox(context.Background(), &terminalOutbox{binding: terminalBinding("other-agent", "original-controller")})
	other := executionClaimID(otherCtx, "step", "derived-claim-step")
	completeExecutionClaim(otherCtx, "step", "derived-claim-step")
	if other == claim {
		t.Fatal("derived receipt claim crossed Agent/controller binding")
	}
	completeExecutionClaim(reportCtx, "step", "derived-claim-step")
	if _, ok := executionClaims.Load("step/" + receiptKey(reportCtx, "step", "derived-claim-step")); ok {
		t.Fatal("confirmed derived claim was not cleaned up")
	}
}
