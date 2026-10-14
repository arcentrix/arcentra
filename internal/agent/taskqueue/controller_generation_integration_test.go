// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/sandboxexec"
	controlconfig "github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/process"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// generationACKLossTransport 保留真实 TLS RPC 和控制面事务，只丢弃一次已收到的后继响应
type generationACKLossTransport struct {
	agentv1.AgentServiceClient
	lost     bool
	requests []string
}

func (c *generationACKLossTransport) ReconcileJobGeneration(ctx context.Context, req *agentv1.ReconcileJobGenerationRequest, opts ...grpc.CallOption) (*agentv1.ReconcileJobGenerationResponse, error) {
	c.requests = append(c.requests, req.TransitionJson)
	response, err := c.AgentServiceClient.ReconcileJobGeneration(ctx, req, opts...)
	if err != nil {
		return response, err
	}
	tr, err := taskpayload.ParseJobGenerationTransition(req.TransitionJson)
	if err != nil {
		return nil, err
	}
	if tr.Operation == "advance" && !c.lost {
		c.lost = true
		return nil, status.Error(codes.Unavailable, "real controller committed transition; test dropped ACK")
	}
	return response, nil
}

// startFakeGenerationIntegrationWorker 使用真实 Nova 消费者和生产 handler，仅注入明确的假容器会话
// 此入口不调用真实 containerd，不替代运行时构造、崩溃恢复或真实容器验收
func startFakeGenerationIntegrationWorker(ctx context.Context, conf *config.AgentConfig, _ *agentgrpc.ClientWrapper, manager *executor.Manager, r *fakeGenerationRuntime, transport *generationACKLossTransport) (nova.TaskQueue, error) {
	reports, err := newTerminalOutbox(ctx, filepath.Join(conf.Agent.WorkspaceDir, ".terminal-report-outbox"), conf.Agent.ID, conf.Grpc.ServerAddr)
	if err != nil {
		return nil, err
	}
	codec, err := taskpayload.ResolveMessageCodec(conf.TaskQueue.MessageFormat, conf.TaskQueue.MessageCodec)
	if err != nil {
		_ = reports.close()
		return nil, err
	}
	options := []nova.QueueOption{nova.WithMessageCodec(codec), nova.WithTopicPrefix(taskpayload.AgentTopicPrefix(conf.Agent.ID)), nova.WithGroupID("generation-integration-" + conf.Agent.ID), nova.WithKafka(conf.MessageQueue.Kafka.BootstrapServers, nova.WithKafkaSessionTimeout(6000), nova.WithKafkaMaxPollInterval(120000), nova.WithKafkaDelaySlots(1, time.Hour))}
	queue, err := nova.NewTaskQueue(options...)
	if err != nil {
		_ = reports.close()
		return nil, err
	}
	controlOptions := append([]nova.QueueOption(nil), options...)
	controlOptions = append(controlOptions, nova.WithTopicPrefix(taskpayload.AgentControlTopicPrefix(conf.Agent.ID)), nova.WithGroupID("generation-control-"+conf.Agent.ID))
	control, err := nova.NewTaskQueue(controlOptions...)
	if err != nil {
		_ = queue.Stop()
		_ = reports.close()
		return nil, err
	}
	// 独立构造带相同 TLS、认证及超时配置的客户端，绝不复制连接互斥锁
	wrapped, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{
		ServerAddr: conf.Grpc.ServerAddr, Token: conf.Grpc.Token,
		ReadWriteTimeout: 2, TLS: conf.Grpc.TLS,
	})
	if err != nil {
		_ = control.Stop()
		_ = queue.Stop()
		_ = reports.close()
		return nil, err
	}
	r.t.Cleanup(func() { _ = wrapped.Close() })
	transport.AgentServiceClient = wrapped.AgentClient
	wrapped.AgentClient = transport
	if err = control.Start(nova.HandlerFunc(func(context.Context, *nova.Task) error { return nil })); err != nil {
		_ = control.Stop()
		_ = queue.Stop()
		_ = reports.close()
		return nil, err
	}
	if err = queue.Start(newWorkerHandler(withOfflineJobRuntime(ctx, r), conf, wrapped, manager, reports)); err != nil {
		_ = control.Stop()
		_ = queue.Stop()
		_ = reports.close()
		return nil, err
	}
	return &workerQueues{TaskQueue: queue, control: control, reports: reports}, nil
}

// TestContainerGenerationRealProcessTLSRPCSQLiteMockKafkaFakeRuntime 验证普通无特权控制面到工作器代次协议
// Process、TLS RPC、SQLite 和 Kafka 线协议是真实路径；容器、清理证据及对象存储明确为测试替身
func TestContainerGenerationRealProcessTLSRPCSQLiteMockKafkaFakeRuntime(t *testing.T) {
	image := "registry.example/approved@sha256:" + strings.Repeat("a", 64)
	policy := taskpayload.OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowedImages: []string{image}, MaxCPU: "1", MaxMemory: "128Mi"}
	r := &fakeGenerationRuntime{t: t, root: t.TempDir(), drain: make(chan struct{}), ready: true}
	r.execute = func(s *fakeGenerationSession, req *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		if req.Env[taskpayload.RuntimeGenerationEnv] != fmt.Sprint(s.generation.Number) {
			return nil, fmt.Errorf("trusted generation variable missing")
		}
		if req.StepIndex == 0 {
			if _, err := os.Stat(filepath.Join(s.workspace, "round-output")); !os.IsNotExist(err) {
				return nil, fmt.Errorf("workspace was reused")
			}
			if err := os.WriteFile(filepath.Join(s.workspace, "round-output"), []byte(fmt.Sprint(s.generation.Number)), 0600); err != nil {
				return nil, err
			}
		}
		if req.StepIndex == 1 && s.generation.Number == 1 {
			return fakeGenerationResult(s, 9, &sandboxexec.CommandExitError{Code: 9})
		}
		return fakeGenerationResult(s, 0, nil)
	}
	transport := &generationACKLossTransport{}
	options := controllerWorkerTLS(t)
	options.processConfig = &controlconfig.AppConfig{Pipeline: controlconfig.PipelineConfig{OfflineRuntime: policy}}
	options.configureAgent = func(conf *config.AgentConfig) {
		conf.Agent.Mode = "sandbox"
		conf.Agent.Sandbox = config.SandboxConfig{Enable: true, Runtime: "containerd", Containerd: config.ContainerdConfig{Policy: policy}}
	}
	options.startWorker = func(ctx context.Context, conf *config.AgentConfig, client *agentgrpc.ClientWrapper, manager *executor.Manager) (nova.TaskQueue, error) {
		return startFakeGenerationIntegrationWorker(ctx, conf, client, manager, r, transport)
	}
	f := newControllerWorkerFixture(t, false, options)
	if !f.client.ControllerAuthenticated() || f.db.Dialector.Name() != "sqlite" || f.store == nil {
		t.Fatal("ordinary integration dependency boundary changed")
	}
	capabilities, err := sonic.Marshal(taskpayload.AgentRuntimeCapabilities{ExecutionMode: taskpayload.RuntimeContainerdOfflineV1, ContainerdOfflineV1: true, ContainerdWholeJobRetryV1: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repos.Agent.Patch(f.ctx, f.conf.Agent.ID, map[string]any{"os": "linux", "arch": "amd64", "runtime_capabilities": capabilities}); err != nil {
		t.Fatal(err)
	}
	job := &spec.Job{Name: "retry-fresh-round", Retry: &spec.Retry{Mode: "whole_job", MaxAttempts: 3, Idempotent: true, AllowedExitCodes: []int32{9}}, Steps: []*spec.Step{controllerWorkerShell("prepare", "synthetic"), controllerWorkerShell("fail-once", "synthetic"), controllerWorkerShell("finish", "synthetic")}}
	run := &model.PipelineRun{RunID: id.GetUild(), PipelineID: f.pipelineID, Status: model.PipelineStatusPending, Env: "{}", TotalJobs: 1}
	if err := f.repos.Pipeline.CreateRun(f.ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.Submit(run, &spec.Pipeline{Namespace: "generation-integration", Runtime: &spec.Runtime{Type: taskpayload.RuntimeContainerdOfflineV1, Image: image, Resources: &spec.Resources{CpuLimit: "500m", MemoryLimit: "64Mi"}}, Jobs: []*spec.Job{job}}); err != nil {
		t.Fatal(err)
	}
	f.waitRun(run.RunID, model.PipelineStatusSuccess)
	if err := f.worker.Stop(); err != nil {
		t.Fatal(err)
	}
	jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%v err=%v", jobs, err)
	}
	saved := jobs[0]
	if r.allocations != 2 || r.acknowledgements != 1 || len(r.sessions) != 2 || r.sessions[0].executions != 2 || r.sessions[1].executions != 3 {
		t.Fatalf("execution was not exactly two fresh rounds: allocations=%d ACKs=%d sessions=%+v", r.allocations, r.acknowledgements, r.sessions)
	}
	if !transport.lost || len(transport.requests) != 3 || transport.requests[1] != transport.requests[2] {
		t.Fatal("transition ACK loss did not replay identical real RPC")
	}
	if saved.Status != model.JobRunStatusSuccess || saved.CurrentGeneration != 2 || saved.ClaimID != r.originalClaim || saved.InputManifestState != "pinned" || saved.RetryPlanDigest == "" {
		t.Fatalf("controller final identity changed: %+v", saved)
	}
	if r.admissions[0].InputManifestDigest != "" || !r.admissions[0].PreparationOnly || r.admissions[1].InputManifestDigest != saved.InputManifestDigest || r.admissions[1].RetryPlanDigest != saved.RetryPlanDigest {
		t.Fatal("bootstrap or immutable generation binding changed")
	}
	steps, total, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID})
	if err != nil || total != 9 {
		t.Fatalf("steps=%d err=%v", total, err)
	}
	for _, step := range steps {
		want := 4
		if step.Attempt == 1 && step.StepIndex == 1 {
			want = 5
		}
		if step.Attempt == 1 && step.StepIndex == 2 || step.Attempt == 3 {
			want = 8
		}
		if step.Status != want {
			t.Fatalf("step %s attempt %d status=%d want=%d", step.Name, step.Attempt, step.Status, want)
		}
	}
	var records []model.JobRunGeneration
	if err := f.db.WithContext(f.ctx).Select("generation_id", "job_run_id", "number", "state", "generation_json", "admission_request", "admission_digest", "closure_request", "closure_digest", "next_generation_id").Where("job_run_id = ?", saved.JobRunID).Order("number").Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].State != "closed" || records[0].NextGenerationID != records[1].GenerationID || records[0].ClosureRequest != transport.requests[1] || records[1].AdmissionRequest != transport.requests[1] {
		t.Fatalf("atomic generation records=%+v", records)
	}
	if r.sessions[0].workspace == r.sessions[1].workspace || r.admissions[0].RuntimeClaimID == r.admissions[1].RuntimeClaimID {
		t.Fatal("real protocol reused fake runtime identity")
	}
}

// failCapturedGenerationQueue 只在真实 TaskFramework 已保存完整重试计划后注入队列派发失败
type failCapturedGenerationQueue struct {
	nova.TaskQueue
	ctx      context.Context
	jobs     repo.IJobRunRepository
	mu       sync.Mutex
	calls    int
	captured *taskpayload.JobRunTaskPayload
	verified bool
}

func (q *failCapturedGenerationQueue) Enqueue(task *nova.Task, opts ...nova.Option) (*nova.Result, error) {
	if task == nil || task.Type != taskpayload.TaskTypeJobRun {
		return q.TaskQueue.Enqueue(task, opts...)
	}
	var p taskpayload.JobRunTaskPayload
	if err := sonic.Unmarshal(task.Payload, &p); err != nil {
		return nil, err
	}
	saved, err := q.jobs.GetByJobRunID(q.ctx, p.JobRunID)
	if err != nil {
		return nil, err
	}
	planErr := taskpayload.ValidateJobRetryPlan(&p)
	encoded, encodeErr := p.RetryPlan.Canonical()
	q.mu.Lock()
	q.calls++
	q.captured = &p
	q.verified = planErr == nil && encodeErr == nil && saved.SourceExpectationCaptured && saved.RetryPlan == encoded && saved.RetryPlanDigest == p.RetryPlanDigest && saved.ClaimID == "" && saved.CurrentGeneration == 0
	q.mu.Unlock()
	return nil, fmt.Errorf("synthetic queue dispatch failure after immutable retry capture")
}

// TestContainerGenerationQueueFailureFinalizesCapturedPlan 验证实际 TaskFramework 失败回调结束已冻结但未派发的计划
func TestContainerGenerationQueueFailureFinalizesCapturedPlan(t *testing.T) {
	image := "registry.example/approved@sha256:" + strings.Repeat("a", 64)
	policy := taskpayload.OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowedImages: []string{image}, MaxCPU: "1", MaxMemory: "128Mi"}
	options := controllerWorkerTLS(t)
	options.processConfig = &controlconfig.AppConfig{Pipeline: controlconfig.PipelineConfig{OfflineRuntime: policy}}
	f := newControllerWorkerFixture(t, false, options)
	capabilities, err := sonic.Marshal(taskpayload.AgentRuntimeCapabilities{ExecutionMode: taskpayload.RuntimeContainerdOfflineV1, ContainerdOfflineV1: true, ContainerdWholeJobRetryV1: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repos.Agent.Patch(f.ctx, f.conf.Agent.ID, map[string]any{"os": "linux", "arch": "amd64", "runtime_capabilities": capabilities}); err != nil {
		t.Fatal(err)
	}
	failure := &failCapturedGenerationQueue{TaskQueue: f.producer, ctx: f.ctx, jobs: f.repos.JobRun}
	f.engine.Stop()
	f.engine = process.NewProcess(f.repos, nil, failure, nil, &log.Logger{SugaredLogger: zap.NewNop().Sugar()}, options.processConfig, nil)
	t.Cleanup(f.engine.Stop)
	var executionRPCs atomic.Int64
	f.mu.Lock()
	f.observeRequest = func(_ context.Context, req any) {
		switch req.(type) {
		case *agentv1.ReconcileJobGenerationRequest, *agentv1.ReportJobRunStatusRequest, *agentv1.ReportStepRunStatusRequest:
			executionRPCs.Add(1)
		}
	}
	f.mu.Unlock()
	job := &spec.Job{Name: "unsubmitted-retry", Retry: &spec.Retry{Mode: "whole_job", MaxAttempts: 3, Idempotent: true, AllowedExitCodes: []int32{9}}, Steps: []*spec.Step{controllerWorkerShell("first", "never execute"), controllerWorkerShell("second", "never execute")}}
	run := &model.PipelineRun{RunID: id.GetUild(), PipelineID: f.pipelineID, Status: model.PipelineStatusPending, Env: "{}", TotalJobs: 1}
	if err := f.repos.Pipeline.CreateRun(f.ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.Submit(run, &spec.Pipeline{Namespace: "generation-dispatch-failure", Runtime: &spec.Runtime{Type: taskpayload.RuntimeContainerdOfflineV1, Image: image, Resources: &spec.Resources{CpuLimit: "500m", MemoryLimit: "64Mi"}}, Jobs: []*spec.Job{job}}); err != nil {
		t.Fatal(err)
	}
	f.waitRun(run.RunID, model.PipelineStatusFailed)
	if err := f.worker.Stop(); err != nil {
		t.Fatal(err)
	}
	failure.mu.Lock()
	calls, captured, verified := failure.calls, failure.captured, failure.verified
	failure.mu.Unlock()
	if calls != 1 || captured == nil || !verified {
		t.Fatalf("dispatch failure did not occur after actual immutable plan capture: calls=%d verified=%t payload=%+v", calls, verified, captured)
	}
	saved, err := f.repos.JobRun.GetByJobRunID(f.ctx, captured.JobRunID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != model.JobRunStatusFailed || saved.EndTime == nil || !strings.Contains(saved.ErrorMessage, "synthetic queue dispatch failure") || saved.RetryPlanDigest != captured.RetryPlanDigest {
		t.Fatalf("TaskFramework did not terminalize captured plan: %+v", saved)
	}
	if saved.ClaimID != "" || saved.CurrentGeneration != 0 || saved.CurrentGenerationID != "" || saved.TerminalReportDigest != "" || saved.InputManifestState != "unknown" {
		t.Fatalf("controller finalization invented Agent execution evidence: %+v", saved)
	}
	steps, total, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID})
	if err != nil || total != 6 {
		t.Fatalf("preallocated attempts=%d err=%v", total, err)
	}
	for _, step := range steps {
		if step.Status != 8 || step.EndTime == nil || step.StartTime != nil || step.ClaimID != "" || step.GenerationID != "" || step.TerminalReportDigest != "" || step.RetryableShellExit {
			t.Fatalf("unexecuted attempt was not safely finalized: %+v", step)
		}
	}
	var generations int64
	if err := f.db.WithContext(f.ctx).Model(&model.JobRunGeneration{}).Where("job_run_id = ?", saved.JobRunID).Count(&generations).Error; err != nil {
		t.Fatal(err)
	}
	if generations != 0 || executionRPCs.Load() != 0 {
		t.Fatalf("failed dispatch executed a generation: records=%d RPCs=%d", generations, executionRPCs.Load())
	}
}

// TestContainerGenerationRealProcessAllSkippedSucceedsWithoutCommands 验证只准备输入且所有条件跳过的整作业终态
func TestContainerGenerationRealProcessAllSkippedSucceedsWithoutCommands(t *testing.T) {
	image := "registry.example/approved@sha256:" + strings.Repeat("a", 64)
	policy := taskpayload.OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowedImages: []string{image}, MaxCPU: "1", MaxMemory: "128Mi"}
	r := &fakeGenerationRuntime{t: t, root: t.TempDir(), drain: make(chan struct{}), ready: true}
	transport := &generationACKLossTransport{}
	options := controllerWorkerTLS(t)
	options.processConfig = &controlconfig.AppConfig{Pipeline: controlconfig.PipelineConfig{OfflineRuntime: policy}}
	options.configureAgent = func(conf *config.AgentConfig) {
		conf.Agent.Mode = "sandbox"
		conf.Agent.Sandbox = config.SandboxConfig{Enable: true, Runtime: "containerd", Containerd: config.ContainerdConfig{Policy: policy}}
	}
	options.startWorker = func(ctx context.Context, conf *config.AgentConfig, client *agentgrpc.ClientWrapper, manager *executor.Manager) (nova.TaskQueue, error) {
		return startFakeGenerationIntegrationWorker(ctx, conf, client, manager, r, transport)
	}
	f := newControllerWorkerFixture(t, false, options)
	capabilities, err := sonic.Marshal(taskpayload.AgentRuntimeCapabilities{ExecutionMode: taskpayload.RuntimeContainerdOfflineV1, ContainerdOfflineV1: true, ContainerdWholeJobRetryV1: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repos.Agent.Patch(f.ctx, f.conf.Agent.ID, map[string]any{"os": "linux", "arch": "amd64", "runtime_capabilities": capabilities}); err != nil {
		t.Fatal(err)
	}
	var stepRunning atomic.Int64
	terminalResult := make(chan error, 1)
	f.mu.Lock()
	f.observeRequest = func(_ context.Context, req any) {
		if step, ok := req.(*agentv1.ReportStepRunStatusRequest); ok && int(step.Status) == 3 {
			stepRunning.Add(1)
		}
	}
	f.observeTerminal = func(_ context.Context, message proto.Message, _ any, err error) {
		if req, ok := message.(*agentv1.ReportJobRunStatusRequest); ok && req.Status == 4 {
			select {
			case terminalResult <- err:
			default:
			}
		}
	}
	f.mu.Unlock()
	first, second := controllerWorkerShell("skip-first", "never execute"), controllerWorkerShell("skip-second", "never execute")
	first.When = "false"
	second.When = "false"
	job := &spec.Job{Name: "all-skipped-retry", Retry: &spec.Retry{Mode: "whole_job", MaxAttempts: 3, Idempotent: true, AllowedExitCodes: []int32{9}}, Steps: []*spec.Step{first, second}}
	run := &model.PipelineRun{RunID: id.GetUild(), PipelineID: f.pipelineID, Status: model.PipelineStatusPending, Env: "{}", TotalJobs: 1}
	if err := f.repos.Pipeline.CreateRun(f.ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.Submit(run, &spec.Pipeline{Namespace: "generation-all-skipped", Runtime: &spec.Runtime{Type: taskpayload.RuntimeContainerdOfflineV1, Image: image, Resources: &spec.Resources{CpuLimit: "500m", MemoryLimit: "64Mi"}}, Jobs: []*spec.Job{job}}); err != nil {
		t.Fatal(err)
	}
	f.wait("all-skipped generation success receipt", func() bool {
		select {
		case err := <-terminalResult:
			if err != nil {
				t.Fatalf("all-skipped generation success rejected: %v", err)
			}
			return true
		default:
			return false
		}
	})
	f.waitRun(run.RunID, model.PipelineStatusSuccess)
	if err := f.worker.Stop(); err != nil {
		t.Fatal(err)
	}
	jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	saved := jobs[0]
	if saved.Status != model.JobRunStatusSuccess || saved.InputManifestState != "pinned" || saved.InputManifestDigest == "" || saved.CurrentGeneration != 1 || saved.TerminalReportDigest == "" {
		t.Fatalf("all-skipped terminal input/generation evidence incomplete: %+v", saved)
	}
	if len(r.sessions) != 1 || r.sessions[0].executions != 0 || r.sessions[0].started || r.allocations != 1 || r.acknowledgements != 1 || len(transport.requests) != 1 || len(r.admissions) != 1 || stepRunning.Load() != 0 {
		t.Fatalf("all-skipped job executed or advanced: sessions=%+v allocations=%d ACKs=%d RPCs=%d admissions=%d RUNNING=%d", r.sessions, r.allocations, r.acknowledgements, len(transport.requests), len(r.admissions), stepRunning.Load())
	}
	steps, total, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID})
	if err != nil || total != 6 {
		t.Fatalf("steps=%d err=%v", total, err)
	}
	for _, step := range steps {
		if step.Status != 8 || step.EndTime == nil || step.TerminalReportDigest == "" {
			t.Fatalf("skipped slot lacks its exact terminal receipt: %+v", step)
		}
		if step.Attempt == 1 && (step.GenerationID != saved.CurrentGenerationID || step.GenerationBindingDigest != r.admissions[0].BindingDigest || step.StartTime == nil) {
			t.Fatalf("current skipped receipt lost generation identity or worker timestamp: %+v", step)
		}
	}
	var generationCount int64
	if err := f.db.WithContext(f.ctx).Model(&model.JobRunGeneration{}).Where("job_run_id = ?", saved.JobRunID).Count(&generationCount).Error; err != nil {
		t.Fatal(err)
	}
	if generationCount != 1 {
		t.Fatalf("all-skipped job admitted %d generations", generationCount)
	}
}
