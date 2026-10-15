// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package taskqueue

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/safe"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// liveStepACKTransport 只丢弃真实服务已提交的响应，不模拟任何控制面状态事务
// 固定身份鉴权仅属于普通测试，不能代表生产身份引导或真实运行时验收
// 运行时仍为明确的 fakeStepRetrySession
type liveStepACKTransport struct {
	agentv1.AgentServiceClient
	runtime              *fakeStepRetryRuntime
	lostFailure, lostCAS bool
	requests             []string
}

func (c *liveStepACKTransport) ReportStepRunStatus(ctx context.Context, req *agentv1.ReportStepRunStatusRequest, opts ...grpc.CallOption) (*agentv1.ReportStepRunStatusResponse, error) {
	response, err := c.AgentServiceClient.ReportStepRunStatus(ctx, req, opts...)
	if err != nil {
		return response, err
	}
	if req.Status == steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING {
		c.runtime.session.running[req.StepRunId] = true
	}
	if req.RetryableShellExit && !c.lostFailure {
		c.lostFailure = true
		return nil, status.Error(codes.Unavailable, "test dropped committed failure ACK")
	}
	return response, nil
}
func (c *liveStepACKTransport) ReconcileStepAttempt(ctx context.Context, req *agentv1.ReconcileStepAttemptRequest, opts ...grpc.CallOption) (*agentv1.ReconcileStepAttemptResponse, error) {
	c.requests = append(c.requests, req.TransitionJson)
	response, err := c.AgentServiceClient.ReconcileStepAttempt(ctx, req, opts...)
	if err != nil {
		return response, err
	}
	tr, err := taskpayload.ParseStepAttemptTransition(req.TransitionJson)
	if err != nil {
		return nil, err
	}
	if tr.Operation == "advance" && !c.lostCAS {
		c.lostCAS = true
		return nil, status.Error(codes.Unavailable, "test dropped committed step CAS ACK")
	}
	return response, nil
}

// TestFailedStepWorkerBufconnTLSServiceSQLiteRetainsFixedGeneration 验证真实 TLS/服务/SQLite 与工作器的完整步骤协议
// 不走生产调度开关，不运行 containerd，不声称 SQLite 能证明 MySQL 行锁
func TestFailedStepWorkerBufconnTLSServiceSQLiteRetainsFixedGeneration(t *testing.T) {
	conf, p, r, _ := fakeStepRetryFixture(t, 3)
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "step-worker.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.WithContext(ctx).AutoMigrate(&model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.JobRunGeneration{}, &model.StepRun{}, &model.Agent{}); err != nil {
		t.Fatal(err)
	}
	adapter := workerTestDatabase{db}
	jobs := repo.NewJobRunRepo(adapter)
	steps := repo.NewStepRunRepo(adapter)
	agents := repo.NewAgentRepo(adapter, nil)
	for _, row := range []any{&model.Project{ProjectID: "project"}, &model.Pipeline{PipelineID: p.PipelineID, ProjectID: "project", IsEnabled: 1}, &model.PipelineRun{RunID: p.PipelineRunID, PipelineID: p.PipelineID, Status: model.PipelineStatusRunning, Env: "{}"}, &model.Agent{AgentID: p.AgentID, IsEnabled: 1, Status: 1}} {
		if err := db.WithContext(ctx).Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := jobs.Create(ctx, &model.JobRun{JobRunID: p.JobRunID, PipelineID: p.PipelineID, PipelineRunID: p.PipelineRunID, AgentID: p.AgentID, Status: model.JobRunStatusQueued, TotalSteps: len(p.Steps)}); err != nil {
		t.Fatal(err)
	}
	policy, _ := sonic.MarshalString(p.Retry)
	for _, step := range p.Steps {
		for attempt, stepID := range step.AttemptRunIDs {
			previous := ""
			if attempt > 0 {
				previous = step.AttemptRunIDs[attempt-1]
			}
			args, _ := sonic.MarshalString(step.Args)
			if err := steps.Create(ctx, &model.StepRun{StepRunID: stepID, Name: step.Name, PipelineID: p.PipelineID, PipelineRunID: p.PipelineRunID, JobRunID: p.JobRunID, StepIndex: int(step.StepIndex), Attempt: attempt + 1, RetryPolicy: policy, RetryMode: "failed_step", PreviousStepRunID: previous, Uses: step.Uses, Action: step.Action, Args: args, Status: model.JobRunStatusQueued}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := jobs.(*repo.JobRunRepo).CaptureRuntimeRetryExpectation(ctx, p.PipelineID, p.PipelineRunID, p.JobRunID, p.AgentID, nil, p.Runtime, nil, nil, p.RetryPlan); err != nil {
		t.Fatal(err)
	}
	tlsOptions := controllerWorkerTLS(t)
	listener := bufconn.Listen(1024 * 1024)
	token := "fixed-step-worker-test-token"
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsOptions.serverTLS)), grpc.UnaryInterceptor(func(callCtx context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(callCtx)
		values := md.Get("authorization")
		if len(values) != 1 || values[0] != "bearer "+token {
			return nil, status.Error(codes.Unauthenticated, "fixed test token required")
		}
		return handler(agentidentity.WithIdentity(callCtx, agentidentity.Identity{ID: p.AgentID}), request)
	}))
	agentv1.RegisterAgentServiceServer(server, controlservice.NewAgentServiceImpl(controlservice.NewAgentService(agents, steps, nil, jobs), nil))
	safe.Go(func() { _ = server.Serve(listener) })
	t.Cleanup(server.Stop)
	client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: "passthrough:///bufconn-step-worker", Token: token, ReadWriteTimeout: 2, TLS: tlsOptions.clientTLS})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	conf.Grpc.ServerAddr = "passthrough:///bufconn-step-worker"
	conf.Grpc.Token = token
	conf.Grpc.TLS = tlsOptions.clientTLS
	roots := x509.NewCertPool()
	pem, err := os.ReadFile(tlsOptions.clientTLS.CAFile)
	if err != nil || !roots.AppendCertsFromPEM(pem) {
		t.Fatalf("read test CA: %v", err)
	}
	conn, err := grpc.NewClient("passthrough:///bufconn-step-worker", grpc.WithContextDialer(func(dialCtx context.Context, _ string) (net.Conn, error) { return listener.DialContext(dialCtx) }), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	transport := &liveStepACKTransport{AgentServiceClient: agentv1.NewAgentServiceClient(conn), runtime: r}
	client.AgentClient = transport
	var effects []string
	r.executeStep = func(a taskpayload.StepAttemptAdmission, _ *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		effects = append(effects, fmt.Sprintf("%d/%d", a.Scope.StepIndex, a.Scope.Attempt))
		if a.Scope.StepIndex == 1 && a.Scope.Attempt == 1 {
			return fakeStepExit(9)
		}
		return fakeStepExit(0)
	}
	outbox, err := newTerminalOutbox(ctx, filepath.Join(t.TempDir(), "outbox"), conf.Agent.ID, conf.Grpc.ServerAddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outbox.close() })
	if err := executeJobRun(withOfflineJobRuntime(withTerminalOutbox(ctx, outbox), r), conf, client, executor.NewExecutorManager(), p); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(effects, []string{"0/1", "1/1", "1/2", "2/1"}) || r.allocations != 1 || r.acknowledgements != 1 {
		t.Fatalf("fixed session effects changed: %v allocations=%d ACKs=%d", effects, r.allocations, r.acknowledgements)
	}
	if !transport.lostFailure || !transport.lostCAS {
		t.Fatal("lost ACK paths not exercised")
	}
	saved, err := jobs.GetByJobRunID(ctx, p.JobRunID)
	if err != nil || saved.Status != model.JobRunStatusSuccess || saved.CurrentGeneration != 1 {
		t.Fatalf("job failed fixed-generation finalization: %+v %v", saved, err)
	}
	rows, total, err := steps.List(ctx, repo.StepRunFilter{PipelineRunID: p.PipelineRunID, StepRunIDs: allJobAttemptIDs(p)})
	if err != nil || total != 9 {
		t.Fatalf("rows %d: %v", total, err)
	}
	admitted := 0
	for _, row := range rows {
		if row.AttemptAdmission != "" {
			admitted++
			if row.GenerationID != saved.CurrentGenerationID {
				t.Fatal("admission changed generation")
			}
		} else if row.Status != model.JobRunStatusSkipped {
			t.Fatal("unused slot was not safely closed")
		}
	}
	if admitted != 4 {
		t.Fatalf("unexpected admission chain size: %d", admitted)
	}
}
