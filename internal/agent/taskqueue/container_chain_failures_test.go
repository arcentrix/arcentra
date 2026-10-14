// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/sandboxexec"
	controlconfig "github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/nova"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
)

// chainFailureRuntime 只模拟离线会话边界，不启动容器、不执行宿主 Shell
// 测试保留真实 Process、Kafka MockCluster、TLS RPC 和 SQLite，不构成真实容器验收
// 所有异步观测都受同一把锁保护
type chainFailureRuntime struct {
	mu       sync.Mutex
	root     string
	failure  string
	sessions map[string]*chainFailureSession
}

type chainFailureSession struct {
	runtime                  *chainFailureRuntime
	jobID, name, claim, root string
	calls                    []string
	acknowledged             bool
}

func (r *chainFailureRuntime) Ready() bool { return true }
func (r *chainFailureRuntime) HasJob(jobID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, session := range r.sessions {
		if session.jobID == jobID && !session.acknowledged {
			return true
		}
	}
	return false
}
func (r *chainFailureRuntime) Allocate(ctx context.Context, payload *taskpayload.JobRunTaskPayload, claim string) (offlineSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[payload.JobName] != nil {
		return nil, fmt.Errorf("synthetic runtime allocated a job more than once")
	}
	root := filepath.Join(r.root, payload.PipelineRunID, payload.JobRunID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	session := &chainFailureSession{runtime: r, jobID: payload.JobRunID, name: payload.JobName, claim: claim, root: root, calls: []string{"allocate"}}
	r.sessions[payload.JobName] = session
	return session, nil
}
func (r *chainFailureRuntime) Acknowledge(_ context.Context, jobID, claim string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, session := range r.sessions {
		if session.jobID == jobID && session.claim == claim {
			session.calls = append(session.calls, "acknowledge")
			session.acknowledged = true
			return nil
		}
	}
	return fmt.Errorf("synthetic runtime claim changed")
}
func (r *chainFailureRuntime) snapshot(name string) ([]string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session := r.sessions[name]
	if session == nil {
		return nil, ""
	}
	return append([]string(nil), session.calls...), session.root
}
func (s *chainFailureSession) Name() string { return "synthetic-offline-chain" }
func (s *chainFailureSession) CanExecute(req *executor.ExecutionRequest) bool {
	return req != nil && req.Step != nil && req.Step.Uses == "shell"
}
func (s *chainFailureSession) Workspace() string { return s.root }
func (s *chainFailureSession) record(call string) error {
	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()
	s.calls = append(s.calls, call)
	if s.name == "producer" && call == "execute" {
		switch s.runtime.failure {
		case "command_exit_once":
			calls := 0
			for _, recorded := range s.calls {
				if recorded == "execute" {
					calls++
				}
			}
			if calls == 1 {
				return &sandboxexec.CommandExitError{Code: 9}
			}
		case "command_exit":
			return &sandboxexec.CommandExitError{Code: 23}
		case "joined_execute":
			return errors.Join(&sandboxexec.CommandExitError{Code: 23}, errors.New("synthetic runtime failure"))
		}
	}
	if s.name == "producer" && call == s.runtime.failure {
		return fmt.Errorf("synthetic %s failure", call)
	}
	return nil
}
func (s *chainFailureSession) Start(context.Context) error { return s.record("start") }
func (s *chainFailureSession) Seal(context.Context) error  { return s.record("seal") }
func (s *chainFailureSession) Close(context.Context) error { return s.record("close") }
func (s *chainFailureSession) Execute(ctx context.Context, _ *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
	err := s.record("execute")
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && s.name == "producer" {
		// 固定字节仅供测试真实 artifact upload；不运行请求中的 Shell
		err = os.MkdirAll(filepath.Join(s.root, "dist"), 0o700)
		if err == nil {
			err = os.WriteFile(filepath.Join(s.root, "dist", "result.txt"), []byte("synthetic producer output"), 0o600)
		}
	}
	result := executor.NewExecutionResult(s.Name())
	if err != nil {
		code := int32(23)
		var commandExit *sandboxexec.CommandExitError
		if errors.As(err, &commandExit) {
			code = commandExit.Code
		}
		result.Complete(false, code, err)
	} else {
		result.Complete(true, 0, nil)
	}
	return result, err
}

// startChainFailureWorker 仅替换真实 containerd 的构造，保留实际消费处理器与持久终态收据
// 未模拟队列投递、RPC、存储步骤或控制面记录；本夹具不注入 ACK 丢失
func startChainFailureWorker(runtime *chainFailureRuntime) func(context.Context, *config.AgentConfig, *agentgrpc.ClientWrapper, *executor.Manager) (nova.TaskQueue, error) {
	return func(ctx context.Context, conf *config.AgentConfig, client *agentgrpc.ClientWrapper, manager *executor.Manager) (_ nova.TaskQueue, resultErr error) {
		if err := ValidateExecutionConfig(conf); err != nil {
			return nil, err
		}
		if !client.ControllerAuthenticated() {
			return nil, fmt.Errorf("synthetic runtime fixture requires authenticated TLS")
		}
		reports, err := newTerminalOutbox(ctx, filepath.Join(conf.Agent.WorkspaceDir, ".terminal-report-outbox"), conf.Agent.ID, conf.Grpc.ServerAddr)
		if err != nil {
			return nil, err
		}
		defer func() {
			if resultErr != nil {
				_ = reports.close()
			}
		}()
		qcfg := conf.TaskQueue
		codec, err := taskpayload.ResolveMessageCodec(qcfg.MessageFormat, qcfg.MessageCodec)
		if err != nil {
			return nil, err
		}
		options := []nova.QueueOption{
			nova.WithMessageCodec(codec), nova.WithTopicPrefix(taskpayload.AgentTopicPrefix(conf.Agent.ID)), nova.WithGroupID("arcentra-agent-" + conf.Agent.ID),
			nova.WithKafka(conf.MessageQueue.Kafka.BootstrapServers, nova.WithKafkaAuth("PLAINTEXT", "", "", ""), nova.WithKafkaAutoCommit(false), nova.WithKafkaSessionTimeout(qcfg.SessionTimeout), nova.WithKafkaMaxPollInterval(qcfg.MaxPollInterval), nova.WithKafkaDelaySlots(qcfg.DelaySlotCount, time.Duration(qcfg.DelaySlotDuration)*time.Second)),
		}
		queue, err := nova.NewTaskQueue(options...)
		if err != nil {
			return nil, err
		}
		defer func() {
			if resultErr != nil {
				_ = queue.Stop()
			}
		}()
		controlOptions := append([]nova.QueueOption{}, options...)
		controlOptions = append(controlOptions, nova.WithTopicPrefix(taskpayload.AgentControlTopicPrefix(conf.Agent.ID)), nova.WithGroupID("arcentra-agent-control-"+conf.Agent.ID))
		control, err := nova.NewTaskQueue(controlOptions...)
		if err != nil {
			return nil, err
		}
		defer func() {
			if resultErr != nil {
				_ = control.Stop()
			}
		}()
		if err := control.Start(nova.HandlerFunc(func(_ context.Context, task *nova.Task) error {
			if task == nil || task.Type != taskpayload.TaskTypeJobCancel {
				return nil
			}
			var payload taskpayload.JobCancelTaskPayload
			if err := sonic.Unmarshal(task.Payload, &payload); err != nil {
				return err
			}
			if payload.AgentID != conf.Agent.ID || payload.JobRunID == "" {
				return fmt.Errorf("invalid cancellation target")
			}
			CancelJobRun(payload.JobRunID)
			return nil
		})); err != nil {
			return nil, err
		}
		if err := queue.Start(newWorkerHandler(withOfflineJobRuntime(ctx, runtime), conf, client, manager, reports)); err != nil {
			return nil, err
		}
		return &workerQueues{TaskQueue: queue, control: control, reports: reports}, nil
	}
}

func newChainFailureFixture(t *testing.T, failure string) (*controllerWorkerFixture, *chainFailureRuntime) {
	t.Helper()
	image := "registry.example/offline-chain@sha256:" + strings.Repeat("a", 64)
	policy := taskpayload.OfflineRuntimePolicy{Enabled: true, AllowedImages: []string{image}, MaxCPU: "500m", MaxMemory: "64Mi"}
	runtime := &chainFailureRuntime{root: t.TempDir(), failure: failure, sessions: make(map[string]*chainFailureSession)}
	options := controllerWorkerTLS(t)
	options.processConfig = &controlconfig.AppConfig{Pipeline: controlconfig.PipelineConfig{OfflineRuntime: policy}}
	options.configureAgent = func(conf *config.AgentConfig) {
		conf.Agent.Mode = "sandbox"
		conf.Agent.Sandbox = config.SandboxConfig{Enable: true, Runtime: "containerd", Containerd: config.ContainerdConfig{Policy: policy}}
	}
	options.startWorker = startChainFailureWorker(runtime)
	f := newControllerWorkerFixture(t, false, options)
	// 固定身份夹具没有操作系统信息；能力仍必须由认证心跳经控制面持久化
	if err := f.repos.Agent.Patch(f.ctx, f.conf.Agent.ID, map[string]any{"os": "linux"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := f.client.WithTimeoutAndAuth(f.ctx)
	defer cancel()
	response, err := f.client.AgentClient.Heartbeat(ctx, &agentv1.HeartbeatRequest{AgentId: f.conf.Agent.ID, Status: agentv1.AgentStatus_AGENT_STATUS_IDLE, Timestamp: time.Now().Unix(), RuntimeCapabilities: &agentv1.RuntimeCapabilities{ContainerdOfflineV1: true, ExecutionMode: taskpayload.RuntimeContainerdOfflineV1}})
	if err != nil || response == nil || !response.Success {
		t.Fatalf("synthetic runtime capability heartbeat: %v", err)
	}
	return f, runtime
}

func submitChainFailurePipeline(t *testing.T, f *controllerWorkerFixture, skipProducer bool, continueProducer ...bool) *model.PipelineRun {
	t.Helper()
	producer := &spec.Job{Name: "producer", Steps: []*spec.Step{
		controllerWorkerShell("produce", "printf unexpected-host-execution > host-fallback-marker"),
		{Name: "publish", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"paths": []any{"dist"}})},
	}}
	if len(continueProducer) > 0 {
		producer.Steps[0].ContinueOnError = continueProducer[0]
	}
	if skipProducer {
		producer.When = "false"
	}
	consumer := &spec.Job{Name: "consumer", DependsOn: []string{"producer"}, Steps: []*spec.Step{
		{Name: "fetch", Uses: "builtin:artifacts", Action: "download", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"job": "producer", "paths": []any{"dist/result.txt"}})},
		controllerWorkerShell("consume", "printf unexpected-host-execution > host-fallback-marker"),
	}}
	return submitChainFixtureJobs(t, f, producer, consumer)
}

func submitChainFixtureJobs(t *testing.T, f *controllerWorkerFixture, jobs ...*spec.Job) *model.PipelineRun {
	t.Helper()
	run := &model.PipelineRun{RunID: id.GetUild(), PipelineID: f.pipelineID, Status: model.PipelineStatusPending, Env: "{}", TotalJobs: len(jobs)}
	if err := f.repos.Pipeline.CreateRun(f.ctx, run); err != nil {
		t.Fatal(err)
	}
	pipeline := &spec.Pipeline{Namespace: "synthetic-offline-chain", Runtime: &spec.Runtime{Type: taskpayload.RuntimeContainerdOfflineV1, Image: f.conf.Agent.Sandbox.Containerd.Policy.AllowedImages[0], Resources: &spec.Resources{CpuLimit: "500m", MemoryLimit: "64Mi"}}, Jobs: jobs}
	if err := f.engine.Submit(run, pipeline); err != nil {
		t.Fatalf("Process.Submit rejected explicit offline chain: %v", err)
	}
	return run
}

func assertChainFixtureReceipt(t *testing.T, f *controllerWorkerFixture, run *model.PipelineRun, job *model.JobRun, wantStatus int) {
	t.Helper()
	f.mu.Lock()
	task, queued := f.tasks[job.JobRunID]
	f.mu.Unlock()
	var payload taskpayload.JobRunTaskPayload
	if !queued || sonic.Unmarshal(task.Payload, &payload) != nil || payload.Runtime == nil {
		t.Fatalf("job missing real Process queue/runtime payload: %+v", job)
	}
	digest, err := taskpayload.RuntimeBindingDigest(f.pipelineID, run.RunID, job.JobRunID, f.conf.Agent.ID, payload.Runtime)
	if err != nil || payload.Runtime.BindingDigest != digest || job.RuntimeBindingDigest != digest || job.AgentID != f.conf.Agent.ID || job.Status != wantStatus || job.StartTime == nil || job.EndTime == nil {
		t.Fatalf("job lost runtime/Agent/terminal binding: %+v, %v", job, err)
	}
	if wantStatus == model.JobRunStatusSuccess {
		assertImmutableJobInputRecord(t, job, &payload)
	}
	f.assertReceipt("job", job.JobRunID, job.ClaimID, job.TerminalReportDigest, run.RunID, job.JobRunID)
	f.wait("terminal outbox drained", func() bool {
		f.worker.reports.mu.Lock()
		defer f.worker.reports.mu.Unlock()
		return len(f.worker.reports.entries) == 0
	})
}

// TestControllerWorkerOfflineChainProducerFailureBlocksConsumer 验证上游执行、封存或清理失败不会派发下游
func TestControllerWorkerOfflineChainProducerFailureBlocksConsumer(t *testing.T) {
	for _, test := range []struct {
		name, failure, message string
		continueOnError        bool
	}{
		{name: "shell_exit", failure: "command_exit", message: "23"},
		{name: "runtime_execute", failure: "execute", message: "synthetic execute failure"},
		{name: "runtime_execute_continue_on_error", failure: "execute", message: "synthetic execute failure", continueOnError: true},
		{name: "joined_runtime_continue_on_error", failure: "joined_execute", message: "synthetic runtime failure", continueOnError: true},
		{name: "start_continue_on_error", failure: "start", message: "synthetic start failure", continueOnError: true},
		{name: "seal", failure: "seal", message: "synthetic seal failure"},
		{name: "close", failure: "close", message: "synthetic close failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := test.failure
			f, runtime := newChainFailureFixture(t, failure)
			run := submitChainFailurePipeline(t, f, false, test.continueOnError)
			completed := f.waitRun(run.RunID, model.PipelineStatusFailed)
			if completed.FailedJobs != 1 || completed.EndTime == nil {
				t.Fatalf("failed chain not persisted: %+v", completed)
			}
			jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
			if err != nil || len(jobs) != 1 || jobs[0].JobName != "producer" {
				t.Fatalf("blocked downstream unexpectedly created/dispatched: jobs=%+v error=%v", jobs, err)
			}
			producer := jobs[0]
			assertChainFixtureReceipt(t, f, run, producer, model.JobRunStatusFailed)
			if !strings.Contains(producer.ErrorMessage, test.message) {
				t.Fatalf("original lifecycle error lost: %s", producer.ErrorMessage)
			}
			f.mu.Lock()
			taskCount := len(f.tasks)
			f.mu.Unlock()
			if calls, _ := runtime.snapshot("consumer"); taskCount != 1 || len(calls) != 0 {
				t.Fatalf("failed predecessor unlocked downstream: queued=%d consumer=%v", taskCount, calls)
			}
			f.wait("failed producer runtime acknowledgement", func() bool { return !runtime.HasJob(producer.JobRunID) })
			calls, workspace := runtime.snapshot("producer")
			want := []string{"allocate", "start", "execute", "seal", "close", "acknowledge"}
			if failure == "start" {
				want = []string{"allocate", "start", "seal", "close", "acknowledge"}
			} else if failure == "seal" {
				want = []string{"allocate", "start", "execute", "seal", "seal", "close", "acknowledge"}
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("failed runtime lifecycle=%v want=%v", calls, want)
			}
			if _, err := os.Stat(filepath.Join(workspace, "host-fallback-marker")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("runtime failure fell back to host Shell: %v", err)
			}
			steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID, PageSize: 100})
			if err != nil || count != 2 {
				t.Fatalf("Process-created producer steps=%d: %v", count, err)
			}
			wanted := map[string]int{"produce": 4, "publish": 4}
			if failure == "execute" || failure == "command_exit" || failure == "joined_execute" || failure == "start" {
				wanted["produce"], wanted["publish"] = 5, 8
			} else if failure == "seal" {
				wanted["publish"] = 5
			}
			for _, step := range steps {
				if step.JobRunID != producer.JobRunID || int(step.Status) != wanted[step.Name] || step.EndTime == nil {
					t.Fatalf("incorrect failed predecessor step: %+v", step)
				}
				f.assertReceipt("step", step.StepRunID, step.ClaimID, step.TerminalReportDigest, run.RunID, producer.JobRunID)
			}
			f.store.mu.Lock()
			uploads, downloads := f.store.uploads, f.store.downloads
			f.store.mu.Unlock()
			wantUploads := 0
			if failure == "close" {
				wantUploads = 1
			}
			if uploads != wantUploads || downloads != 0 {
				t.Fatalf("failure allowed unintended artifact transfers: uploads=%d downloads=%d", uploads, downloads)
			}
		})
	}
}

// TestControllerWorkerOfflineChainSkippedProducerRequiresArtifactBeforeStart 验证跳过上游仍解锁依赖但缺失输入禁止启动会话
func TestControllerWorkerOfflineChainSkippedProducerRequiresArtifactBeforeStart(t *testing.T) {
	f, runtime := newChainFailureFixture(t, "")
	run := submitChainFailurePipeline(t, f, true)
	f.waitRun(run.RunID, model.PipelineStatusFailed)
	jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("skipped upstream did not unlock consumer: jobs=%d error=%v", len(jobs), err)
	}
	var producer, consumer *model.JobRun
	for _, job := range jobs {
		if job.JobName == "producer" {
			producer = job
		} else if job.JobName == "consumer" {
			consumer = job
		}
	}
	if producer == nil || consumer == nil || producer.Status != model.JobRunStatusSkipped || producer.ClaimID != "" {
		t.Fatalf("upstream skip state changed: producer=%+v consumer=%+v", producer, consumer)
	}
	assertChainFixtureReceipt(t, f, run, consumer, model.JobRunStatusFailed)
	if !strings.Contains(consumer.ErrorMessage, "matched no files") {
		t.Fatalf("consumer did not fail on its required artifact: %s", consumer.ErrorMessage)
	}
	f.mu.Lock()
	_, producerQueued := f.tasks[producer.JobRunID]
	taskCount := len(f.tasks)
	f.mu.Unlock()
	if calls, _ := runtime.snapshot("producer"); producerQueued || taskCount != 1 || len(calls) != 0 {
		t.Fatalf("skipped producer reached worker: queued=%v tasks=%d lifecycle=%v", producerQueued, taskCount, calls)
	}
	f.wait("failed consumer runtime acknowledgement", func() bool { return !runtime.HasJob(consumer.JobRunID) })
	calls, _ := runtime.snapshot("consumer")
	if !reflect.DeepEqual(calls, []string{"allocate", "seal", "close", "acknowledge"}) {
		t.Fatalf("missing input reached container Start/Execute: %v", calls)
	}
	steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID, PageSize: 100})
	if err != nil || count != 4 {
		t.Fatalf("skipped chain steps=%d: %v", count, err)
	}
	for _, step := range steps {
		want := 8
		if step.JobRunID == consumer.JobRunID && step.Name == "fetch" {
			want = 5
		}
		if int(step.Status) != want || step.EndTime == nil {
			t.Fatalf("missing input step state: %+v", step)
		}
		if step.JobRunID == consumer.JobRunID {
			f.assertReceipt("step", step.StepRunID, step.ClaimID, step.TerminalReportDigest, run.RunID, consumer.JobRunID)
		}
	}
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	if f.store.uploads != 0 || f.store.downloads != 0 {
		t.Fatalf("missing input caused storage transfer: uploads=%d downloads=%d", f.store.uploads, f.store.downloads)
	}
}

// TestControllerWorkerOfflineChainCommandExitMayContinue 验证真实管理执行及收据包装保留命令退出类型并允许显式继续
func TestControllerWorkerOfflineChainCommandExitMayContinue(t *testing.T) {
	f, runtime := newChainFailureFixture(t, "command_exit_once")
	first := controllerWorkerShell("exit", "exit 9")
	first.ContinueOnError = true
	run := submitChainFixtureJobs(t, f,
		&spec.Job{Name: "producer", Steps: []*spec.Step{
			first,
			controllerWorkerShell("produce", "printf unexpected-host-execution > host-fallback-marker"),
			{Name: "publish", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"paths": []any{"dist"}})},
		}},
		&spec.Job{Name: "consumer", DependsOn: []string{"producer"}, Steps: []*spec.Step{
			{Name: "fetch", Uses: "builtin:artifacts", Action: "download", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"job": "producer", "paths": []any{"dist/result.txt"}})},
			controllerWorkerShell("consume", "printf unexpected-host-execution > host-fallback-marker"),
		}},
	)
	completed := f.waitRun(run.RunID, model.PipelineStatusSuccess)
	if completed.CompletedJobs != 2 || completed.FailedJobs != 0 || completed.EndTime == nil {
		t.Fatalf("allowed command exit prevented successful chain completion: %+v", completed)
	}
	jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("successful chain jobs=%d: %v", len(jobs), err)
	}
	byName := make(map[string]*model.JobRun)
	for _, job := range jobs {
		byName[job.JobName] = job
		assertChainFixtureReceipt(t, f, run, job, model.JobRunStatusSuccess)
		f.wait("successful runtime acknowledgement", func() bool { return !runtime.HasJob(job.JobRunID) })
		calls, workspace := runtime.snapshot(job.JobName)
		want := []string{"allocate", "start", "execute", "seal", "close", "acknowledge"}
		if job.JobName == "producer" {
			want = []string{"allocate", "start", "execute", "execute", "seal", "close", "acknowledge"}
		}
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("allowed command exit changed %s lifecycle: %v", job.JobName, calls)
		}
		if _, err := os.Stat(filepath.Join(workspace, "host-fallback-marker")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("managed runtime fell back to host Shell: %v", err)
		}
	}
	if byName["producer"] == nil || byName["consumer"] == nil {
		t.Fatalf("chain job names changed: %v", byName)
	}
	steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID, PageSize: 100})
	if err != nil || count != 5 {
		t.Fatalf("continued chain steps=%d: %v", count, err)
	}
	seen := make(map[string]bool)
	for _, step := range steps {
		seen[step.Name] = true
		wantStatus, wantExit := 4, 0
		if step.Name == "exit" {
			wantStatus, wantExit = 5, 9
			if step.ContinueOnError != 1 || !strings.Contains(step.ErrorMessage, "9") {
				t.Fatalf("original nonzero exit receipt was hidden: %+v", step)
			}
		}
		if int(step.Status) != wantStatus || step.ExitCode == nil || *step.ExitCode != wantExit || step.EndTime == nil {
			t.Fatalf("continued step did not retain its actual result: %+v", step)
		}
		f.assertReceipt("step", step.StepRunID, step.ClaimID, step.TerminalReportDigest, run.RunID, step.JobRunID)
	}
	for _, name := range []string{"exit", "produce", "publish", "fetch", "consume"} {
		if !seen[name] {
			t.Fatalf("step %s did not execute", name)
		}
	}
	_, workspace := runtime.snapshot("consumer")
	if got := artifactTestRead(t, workspace, "dist/result.txt"); got != "synthetic producer output" {
		t.Fatalf("continued producer output did not reach downstream: %q", got)
	}
	f.mu.Lock()
	taskCount := len(f.tasks)
	f.mu.Unlock()
	f.store.mu.Lock()
	uploads, downloads := f.store.uploads, f.store.downloads
	f.store.mu.Unlock()
	if taskCount != 2 || uploads != 1 || downloads < 1 {
		t.Fatalf("continued chain did not use real queues/storage steps: tasks=%d uploads=%d downloads=%d", taskCount, uploads, downloads)
	}
}

// TestOfflineChainInputFailureNeverStartsSession 直接工作器补充测试，不声称使用真实控制面或容器
func TestOfflineChainInputFailureNeverStartsSession(t *testing.T) {
	for _, failure := range []string{"expired", "storage_changed", "storage_missing", "empty_object_key", "missing_object", "traversal", "symlink", "interrupted", "sha256_mismatch", "size_mismatch"} {
		t.Run(failure, func(t *testing.T) {
			conf, payload, runtime := offlineWorkerFixture(t)
			payload.JobName = "consumer"
			const name = "producer/dist/result.txt"
			payload.ArtifactURIs = map[string]string{name: "object"}
			payload.ArtifactReferences = artifactTestReferences(payload.ArtifactURIs, map[string][]byte{"object": []byte("input")})
			reference := payload.ArtifactReferences[name]
			st := &memoryArtifactStorage{objects: map[string][]byte{"object": []byte("input")}}
			outside := t.TempDir()
			artifactTestWrite(t, outside, "sentinel", "unchanged")
			wantError := ""
			switch failure {
			case "expired":
				ref := reference
				ref.ExpiresAt = time.Now().Add(-time.Minute).Unix()
				payload.ArtifactReferences[name] = ref
				wantError = "expired"
			case "storage_changed":
				ref := reference
				ref.Fingerprint = strings.Repeat("b", 64)
				payload.ArtifactReferences[name] = ref
				wantError = "storage identity"
			case "storage_missing":
				payload.ArtifactReferences = nil
				wantError = "complete references"
			case "empty_object_key":
				payload.ArtifactURIs[name] = " \t"
				wantError = "invalid or unordered input artifact"
			case "missing_object":
				payload.ArtifactURIs[name] = "missing-object"
				wantError = "artifact"
			case "traversal":
				payload.ArtifactURIs = map[string]string{"../outside": "object"}
				payload.ArtifactReferences = map[string]taskpayload.ArtifactReference{"../outside": reference}
				wantError = "invalid or unordered input artifact"
			case "symlink":
				if err := os.Symlink(outside, filepath.Join(runtime.session.workspace, ".artifacts")); err != nil {
					t.Fatal(err)
				}
				wantError = "symbolic artifact links"
			case "sha256_mismatch", "size_mismatch":
				artifactTestWrite(t, runtime.session.workspace, ".artifacts/"+name, "original")
				if failure == "sha256_mismatch" {
					st.objects["object"] = []byte("other")
					wantError = "SHA256 does not match"
				} else {
					st.objects["object"] = []byte("longer input")
					wantError = "exceeds registered size"
				}
			case "interrupted":
				artifactTestWrite(t, runtime.session.workspace, ".artifacts/"+name, "original")
				st.download = func(context.Context, string) (io.ReadCloser, error) { return &interruptedArtifactReader{}, nil }
				wantError = "artifact"
			}
			SetStorage(st, reference)
			t.Cleanup(func() { SetStorage(nil) })
			manager := executor.NewExecutorManager()
			manager.Register(executor.NewShellExecutor())
			client := newImmutableInputFixtureClient(payload, &artifactStatusClient{})
			err := executeJobRun(withOfflineJobRuntime(context.Background(), runtime), conf, &agentgrpc.ClientWrapper{AgentClient: client}, manager, payload)
			if err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("input failure not propagated: %v", err)
			}
			if !reflect.DeepEqual(*runtime.session.calls, []string{"allocate", "seal", "close"}) || runtime.acknowledgements != 1 {
				t.Fatalf("bad input reached Start/Execute or leaked runtime: calls=%v acknowledged=%d", *runtime.session.calls, runtime.acknowledgements)
			}
			if got := client.jobReports[len(client.jobReports)-1]; got.Status != model.JobRunStatusFailed || got.RuntimeBindingDigest != payload.Runtime.BindingDigest {
				t.Fatalf("invalid input terminal identity: %+v", got)
			}
			for _, report := range client.stepReports {
				if report.Status == 3 || report.Status == 4 {
					t.Fatalf("command ran despite rejected input: %+v", report)
				}
			}
			wantDownloads := 0
			if failure == "interrupted" || failure == "missing_object" || failure == "sha256_mismatch" || failure == "size_mismatch" {
				wantDownloads = 1
			}
			if st.downloads != wantDownloads {
				t.Fatalf("rejected input reached storage: %d downloads", st.downloads)
			}
			if failure == "sha256_mismatch" || failure == "size_mismatch" {
				if client.inputCalls != 2 || client.pinned == nil {
					t.Fatal("corrupt transfer was not checked against an already pinned input")
				}
				manifest, err := taskpayload.ParseJobInputManifest(client.pinned.ManifestJson)
				if err != nil || len(manifest.Artifacts) != 1 || manifest.Artifacts[0].Reference.SHA256 != reference.SHA256 || *manifest.Artifacts[0].Reference.Size != *reference.Size {
					t.Fatalf("corrupt transfer changed the original expected content: %v", err)
				}
			}
			if failure == "interrupted" || failure == "sha256_mismatch" || failure == "size_mismatch" {
				if st.downloads != 1 || artifactTestRead(t, runtime.session.workspace, ".artifacts/"+name) != "original" {
					t.Fatal("failed staging replaced the existing destination")
				}
				matches, err := filepath.Glob(filepath.Join(runtime.session.workspace, ".artifacts", "producer", "dist", ".arcentra-artifact-*"))
				if err != nil || len(matches) != 0 {
					t.Fatalf("failed staging retained partial files: %v %v", matches, err)
				}
			}
			if artifactTestRead(t, outside, "sentinel") != "unchanged" {
				t.Fatal("artifact input changed a file outside its workspace")
			}
		})
	}
}
