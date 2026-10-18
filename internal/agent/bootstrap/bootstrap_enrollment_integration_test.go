// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/identitystore"
	agentservice "github.com/arcentrix/arcentra/internal/agent/service"
	controlconfig "github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/process"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/internal/shared/grpc/interceptor"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/cron"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	kafkaconfig "github.com/arcentrix/arcentra/pkg/mq/kafka"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/safe"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const enrollmentTestToken = "art_synthetic_integration_enrollment_only"

type enrollmentDatabase struct{ db *gorm.DB }

func (d enrollmentDatabase) Database() *gorm.DB { return d.db }

type enrollmentReceipt struct {
	claim   string
	payload []byte
}

// enrollmentFixture 使用真实控制服务、认证拦截器、TLS 和磁盘数据库；故障仅丢弃已提交的响应
// 所有令牌、HMAC 密钥和证书均为测试夹具，绝不读取操作者配置或连接外部服务
// 测试不并行，因为生产认证拦截器和心跳调度器使用进程级配置
type enrollmentFixture struct {
	t       *testing.T
	ctx     context.Context
	root    string
	db      *gorm.DB
	repos   *repo.Repositories
	setting repo.ISettingRepository
	address string
	tls     agentgrpc.ClientTLSConfig

	mu                  sync.Mutex
	registrations       []*agentv1.RegisterRequest
	pendingBeforeRPC    bool
	verifiedTLS         bool
	loseRegistrationACK bool
	loseStepACK         bool
	receipts            map[string][]enrollmentReceipt
}

func newEnrollmentFixture(t *testing.T, autoApprove bool) *enrollmentFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	f := &enrollmentFixture{t: t, ctx: ctx, root: t.TempDir(), receipts: make(map[string][]enrollmentReceipt)}
	db, err := gorm.Open(sqlite.Open(filepath.Join(f.root, "controller.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	f.db = db
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.WithContext(ctx).AutoMigrate(&model.Agent{}, &model.Setting{}, &model.RegistrationToken{}, &model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.StepRunLog{}, &model.ProjectArtifact{}); err != nil {
		t.Fatal(err)
	}
	adapter := enrollmentDatabase{db}
	f.repos = &repo.Repositories{Agent: repo.NewAgentRepo(adapter, nil), Pipeline: repo.NewPipelineRepo(adapter), JobRun: repo.NewJobRunRepo(adapter), StepRun: repo.NewStepRunRepo(adapter), Log: repo.NewLogRepository(db), Artifact: repo.NewArtifactRepo(adapter)}
	f.setting = repo.NewSettingRepo(adapter, nil)
	f.setSecret("synthetic-controller-hmac-first")
	approval := []byte(`{"auto_approve":false}`)
	if autoApprove {
		approval = []byte(`{"auto_approve":true}`)
	}
	if err := f.setting.Upsert(ctx, &model.Setting{Name: consts.SettingNameAgentAutoApprove, Value: approval}); err != nil {
		t.Fatal(err)
	}
	tokens := repo.NewRegistrationTokenRepo(adapter)
	hash, err := bcrypt.GenerateFromPassword([]byte(enrollmentTestToken), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := tokens.Create(ctx, &model.RegistrationToken{TokenHash: string(hash), IsActive: 1, Description: "synthetic integration fixture"}); err != nil {
		t.Fatal(err)
	}
	settings := controlservice.NewSettingService(f.setting)
	agents := controlservice.NewAgentService(f.repos.Agent, f.repos.StepRun, settings, f.repos.JobRun)
	agents.SetRegistrationTokenService(controlservice.NewRegistrationTokenService(tokens))
	interceptor.SetTokenVerifier(interceptor.NewAgentTokenVerifier(agents, f.repos.Agent, settings, nil))
	serverTLS, clientTLS := enrollmentTLS(t, f.root)
	f.tls = clientTLS
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.address = listener.Addr().String()
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)), grpc.ChainUnaryInterceptor(f.observe, interceptor.AuthUnaryInterceptor()))
	agentv1.RegisterAgentServiceServer(server, controlservice.NewAgentServiceImpl(agents, nil))
	safe.Go(func() { _ = server.Serve(listener) })
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	cron.Init(nil)
	return f
}

func (f *enrollmentFixture) setSecret(secret string) {
	f.t.Helper()
	if err := f.setting.Upsert(f.ctx, &model.Setting{Name: consts.SettingNameAgentSecretKey, Value: []byte(`{"salt":"synthetic-salt","secret_key":"` + secret + `"}`)}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *enrollmentFixture) observe(ctx context.Context, request any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	if req, ok := request.(*agentv1.RegisterRequest); ok {
		p, hasPeer := peer.FromContext(ctx)
		tlsVerified := false
		if hasPeer {
			_, tlsVerified = p.AuthInfo.(credentials.TLSInfo)
		}
		f.mu.Lock()
		f.registrations = append(f.registrations, proto.Clone(req).(*agentv1.RegisterRequest))
		f.verifiedTLS = tlsVerified
		if req.RegistrationToken != "" {
			pending, err := os.ReadFile(filepath.Join(f.root, "private-identity", "enrollment.pending"))
			f.pendingBeforeRPC = err == nil && !bytes.Contains(pending, []byte(req.RegistrationToken))
		}
		f.mu.Unlock()
	}
	response, err := next(ctx, request)
	if err != nil {
		return response, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := request.(*agentv1.RegisterRequest); ok && f.loseRegistrationACK {
		return nil, status.Error(codes.Unavailable, "synthetic registration ACK loss after controller commit")
	}
	var key string
	var terminal proto.Message
	switch req := request.(type) {
	case *agentv1.ReportStepRunStatusRequest:
		if req.Status >= 4 {
			key, terminal = "step/"+req.StepRunId, req
		}
	case *agentv1.ReportJobRunStatusRequest:
		if req.Status >= 4 {
			key, terminal = "job/"+req.JobRunId, req
		}
	}
	if terminal != nil {
		md, _ := metadata.FromIncomingContext(ctx)
		claims := md.Get("x-arcentra-claim-id")
		if len(claims) != 1 {
			return nil, status.Error(codes.InvalidArgument, "test observed missing execution claim")
		}
		payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(terminal)
		if err != nil {
			return nil, err
		}
		f.receipts[key] = append(f.receipts[key], enrollmentReceipt{claim: claims[0], payload: payload})
		if strings.HasPrefix(key, "step/") && f.loseStepACK {
			return nil, status.Error(codes.Unavailable, "synthetic terminal ACK loss after controller commit")
		}
	}
	return response, nil
}

func enrollmentTLS(t *testing.T, directory string) (*tls.Config, agentgrpc.ClientTLSConfig) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic enrollment test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"controller.synthetic.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(directory, "test-ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}}}, agentgrpc.ClientTLSConfig{Enabled: true, CAFile: caFile, ServerName: "controller.synthetic.test"}
}

func (f *enrollmentFixture) lifecycle() (*Agent, func()) {
	f.t.Helper()
	conf := &config.AgentConfig{
		Agent:     config.AgentInfo{Name: "synthetic dynamically enrolled worker", Mode: "baremetal", WorkspaceDir: filepath.Join(f.root, "workspace"), JobTimeout: 30, IdentityPersistence: config.IdentityPersistenceConfig{Enabled: true, Directory: filepath.Join(f.root, "private-identity")}},
		Grpc:      config.GrpcConfig{ServerAddr: f.address, RegistrationToken: enrollmentTestToken, TLS: f.tls, ReadWriteTimeout: 2},
		TaskQueue: nova.TaskQueueConfig{DelaySlotCount: 1, DelaySlotDuration: 3600, SessionTimeout: 6000, MaxPollInterval: 120000},
	}
	client, err := agentgrpc.NewGrpcClient(*config.ProvideGrpcClientConfig(conf))
	if err != nil {
		f.t.Fatal(err)
	}
	app := &Agent{AgentConf: conf, GrpcClient: client, AgentService: agentservice.NewAgentServiceImpl(conf, client, nil)}
	var once sync.Once
	closeLifecycle := func() {
		once.Do(func() {
			app.ready.Store(false)
			if app.TaskQueue != nil {
				if err := app.TaskQueue.Stop(); err != nil {
					f.t.Error(err)
				}
			}
			if app.logPublisher != nil {
				app.logPublisher.Close()
			}
			if app.Outbox != nil {
				if err := app.Outbox.Close(); err != nil {
					f.t.Error(err)
				}
			}
			if err := app.AgentService.CloseIdentity(); err != nil {
				f.t.Error(err)
			}
			if err := client.Close(); err != nil {
				f.t.Error(err)
			}
		})
	}
	f.t.Cleanup(closeLifecycle)
	return app, closeLifecycle
}

func (f *enrollmentFixture) assertRegistrations(count int, identity, token string) {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.registrations) != count || !f.pendingBeforeRPC || !f.verifiedTLS {
		f.t.Fatalf("registration observations: count=%d want=%d durable-pending=%v TLS=%v", len(f.registrations), count, f.pendingBeforeRPC, f.verifiedTLS)
	}
	first := f.registrations[0]
	if first.AgentId != "" || first.Token != "" || first.RegistrationToken != enrollmentTestToken {
		f.t.Fatal("first request did not use genuine dynamic enrollment")
	}
	for _, req := range f.registrations[1:] {
		if req.AgentId != identity || req.Token != token || req.RegistrationToken != "" {
			f.t.Fatal("restored registration changed identity or fell back to dynamic enrollment")
		}
	}
	_, total, err := f.repos.Agent.List(f.ctx, 1, 10)
	if err != nil || total != 1 {
		f.t.Fatalf("controller Agent rows=%d want=1 error=%v", total, err)
	}
}

func enrollmentFile(t *testing.T, directory, name string, mode os.FileMode) ([]byte, os.FileInfo) {
	t.Helper()
	path := filepath.Join(directory, name)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("%s mode=%v want=%v", name, info.Mode().Perm(), mode)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data, info
}

func assertUnchangedIdentity(t *testing.T, directory string, before []byte, original os.FileInfo) {
	t.Helper()
	after, current := enrollmentFile(t, directory, "identity.json", 0600)
	if !bytes.Equal(before, after) || !os.SameFile(original, current) || !original.ModTime().Equal(current.ModTime()) {
		t.Fatal("restart rewrote the persisted identity")
	}
}

func assertEnrollmentBlocked(t *testing.T, app *Agent) {
	t.Helper()
	if app.IsReady() || app.AgentService.Authenticated() || app.TaskQueue != nil || app.Outbox != nil || app.logPublisher != nil {
		t.Fatal("unauthenticated lifecycle became ready or started a publisher/worker")
	}
	if err := app.startWorkers(); err == nil {
		t.Fatal("unauthenticated lifecycle bypassed worker startup gate")
	}
}

func TestDynamicEnrollmentRestoresIdentityWithResidualBootstrapToken(t *testing.T) {
	f := newEnrollmentFixture(t, true)
	first, stopFirst := f.lifecycle()
	if err := first.registerAndStart(); err != nil {
		t.Fatal(err)
	}
	if !first.IsReady() || first.AgentConf.Agent.ID == "" || first.AgentConf.Grpc.Token == "" {
		t.Fatal("real dynamic registration did not authenticate the lifecycle")
	}
	identity, token := first.AgentConf.Agent.ID, first.AgentConf.Grpc.Token
	persisted, original := enrollmentFile(t, first.AgentConf.Agent.IdentityPersistence.Directory, "identity.json", 0600)
	loadedBinding, err := first.GrpcClient.ControllerIdentityBinding()
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		State             string `json:"state"`
		ControllerBinding string `json:"controllerBinding"`
		AgentID           string `json:"agentID"`
		PerAgentToken     string `json:"perAgentToken"`
	}
	if err := sonic.Unmarshal(persisted, &record); err != nil || record.State != "ready" || record.ControllerBinding != loadedBinding || record.AgentID != identity || record.PerAgentToken != token {
		t.Fatal("persisted record does not match the loaded TLS trust and dynamically assigned identity", err)
	}
	if bytes.Contains(persisted, []byte(enrollmentTestToken)) || bytes.Contains(persisted, []byte("synthetic-controller-hmac-first")) {
		t.Fatal("identity persistence retained bootstrap or server credentials")
	}
	if info, err := os.Stat(first.AgentConf.Agent.IdentityPersistence.Directory); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("identity directory is not private", err)
	}
	stopFirst()
	second, _ := f.lifecycle()
	if second.AgentConf.Agent.ID != "" || second.AgentConf.Grpc.Token != "" {
		t.Fatal("test accidentally retained runtime identity")
	}
	if err := second.AgentService.PrepareIdentity(f.ctx); err != nil {
		t.Fatal(err)
	}
	if second.AgentConf.Agent.ID != identity || second.AgentConf.Grpc.Token != token {
		t.Fatal("fresh service did not load the persisted identity")
	}
	if err := second.registerAndStart(); err != nil {
		t.Fatal(err)
	}
	if !second.IsReady() {
		t.Fatal("restored authenticated lifecycle did not become ready")
	}
	f.assertRegistrations(2, identity, token)
	assertUnchangedIdentity(t, second.AgentConf.Agent.IdentityPersistence.Directory, persisted, original)
}

func TestDynamicEnrollmentCommittedACKLossBlocksFreshLifecycle(t *testing.T) {
	f := newEnrollmentFixture(t, true)
	f.mu.Lock()
	f.loseRegistrationACK = true
	f.mu.Unlock()
	first, stopFirst := f.lifecycle()
	if err := first.registerAndStart(); status.Code(err) != codes.Unavailable {
		t.Fatalf("committed ACK loss error=%v", err)
	}
	assertEnrollmentBlocked(t, first)
	if first.AgentConf.Agent.ID != "" || first.AgentConf.Grpc.Token != "" {
		t.Fatal("unacknowledged enrollment published runtime credentials")
	}
	pending, _ := enrollmentFile(t, first.AgentConf.Agent.IdentityPersistence.Directory, "enrollment.pending", 0600)
	record, _ := enrollmentFile(t, first.AgentConf.Agent.IdentityPersistence.Directory, "identity.json", 0600)
	var pendingState struct {
		State   string `json:"state"`
		AgentID string `json:"agentID"`
		Token   string `json:"perAgentToken"`
	}
	if err := sonic.Unmarshal(record, &pendingState); err != nil || pendingState.State != "pending" || pendingState.AgentID != "" || pendingState.Token != "" {
		t.Fatal("lost response created a ready identity", err)
	}
	f.assertRegistrations(1, "", "")
	stopFirst()
	second, _ := f.lifecycle()
	if err := second.AgentService.PrepareIdentity(f.ctx); !errors.Is(err, identitystore.ErrPending) {
		t.Fatalf("fresh lifecycle did not require explicit reconciliation: %v", err)
	}
	started := time.Now()
	second.waitForRegistrationAndStartHeartbeat()
	if time.Since(started) > time.Second {
		t.Fatal("ambiguous enrollment waited for network instead of failing before registration")
	}
	assertEnrollmentBlocked(t, second)
	f.assertRegistrations(1, "", "")
	after, _ := enrollmentFile(t, second.AgentConf.Agent.IdentityPersistence.Directory, "enrollment.pending", 0600)
	if !bytes.Equal(pending, after) {
		t.Fatal("restart changed ambiguous-enrollment evidence")
	}
}

func TestRestoredDynamicIdentityRejectsDisabledPendingAndRevokedAuthorization(t *testing.T) {
	for _, state := range []string{"disabled", "pending_approval", "revoked_hmac"} {
		t.Run(state, func(t *testing.T) {
			f := newEnrollmentFixture(t, state != "pending_approval")
			first, stopFirst := f.lifecycle()
			err := first.registerAndStart()
			if state == "pending_approval" {
				if err == nil {
					t.Fatal("pending dynamic identity started workers")
				}
				assertEnrollmentBlocked(t, first)
			} else if err != nil {
				t.Fatal(err)
			}
			identity, token := first.AgentConf.Agent.ID, first.AgentConf.Grpc.Token
			persisted, original := enrollmentFile(t, first.AgentConf.Agent.IdentityPersistence.Directory, "identity.json", 0600)
			stopFirst()
			switch state {
			case "disabled":
				if err := f.repos.Agent.Patch(f.ctx, identity, map[string]any{"is_enabled": 0}); err != nil {
					t.Fatal(err)
				}
			case "revoked_hmac":
				f.setSecret("synthetic-controller-hmac-rotated")
			}
			second, stopSecond := f.lifecycle()
			err = second.registerAndStart()
			want := codes.PermissionDenied
			if state == "revoked_hmac" {
				want = codes.Unauthenticated
			}
			if status.Code(err) != want {
				t.Fatalf("restored %s identity error=%v want=%v", state, err, want)
			}
			assertEnrollmentBlocked(t, second)
			f.assertRegistrations(2, identity, token)
			assertUnchangedIdentity(t, second.AgentConf.Agent.IdentityPersistence.Directory, persisted, original)
			stopSecond()
			if state == "pending_approval" {
				if err := f.repos.Agent.Approve(f.ctx, identity); err != nil {
					t.Fatal(err)
				}
				approved, _ := f.lifecycle()
				if err := approved.registerAndStart(); err != nil || !approved.IsReady() {
					t.Fatal("approved identity did not resume without reenrollment", err)
				}
				f.assertRegistrations(3, identity, token)
			}
		})
	}
}

func (f *enrollmentFixture) wait(description string, predicate func() bool) {
	f.t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !predicate() {
		select {
		case <-f.ctx.Done():
			f.t.Fatalf("%s: %v", description, f.ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestDynamicEnrollmentRestoresTerminalJournalThroughRealProcessAndWorker(t *testing.T) {
	if testing.Short() {
		t.Skip("real Kafka wire-protocol lifecycle integration")
	}
	f := newEnrollmentFixture(t, true)
	cluster, err := ckafka.NewMockCluster(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cluster.Close)
	first, stopFirst := f.lifecycle()
	if _, err := first.AgentService.Register(f.ctx, nil); err != nil {
		t.Fatal(err)
	}
	identity, token := first.AgentConf.Agent.ID, first.AgentConf.Grpc.Token
	if _, err := first.AgentService.Heartbeat(f.ctx, nil); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{taskpayload.AgentTopicPrefix(identity), taskpayload.AgentControlTopicPrefix(identity)} {
		for _, suffix := range []string{"_PRIORITY_NORMAL", "_PRIORITY_HIGH", "_PRIORITY_LOW", "_TASKS_DELAY_0"} {
			if err := cluster.CreateTopic(strings.ToUpper(prefix+suffix), 1, 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	first.AgentConf.MessageQueue.Kafka = kafkaconfig.Config{BootstrapServers: cluster.BootstrapServers(), SecurityProtocol: "PLAINTEXT"}
	if err := first.startWorkers(); err != nil {
		t.Fatal(err)
	}
	if !first.IsReady() || first.TaskQueue == nil {
		t.Fatal("authenticated dynamic enrollment did not start real workers")
	}
	producer, err := process.ProvideTaskQueueProducer(&controlconfig.AppConfig{
		MessageQueue: controlconfig.MessageQueueConfig{Kafka: kafkaconfig.Config{BootstrapServers: cluster.BootstrapServers(), SecurityProtocol: "PLAINTEXT"}},
		TaskQueue:    nova.TaskQueueConfig{MessageFormat: "sonic", MessageCodec: "sonic"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Stop() })
	engine := process.NewProcess(f.repos, nil, producer, nil, &log.Logger{SugaredLogger: zap.NewNop().Sugar()}, nil, nil)
	t.Cleanup(engine.Stop)
	projectID, pipelineID, runID := id.GetUild(), id.GetUild(), id.GetUild()
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(os.TempDir(), "pipelines", pipelineID)) })
	if err := f.db.WithContext(f.ctx).Create(&model.Project{ProjectID: projectID, IsEnabled: 1, Status: model.ProjectStatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.repos.Pipeline.Create(f.ctx, &model.Pipeline{PipelineID: pipelineID, ProjectID: projectID, Name: "durable-enrollment", IsEnabled: 1}); err != nil {
		t.Fatal(err)
	}
	run := &model.PipelineRun{RunID: runID, PipelineID: pipelineID, Status: model.PipelineStatusPending, TotalJobs: 1}
	if err := f.repos.Pipeline.CreateRun(f.ctx, run); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.loseStepACK = true
	f.mu.Unlock()
	if err := engine.Submit(run, &spec.Pipeline{Namespace: "enrollment-restart", Jobs: []*spec.Job{{Name: "execute-once", Steps: []*spec.Step{{Name: "append-once", Uses: "shell", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"run": "printf x >> executions"})}}}}}); err != nil {
		t.Fatal(err)
	}
	var job *model.JobRun
	var step *model.StepRun
	journal := filepath.Join(first.AgentConf.Agent.WorkspaceDir, ".terminal-report-outbox")
	f.wait("committed step and pending terminal journal", func() bool {
		jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, runID)
		if err != nil || len(jobs) != 1 {
			return false
		}
		steps, total, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: runID, PageSize: 10})
		if err != nil || total != 1 || steps[0].Status != 4 || steps[0].TerminalReportDigest == "" {
			return false
		}
		job, step = jobs[0], &steps[0]
		records, err := filepath.Glob(filepath.Join(journal, "*.record"))
		return err == nil && len(records) == 2
	})
	if job.AgentID != identity || step.AgentID != identity || job.Status != 3 {
		t.Fatal("terminal test did not use the dynamically enrolled identity or pending parent")
	}
	originalDigest, originalClaim, committedAt := step.TerminalReportDigest, step.ClaimID, step.UpdatedAt
	persisted, identityInfo := enrollmentFile(t, first.AgentConf.Agent.IdentityPersistence.Directory, "identity.json", 0600)
	journalIdentity, err := os.ReadFile(filepath.Join(journal, "identity"))
	if err != nil {
		t.Fatal(err)
	}
	stopFirst()
	if first.IsReady() {
		t.Fatal("closed lifecycle remains ready")
	}
	f.mu.Lock()
	f.loseStepACK = false
	beforeReplay := len(f.receipts["step/"+step.StepRunID])
	f.mu.Unlock()
	second, _ := f.lifecycle()
	second.AgentConf.MessageQueue.Kafka = first.AgentConf.MessageQueue.Kafka
	if err := second.registerAndStart(); err != nil {
		t.Fatal(err)
	}
	if !second.IsReady() || second.TaskQueue == nil || second.AgentConf.Agent.ID != identity || second.AgentConf.Grpc.Token != token {
		t.Fatal("fresh authenticated lifecycle failed to reopen the original worker identity")
	}
	f.wait("original journal replay and pipeline completion", func() bool {
		completed, err := f.repos.Pipeline.GetRun(f.ctx, runID)
		if err != nil || completed.Status != model.PipelineStatusSuccess {
			return false
		}
		records, err := filepath.Glob(filepath.Join(journal, "*.record"))
		return err == nil && len(records) == 0
	})
	replayed, err := f.repos.StepRun.GetByStepRunID(f.ctx, step.StepRunID)
	if err != nil || replayed.TerminalReportDigest != originalDigest || replayed.ClaimID != originalClaim || !replayed.UpdatedAt.Equal(committedAt) {
		t.Fatal("replay changed the controller's committed terminal receipt", err)
	}
	marker, err := os.ReadFile(filepath.Join(second.AgentConf.Agent.WorkspaceDir, runID, job.JobRunID, "executions"))
	if err != nil || string(marker) != "x" {
		t.Fatal("restored lifecycle executed the shell more than once", err)
	}
	afterJournalIdentity, err := os.ReadFile(filepath.Join(journal, "identity"))
	if err != nil || !bytes.Equal(journalIdentity, afterJournalIdentity) {
		t.Fatal("restored worker replaced the terminal journal binding", err)
	}
	f.assertRegistrations(2, identity, token)
	assertUnchangedIdentity(t, second.AgentConf.Agent.IdentityPersistence.Directory, persisted, identityInfo)
	f.mu.Lock()
	defer f.mu.Unlock()
	receipts := f.receipts["step/"+step.StepRunID]
	if beforeReplay == 0 || len(receipts) <= beforeReplay {
		t.Fatal("restored terminal journal did not replay the original pending RPC")
	}
	for _, receipt := range receipts[1:] {
		if receipt.claim != receipts[0].claim || !bytes.Equal(receipt.payload, receipts[0].payload) {
			t.Fatal("journal replay changed the original claim or terminal protobuf")
		}
	}
}

func TestAgentKafkaStartupErrorDoesNotExposeBrokerSettings(t *testing.T) {
	f := newEnrollmentFixture(t, true)
	app, cleanup := f.lifecycle()
	defer cleanup()
	if _, err := app.AgentService.Register(f.ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AgentService.Heartbeat(f.ctx, nil); err != nil {
		t.Fatal(err)
	}
	app.AgentConf.MessageQueue.Kafka = kafkaconfig.Config{BootstrapServers: "127.0.0.1:9092", SecurityProtocol: "CANARY_PRIVATE_BROKER_CONFIG"}
	err := app.startWorkers()
	if err == nil || !strings.Contains(err.Error(), "create agent Kafka log publisher") || strings.Contains(err.Error(), "CANARY_PRIVATE") || errors.Unwrap(err) == nil {
		t.Fatalf("unsafe or missing Kafka startup error: %v", err)
	}
	if app.IsReady() || app.TaskQueue != nil {
		t.Fatal("failed Kafka startup became ready")
	}
}
