//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	controlconfig "github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/logstream"
	kafkaconfig "github.com/arcentrix/arcentra/pkg/mq/kafka"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/sandbox"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestRealContainerAcceptanceGate(t *testing.T) {
	t.Run("generation-admission-observer", testContainerAcceptanceAdmissionObserver)
	t.Run("exact-retained-quarantine-drain", testContainerAcceptanceRetainedAuditDrain)
	image := "docker.io/library/alpine@sha256:" + strings.Repeat("a", 64)
	complete := map[string]string{"ARCENTRA_CONTAINERD_ACCEPTANCE": "1", "ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE": "1", "ARCENTRA_REAL_STACK": "1", "ARCENTRA_REQUIRE_REAL_STACK": "1", "ARCENTRA_TEST_MYSQL_DSN": "synthetic", "ARCENTRA_TEST_KAFKA_BROKERS": "127.0.0.1:19092", "ARCENTRA_TEST_S3_ENDPOINT": "http://127.0.0.1:19000", "ARCENTRA_TEST_S3_ACCESS_KEY": "synthetic", "ARCENTRA_TEST_S3_SECRET_KEY": "synthetic", "ARCENTRA_CONTAINERD_SOCKET": "/tmp/acceptance/containerd.sock", "ARCENTRA_SANDBOX_TEST_IMAGE": image, "ARCENTRA_TEST_CONTAINERD_NAMESPACE": "arcentra-containerjob-acceptance-synthetic", "ARCENTRA_TEST_CONTAINERD_ROOT": "/tmp/acceptance/job", "ARCENTRA_CONTAINERD_RUNC_ROOT": "/tmp/acceptance/runc", "ARCENTRA_CONTAINERD_FIFO_DIR": "/tmp/acceptance/fifo"}
	if cfg, err := realContainerEnvironment(func(string) string { return "" }); cfg != nil || err != nil {
		t.Fatalf("ordinary test run must remain disabled: %v %v", cfg, err)
	}
	if cfg, err := realContainerEnvironment(func(key string) string { return complete[key] }); cfg == nil || err != nil {
		t.Fatalf("complete opt-in rejected: %v", err)
	}
	for _, key := range []string{"ARCENTRA_CONTAINERD_ACCEPTANCE", "ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", "ARCENTRA_REAL_STACK", "ARCENTRA_REQUIRE_REAL_STACK", "ARCENTRA_CONTAINERD_SOCKET", "ARCENTRA_SANDBOX_TEST_IMAGE", "ARCENTRA_TEST_CONTAINERD_NAMESPACE", "ARCENTRA_TEST_CONTAINERD_ROOT", "ARCENTRA_CONTAINERD_RUNC_ROOT", "ARCENTRA_CONTAINERD_FIFO_DIR"} {
		t.Run("missing_"+key, func(t *testing.T) {
			if _, err := realContainerEnvironment(func(name string) string {
				if name == key {
					return ""
				}
				return complete[name]
			}); err == nil {
				t.Fatal("missing dedicated prerequisite did not fail closed")
			}
		})
	}
	for _, invalid := range []struct{ key, value string }{
		{"ARCENTRA_CONTAINERD_ACCEPTANCE", "true"},
		{"ARCENTRA_SANDBOX_TEST_IMAGE", "alpine:latest"},
		{"ARCENTRA_TEST_CONTAINERD_NAMESPACE", "arcentra"},
		{"ARCENTRA_CONTAINERD_SOCKET", "/run/containerd/containerd.sock"},
		{"ARCENTRA_CONTAINERD_RUNC_ROOT", "/run/containerd/runc"},
		{"ARCENTRA_CONTAINERD_FIFO_DIR", "/run/containerd/fifo"},
		{"ARCENTRA_CONTAINERD_FIFO_DIR", "/var/run/containerd/fifo"},
		{"ARCENTRA_CONTAINERD_FIFO_DIR", "relative/fifo"},
		{"ARCENTRA_CONTAINERD_FIFO_DIR", "/tmp/../tmp/fifo"},
		{"ARCENTRA_TEST_CONTAINERD_ROOT", "/tmp/../tmp/acceptance"},
	} {
		if _, err := realContainerEnvironment(func(key string) string {
			if key == invalid.key {
				return invalid.value
			}
			return complete[key]
		}); err == nil {
			t.Fatalf("unsafe acceptance setting was admitted: %s", invalid.key)
		}
	}
}

// TestRealContainerJobAcceptance 验证真实控制面提交到离线容器执行、认证终态和制品发布闭环
// 关闭门禁时只替换测试构造；真实顺序链仍须经过已登记对象存储制品传递
func TestRealContainerJobAcceptance(t *testing.T) {
	settings, err := realContainerEnvironment(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if settings == nil {
		t.Skip("dedicated authorized real-container acceptance is opt-in")
	}
	if testing.Short() || runtime.GOOS != "linux" || os.Geteuid() == 0 || os.Getegid() == 0 {
		t.Fatal("acceptance requires a non-root Linux Agent and must not use -short or sudo go test")
	}
	if info, err := os.Lstat(settings.socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("dedicated containerd socket unavailable: %v", err)
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Fatalf("cgroup v2 is required: %v", err)
	}
	for _, path := range []string{settings.socket, settings.root, settings.runtimeRoot, settings.fifoDir} {
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || canonical != path {
			t.Fatalf("acceptance path must exist and contain no symbolic links: %s (%v)", path, err)
		}
	}
	info, err := os.Lstat(settings.root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("pre-authorized test root must already exist with private mode 0700: %v", err)
	}
	// Retain test-owned state on failure. Runtime cleanup still targets exact journal owners.
	root, err := os.MkdirTemp(settings.root, "whole-job-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			if err := os.RemoveAll(root); err != nil {
				t.Errorf("remove exact successful acceptance fixture root: %v", err)
			}
		}
	})
	t.Logf("container acceptance retained-evidence root=%s namespace=%s image=%s", root, settings.namespace, settings.image)
	workspace, journal := filepath.Join(root, "workspaces"), filepath.Join(root, "journal")
	for _, path := range []string{workspace, journal} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	policy := taskpayload.OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowWholeJobRetryApproval: true, AllowedImages: []string{settings.image}, MaxCPU: "500m", MaxMemory: "64Mi"}
	setupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db := realStackDatabase(t, setupCtx, settings.stack.dsn)
	databaseDiagnostics := installContainerDatabaseDiagnostics(t, db)
	defer databaseDiagnostics.reportFailure(t)
	store, storageConfig, reference := realStackStorage(t, setupCtx, settings.stack)
	observedStore := &containerAcceptanceStorage{S3Storage: store, socket: settings.socket, namespace: settings.namespace, journal: journal, workspace: workspace}
	for _, address := range strings.Split(settings.stack.brokers, ",") {
		realStackLoopback(t, strings.TrimSpace(address), "Kafka")
	}
	publisher, err := executor.NewReliableKafkaLogPublisher(kafkaconfig.Config{BootstrapServers: settings.stack.brokers, SecurityProtocol: "PLAINTEXT"}, "container-acceptance-"+id.GetUild(), logstream.SpoolConfig{Directory: filepath.Join(root, "logs"), MaxDiskUsageMB: 8}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(publisher.Close)
	approvalPolicy := &retryapproval.Policy{ID: "approval-" + id.GetUild(), Candidates: []string{"reviewer-a-" + id.GetUild(), "reviewer-b-" + id.GetUild()}, Mode: "all", RequiredApproverCount: 2, TimeoutSeconds: 60}
	var approvalServices *controlservice.Services
	lifecycleAudit := &containerApprovalLifecycle{calls: make(map[string]map[string]int)}
	removeObserver, err := sandbox.ObserveAcceptanceLifecycle(settings.namespace, lifecycleAudit.record)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(removeObserver)
	options := controllerWorkerTLS(t)
	options.timeout = 7 * time.Minute
	options.backend = &controllerWorkerBackend{db: db, brokers: settings.stack.brokers, store: observedStore, storageConfig: storageConfig, reference: reference, logPublisher: publisher, authentication: realStackAuthentication, prepareTopics: func(t *testing.T, ctx context.Context, agentID string) {
		realStackTopics(t, ctx, settings.stack.brokers, agentID)
	}}
	options.configureAgent = func(conf *config.AgentConfig) {
		conf.Agent.Mode = "sandbox"
		conf.Agent.WorkspaceDir = workspace
		conf.Agent.Sandbox = config.SandboxConfig{Enable: true, Runtime: "containerd", Containerd: config.ContainerdConfig{Policy: policy, Namespace: settings.namespace, JournalDir: journal, User: fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()), Network: "none", UnixSocket: settings.socket, RuntimeRoot: settings.runtimeRoot, FIFODir: settings.fifoDir}}
	}
	options.configureRepositories = func(repositories *repo.Repositories) {
		approvalServices = controlservice.NewServices(workerTestDatabase{db}, nil, repositories)
		if err := approvalServices.ConfigureRetryApprovalPolicy(approvalPolicy); err != nil {
			t.Fatal(err)
		}
		if err := approvalServices.ConfigureRetryApprovalAuthorization(); err != nil {
			t.Fatal(err)
		}
	}
	options.startWorker = startContainerAcceptanceWorker
	if taskpayload.OfflineRuntimeAccepted {
		options.processConfig = &controlconfig.AppConfig{Pipeline: controlconfig.PipelineConfig{OfflineRuntime: policy, RetryApproval: approvalPolicy}}
	}
	var f *controllerWorkerFixture
	t.Cleanup(func() {
		if f != nil {
			realStackCleanupDatabase(t, f, storageConfig.StorageID)
		}
	})
	f = newControllerWorkerFixture(t, true, options)
	if !taskpayload.OfflineRuntimeAccepted {
		if err := f.engine.ConfigureContainerAcceptancePolicy(policy); err != nil {
			t.Fatal(err)
		}
		t.Log("production gate remains closed; explicit acceptance-only constructors are in use")
	} else {
		t.Log("production gate is open; actual NewProcess and StartWorker constructors are in use")
	}
	if f.store != nil || f.db.Dialector.Name() != "mysql" || !f.client.ControllerAuthenticated() || !f.worker.OfflineRuntimeReady() {
		t.Fatal("real authenticated dependency/runtime setup was replaced or is not ready")
	}
	if err := f.repos.Agent.Patch(f.ctx, f.conf.Agent.ID, map[string]any{"os": "linux", "arch": runtime.GOARCH}); err != nil {
		t.Fatal(err)
	}
	streamService := controlservice.NewStreamService(nil, db, controlservice.KafkaSettings{BootstrapServers: settings.stack.brokers, SecurityProtocol: "PLAINTEXT"})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := streamService.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	realStackRejectInvalidToken(t, f)
	var sourceFetches atomic.Int64
	sourceURL, initialCommit := containerAcceptanceSource(t, f.ctx, func() error {
		records, err := containerAcceptanceRecords(journal)
		if err != nil || len(records) != 1 || records[0].Record.Phase != "allocated" {
			return fmt.Errorf("source staging did not precede real container startup: %v", err)
		}
		sourceFetches.Add(1)
		return nil
	})
	var blockJobACK atomic.Bool
	f.mu.Lock()
	f.beforeTerminal = func(_ context.Context, message proto.Message) error {
		if _, ok := message.(*agentv1.ReportJobRunStatusRequest); ok && blockJobACK.Load() {
			return status.Error(codes.Unavailable, "synthetic continued ACK outage until same-journal restart")
		}
		return nil
	}
	f.observeTerminal = func(_ context.Context, message proto.Message, _ any, err error) {
		if request, ok := message.(*agentv1.ReportJobRunStatusRequest); ok && len(request.Artifacts) > 0 && err == nil {
			blockJobACK.Store(true)
		}
	}
	f.mu.Unlock()
	command := fmt.Sprintf(`test "$(id -u)" = %d
 test "$(pwd)" = /workspace
 test "$(cat source.txt)" = 'initial checkout'
 test "$(ls /sys/class/net)" = lo
 printf x > executions
 mkdir -p dist
 cp source.txt dist/result.txt
 cp executions dist/executions
 (while :; do printf 'tick\n' >> dist/background.txt; sleep 0.05; done) </dev/null >/dev/null 2>&1 &
 while [ ! -s dist/background.txt ]; do sleep 0.01; done
 printf 'container-whole-job-stdout\n'
 printf 'container-whole-job-stderr\n' >&2`, os.Geteuid())
	run := containerAcceptanceSubmit(t, f, settings, &spec.Job{Name: "build", Source: &spec.Source{Type: "git", Repo: sourceURL, Branch: "main"}, Steps: []*spec.Step{controllerWorkerShell("produce", command), controllerWorkerShell("sequential", "test \"$(cat executions)\" = x; printf 'container-second-step\\n'"), {Name: "publish", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"paths": []any{"dist"}, "expireIn": "7d"})}}})
	f.waitRun(run.RunID, model.PipelineStatusSuccess)
	job := containerAcceptanceJob(t, f, run.RunID, model.JobRunStatusSuccess)
	if job.SourceResolvedCommit != initialCommit || job.SourceProvenanceState != "reported" || job.SourceBindingDigest == "" {
		t.Fatalf("host Git staging lost initial checkout binding: %+v", job)
	}
	if sourceFetches.Load() == 0 {
		t.Fatal("no actual host-side source transfer was observed before container start")
	}
	f.wait("persisted job receipt survives committed ACK outage", func() bool {
		f.worker.reports.mu.Lock()
		defer f.worker.reports.mu.Unlock()
		_, ok := f.worker.reports.entries[terminalRecordName("job", job.JobRunID)]
		return ok && blockJobACK.Load()
	})
	recovery := f.worker.offline.controller.Recoveries()
	if len(recovery) != 1 || recovery[0].JobRunID != job.JobRunID || recovery[0].ExecutionClaimID != job.ClaimID || recovery[0].RuntimeBindingDigest != job.RuntimeBindingDigest || !recovery[0].CleanupComplete {
		t.Fatalf("runtime tombstone lost original claim or cleanup evidence: %+v", recovery)
	}
	steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID, PageSize: 100})
	if err != nil || count != 3 {
		t.Fatalf("step count=%d: %v", count, err)
	}
	for _, step := range steps {
		if step.Status != 4 || step.Attempt != 1 || step.ExitCode == nil || *step.ExitCode != 0 {
			t.Fatalf("invalid real shell/artifact step: %+v", step)
		}
		f.assertReceipt("step", step.StepRunID, step.ClaimID, step.TerminalReportDigest, run.RunID, job.JobRunID)
	}
	realStackWaitLogs(t, f, steps, map[string]string{"container-whole-job-stdout": "stdout", "container-whole-job-stderr": "stderr", "container-second-step": "stdout"})
	containerAcceptanceArtifacts(t, f, observedStore, job)
	// Close workers and discard only their volatile execution caches. Keep the same durable
	// outbox, runtime journal, Agent ID and verified controller TLS identity for restart.
	oldOutbox := f.worker.reports
	if err := f.worker.Stop(); err != nil {
		t.Fatal(err)
	}
	cacheCtx := withTerminalOutbox(context.Background(), oldOutbox)
	pendingJobReports.Delete(receiptKey(cacheCtx, "job", job.JobRunID))
	completeExecutionClaim(cacheCtx, "job", job.JobRunID)
	for _, step := range steps {
		pendingStepReports.Delete(receiptKey(cacheCtx, "step", step.StepRunID))
		completeExecutionClaim(cacheCtx, "step", step.StepRunID)
	}
	f.mu.Lock()
	f.observeTerminal = nil
	f.beforeTerminal = nil
	inspections := make(chan *agentv1.InspectJobExecutionRequest, 4)
	f.observeRequest = func(_ context.Context, request any) {
		if inspect, ok := request.(*agentv1.InspectJobExecutionRequest); ok {
			select {
			case inspections <- proto.Clone(inspect).(*agentv1.InspectJobExecutionRequest):
			default:
			}
		}
	}
	f.mu.Unlock()
	blockJobACK.Store(false)
	manager := executor.NewExecutorManager()
	manager.Register(executor.NewShellExecutor())
	manager.SetLogPublisher(publisher)
	restarted, err := startContainerAcceptanceWorker(f.ctx, f.conf, f.client, manager)
	if err != nil {
		t.Fatalf("same-journal restart failed: %v", err)
	}
	f.worker = restarted.(*workerQueues)
	if !f.worker.OfflineRuntimeReady() || len(f.worker.offline.controller.Recoveries()) != 0 {
		t.Fatal("authoritative original-claim recovery did not become ready")
	}
	select {
	case inspect := <-inspections:
		if inspect.AgentId != f.conf.Agent.ID || inspect.JobRunId != job.JobRunID || inspect.OriginalClaimId != job.ClaimID || inspect.RuntimeBindingDigest != job.RuntimeBindingDigest {
			t.Fatalf("authenticated restart inspected a different execution identity: %+v", inspect)
		}
	default:
		t.Fatal("same-journal restart did not query authoritative execution identity over real TLS gRPC")
	}
	f.assertReceipt("job", job.JobRunID, job.ClaimID, job.TerminalReportDigest, run.RunID, job.JobRunID)
	for _, changed := range []struct{ claim, binding string }{
		{claim: id.GetUUID(), binding: job.RuntimeBindingDigest},
		{claim: job.ClaimID, binding: "v1:" + strings.Repeat("0", 64)},
	} {
		ctx, cancel := f.client.WithTimeoutAndAuth(f.ctx)
		_, err := f.client.AgentClient.InspectJobExecution(ctx, &agentv1.InspectJobExecutionRequest{AgentId: f.conf.Agent.ID, JobRunId: job.JobRunID, OriginalClaimId: changed.claim, RuntimeBindingDigest: changed.binding})
		cancel()
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("authenticated recovery accepted a changed original claim/runtime digest: %v", err)
		}
	}
	f.mu.Lock()
	for _, receipt := range f.receipts["job/"+job.JobRunID] {
		request, ok := receipt.message.(*agentv1.ReportJobRunStatusRequest)
		if !ok || request.RuntimeBindingDigest != job.RuntimeBindingDigest {
			f.mu.Unlock()
			t.Fatal("terminal ACK replay lost the immutable runtime binding")
		}
	}
	replayed := len(f.receipts["job/"+job.JobRunID])
	lost := f.lostACKs
	duplicate := f.tasks[job.JobRunID]
	f.mu.Unlock()
	if replayed < 2 || lost != 1 || len(duplicate.Payload) == 0 {
		t.Fatalf("committed terminal ACK was not durably replayed: receipts=%d lost=%d", replayed, lost)
	}
	if _, err := f.producer.Enqueue(&duplicate, nova.Queue(taskpayload.AgentQueue(f.conf.Agent.ID))); err != nil {
		t.Fatal(err)
	}
	fifo := containerAcceptanceSubmit(t, f, settings, &spec.Job{Name: "fifo-rejected", Steps: []*spec.Step{controllerWorkerShell("create-fifo", "mkdir dist; mkfifo dist/pipe"), {Name: "reject-special-file", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"paths": []any{"dist"}})}}}, containerAcceptanceDependent("fifo-rejected"))
	f.waitRun(fifo.RunID, model.PipelineStatusFailed)
	fifoJob := containerAcceptanceJob(t, f, fifo.RunID, model.JobRunStatusFailed)
	containerAcceptanceBlockedDownstream(t, f, fifoJob)
	if !strings.Contains(strings.ToLower(fifoJob.ErrorMessage), "regular") && !strings.Contains(strings.ToLower(fifoJob.ErrorMessage), "unsupported") {
		t.Fatalf("FIFO was not rejected as a special file: %s", fifoJob.ErrorMessage)
	}
	assertControllerQueueCommitted(t, f, 3)
	actual := containerAcceptanceJob(t, f, run.RunID, model.JobRunStatusSuccess)
	if !reflect.DeepEqual(job, actual) {
		t.Fatal("duplicate real Kafka task changed original accepted execution")
	}
	containerAcceptanceArtifacts(t, f, observedStore, job)
	t.Log("Process -> real Kafka -> container shell -> authenticated TLS gRPC -> MySQL/S3: success, source staging, sequential shell, background seal, FIFO rejection, ACK loss and same-journal restart verified")

	cancelRun := containerAcceptanceSubmit(t, f, settings, &spec.Job{Name: "cancel", Steps: []*spec.Step{controllerWorkerShell("wait", "printf started > started; sleep 60; printf forbidden > after-cancel"), controllerWorkerShell("never", "printf forbidden > later")}}, containerAcceptanceDependent("cancel"))
	var cancelRecord containerAcceptanceJournal
	f.wait("real container command starts before Kafka cancellation", func() bool {
		records, err := containerAcceptanceRecords(journal)
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.Record.RunID == cancelRun.RunID && record.Record.Phase == "executing" {
				if _, err := os.Stat(filepath.Join(workspace, record.Record.Directory, "started")); err == nil {
					cancelRecord = record
					return true
				}
			}
		}
		return false
	})
	if err := f.engine.CancelRun(cancelRun.RunID); err != nil {
		t.Fatal(err)
	}
	f.waitRun(cancelRun.RunID, model.PipelineStatusCancelled)
	controllerWorkerWaitReceipts(t, f, controllerReceiptExpectation{jobID: cancelRecord.Record.JobRunID, runID: cancelRun.RunID, claim: cancelRecord.Record.Claim, runtimeBinding: cancelRecord.Record.Binding, status: model.JobRunStatusCancelled, steps: map[string]int{"wait": 6, "never": 6}})
	cancelJob := containerAcceptanceJob(t, f, cancelRun.RunID, model.JobRunStatusCancelled)
	containerAcceptanceBlockedDownstream(t, f, cancelJob)
	if cancelJob.CancellationClaimID != cancelRecord.Record.Claim {
		t.Fatal("cancellation changed the original execution claim")
	}
	containerAcceptanceStoppedSteps(t, f, cancelJob, map[string]int{"wait": 6, "never": 6})
	containerAcceptanceRemoved(t, f, cancelRecord)
	timeoutStep := controllerWorkerShell("wait", "printf started > started; sleep 60; printf forbidden > after-timeout")
	timeoutStep.Timeout = "2s"
	timeoutRun := containerAcceptanceSubmit(t, f, settings, &spec.Job{Name: "agent-step-timeout", Timeout: "20s", Steps: []*spec.Step{timeoutStep, controllerWorkerShell("never", "printf forbidden > later")}}, containerAcceptanceDependent("agent-step-timeout"))
	var timeoutRecord containerAcceptanceJournal
	f.wait("real container command starts before its deadline", func() bool {
		records, err := containerAcceptanceRecords(journal)
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.Record.RunID == timeoutRun.RunID && record.Record.Phase == "executing" {
				if _, err := os.Stat(filepath.Join(workspace, record.Record.Directory, "started")); err == nil {
					timeoutRecord = record
					return true
				}
			}
		}
		return false
	})
	f.waitRun(timeoutRun.RunID, model.PipelineStatusFailed)
	controllerWorkerWaitReceipts(t, f, controllerReceiptExpectation{jobID: timeoutRecord.Record.JobRunID, runID: timeoutRun.RunID, claim: timeoutRecord.Record.Claim, runtimeBinding: timeoutRecord.Record.Binding, status: model.JobRunStatusTimeout, steps: map[string]int{"wait": 7, "never": 8}})
	timeoutJob := containerAcceptanceJob(t, f, timeoutRun.RunID, model.JobRunStatusTimeout)
	containerAcceptanceBlockedDownstream(t, f, timeoutJob)
	if !strings.Contains(timeoutJob.ErrorMessage, "timeout") && !strings.Contains(timeoutJob.ErrorMessage, "deadline") {
		t.Fatalf("timeout result lost cause: %s", timeoutJob.ErrorMessage)
	}
	containerAcceptanceStoppedSteps(t, f, timeoutJob, map[string]int{"wait": 7, "never": 8})
	containerAcceptanceRemoved(t, f, timeoutRecord)
	healthy := containerAcceptanceSubmit(t, f, settings, &spec.Job{Name: "after-timeout", Steps: []*spec.Step{controllerWorkerShell("healthy", "printf 'container-after-timeout\\n'")}})
	f.waitRun(healthy.RunID, model.PipelineStatusSuccess)
	containerAcceptanceDrain(t, f)
	assertControllerQueueCommitted(t, f, 6)
	retainedAudit := containerAcceptanceControllerTimeout(t, f, settings, publisher)
	containerAcceptanceArtifactChain(t, f, settings, observedStore)
	containerAcceptanceWholeJobRetry(t, f, settings, observedStore, retainedAudit)
	containerAcceptanceRetryApproval(t, f, settings, observedStore, retainedAudit, approvalServices, approvalPolicy, lifecycleAudit)
	testRealContainerGenerationCancellationFence(t, f)
	if !t.Run("failed-step", func(t *testing.T) {
		containerAcceptanceFailedStepRetry(t, f, settings, observedStore, publisher, retainedAudit)
	}) {
		return
	}
	if !t.Run("failed-step-approval", func(t *testing.T) {
		containerAcceptanceFailedStepApproval(t, f, settings, observedStore, publisher, retainedAudit, approvalServices, approvalPolicy, lifecycleAudit)
	}) {
		return
	}
	if !containerAcceptanceAuditMatches(retainedAudit, controllerTerminalRecords(f)) {
		t.Fatal("completed acceptance changed retained timeout audit or left new terminal receipts")
	}
	for _, ref := range observedStore.refs {
		state, err := f.worker.offline.backend.InspectOwned(f.ctx, ref)
		if err != nil || state != (sandbox.ResourceState{}) {
			t.Fatalf("owned container/snapshot/task remains after completion: %+v %v", state, err)
		}
	}
	records, err := containerAcceptanceRecords(journal)
	if err != nil || len(records) != 0 {
		t.Fatalf("runtime tombstones remain: %d %v", len(records), err)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != ".terminal-report-outbox" {
			t.Fatalf("workspace cleanup left %s", entry.Name())
		}
	}
	if t.Failed() {
		return
	}
	t.Log("real linear-chain failure, control-queue cancellation, timeout, registered artifact transfer, subsequent healthy execution and exact owned cleanup verified; bounded whole-job retry and gated failed-step candidate verified; production failed-step enablement still requires separate review; branching and parallel jobs remain unsupported")
}

func containerAcceptanceStoppedSteps(t *testing.T, f *controllerWorkerFixture, job *model.JobRun, expected map[string]int) {
	t.Helper()
	steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: job.PipelineRunID})
	if err != nil || count != 2 {
		t.Fatalf("interrupted job steps=%d: %v", count, err)
	}
	for _, step := range steps {
		want, exists := expected[step.Name]
		if !exists || int(step.Status) != want || step.EndTime == nil || step.TerminalReportDigest == "" || step.AgentID != job.AgentID {
			t.Fatalf("interrupted real step result was not persisted: %+v", step)
		}
		claim := agentidentity.StepClaimID(job.ClaimID, step.StepRunID)
		if step.Name == "never" && want == model.JobRunStatusCancelled {
			if step.ClaimID != "" || step.CancellationClaimID != job.ClaimID {
				t.Fatal("unstarted cancellation invented execution ownership")
			}
		} else if step.ClaimID != claim {
			t.Fatal("step receipt changed the original derived execution claim")
		}
		f.assertReceipt("step", step.StepRunID, claim, step.TerminalReportDigest, job.PipelineRunID, job.JobRunID)
	}
	f.assertReceipt("job", job.JobRunID, job.ClaimID, job.TerminalReportDigest, job.PipelineRunID, job.JobRunID)
}

func containerAcceptanceSubmit(t *testing.T, f *controllerWorkerFixture, cfg *realContainerSettings, jobs ...*spec.Job) *model.PipelineRun {
	t.Helper()
	ctx, cancel := f.client.WithTimeoutAndAuth(f.ctx)
	response, err := f.client.AgentClient.Heartbeat(ctx, &agentv1.HeartbeatRequest{AgentId: f.conf.Agent.ID, Status: agentv1.AgentStatus_AGENT_STATUS_IDLE, Timestamp: time.Now().Unix(), RuntimeCapabilities: &agentv1.RuntimeCapabilities{ContainerdOfflineV1: f.worker.OfflineRuntimeReady(), ContainerdWholeJobRetryV1: f.conf.Agent.Sandbox.Containerd.Policy.AllowWholeJobRetry && f.worker.OfflineRuntimeReady(), ExecutionMode: taskpayload.RuntimeContainerdOfflineV1}})
	cancel()
	if err != nil || response == nil || !response.Success {
		t.Fatalf("authenticated runtime readiness report: %v", err)
	}
	run := &model.PipelineRun{RunID: id.GetUild(), PipelineID: f.pipelineID, Status: model.PipelineStatusPending, Env: `{"ROUNDTRIP_VALUE":"controller-agent-artifact"}`, TotalJobs: len(jobs)}
	if err := f.repos.Pipeline.CreateRun(f.ctx, run); err != nil {
		t.Fatal(err)
	}
	pipeline := &spec.Pipeline{Namespace: "container-acceptance", Runtime: &spec.Runtime{Type: taskpayload.RuntimeContainerdOfflineV1, Image: cfg.image, Resources: &spec.Resources{CpuLimit: "500m", MemoryLimit: "64Mi"}}, Jobs: jobs}
	if err := f.engine.Submit(run, pipeline); err != nil {
		t.Fatalf("actual Process.Submit rejected acceptance job: %v", err)
	}
	return run
}

func containerAcceptanceJob(t *testing.T, f *controllerWorkerFixture, runID string, want int) *model.JobRun {
	t.Helper()
	jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, runID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("expected one Process-created job: %d %v", len(jobs), err)
	}
	return containerAcceptanceBoundJob(t, f, jobs[0], want)
}

func containerAcceptanceBoundJob(t *testing.T, f *controllerWorkerFixture, job *model.JobRun, want int) *model.JobRun {
	t.Helper()
	if job.Status != want || job.AgentID != f.conf.Agent.ID || job.ClaimID == "" || job.RuntimeBindingDigest == "" || job.TerminalReportDigest == "" {
		t.Fatalf("job result lost original runtime/claim binding: %+v", job)
	}
	f.mu.Lock()
	task := f.tasks[job.JobRunID]
	f.mu.Unlock()
	var payload taskpayload.JobRunTaskPayload
	if err := sonic.Unmarshal(task.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	digest, err := taskpayload.RuntimeBindingDigest(f.pipelineID, job.PipelineRunID, job.JobRunID, f.conf.Agent.ID, payload.Runtime)
	if err != nil || digest != job.RuntimeBindingDigest || payload.Runtime.BindingDigest != digest {
		t.Fatalf("Process/Kafka/MySQL runtime binding changed: %v", err)
	}
	if want == model.JobRunStatusSuccess {
		assertImmutableJobInputRecord(t, job, &payload)
	}
	return job
}

func containerAcceptanceDrain(t *testing.T, f *controllerWorkerFixture) {
	t.Helper()
	containerAcceptanceDrainWithAudit(t, f, nil)
}

func containerAcceptanceDrainWithAudit(t *testing.T, f *controllerWorkerFixture, retainedAudit map[string]string) {
	t.Helper()
	var snapshot containerAcceptanceDrainState
	defer func() {
		if !snapshot.drained() {
			t.Logf("drain state: terminal_records=%d expected_audit=%d exact_audit=%t runtime_records=%d recoveries=%d ready=%t", snapshot.terminalRecords, len(retainedAudit), snapshot.exactAudit, snapshot.runtimeRecords, snapshot.recoveries, snapshot.ready)
		}
	}()
	f.wait("terminal receipts and runtime tombstones drain", func() bool {
		terminalRecords := controllerTerminalRecords(f)
		snapshot = containerAcceptanceDrainState{terminalRecords: len(terminalRecords), exactAudit: containerAcceptanceAuditMatches(retainedAudit, terminalRecords), runtimeRecords: -1, recoveries: len(f.worker.offline.controller.Recoveries()), ready: f.worker.OfflineRuntimeReady()}
		records, err := containerAcceptanceRecords(f.conf.Agent.Sandbox.Containerd.JournalDir)
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		snapshot.runtimeRecords = len(records)
		return snapshot.drained()
	})
}

func containerAcceptanceRemoved(t *testing.T, f *controllerWorkerFixture, record containerAcceptanceJournal) {
	t.Helper()
	state, err := f.worker.offline.backend.InspectOwned(f.ctx, record.Record.Resource)
	if err != nil || state != (sandbox.ResourceState{}) {
		t.Fatalf("exact owned resources survived: %+v %v", state, err)
	}
	if _, err := os.Lstat(filepath.Join(f.conf.Agent.WorkspaceDir, record.Record.Directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exact workspace survived cleanup: %v", err)
	}
}

func containerAcceptanceArtifacts(t *testing.T, f *controllerWorkerFixture, store *containerAcceptanceStorage, job *model.JobRun) {
	t.Helper()
	rows, count, err := f.repos.Artifact.List(f.ctx, repo.ArtifactQuery{ProjectID: f.projectID, Limit: 20})
	if err != nil || count != 3 {
		t.Fatalf("real S3 artifact rows=%d: %v", count, err)
	}
	store.mu.Lock()
	uploads, background := store.sealedUploads, bytes.Clone(store.background)
	store.mu.Unlock()
	if uploads != 3 || len(background) == 0 {
		t.Fatalf("sealed S3 publication repeated or missing: %d", uploads)
	}
	expected := map[string][]byte{"dist/result.txt": []byte("initial checkout"), "dist/executions": []byte("x"), "dist/background.txt": background}
	for _, row := range rows {
		wanted, ok := expected[row.Name]
		if !ok || row.JobRunID != job.JobRunID || row.PipelineRunID != job.PipelineRunID || row.StepRunID == "" || row.StorageID == "" || row.StorageFingerprint == "" || row.Size != int64(len(wanted)) || row.SHA256 != fmt.Sprintf("%x", sha256.Sum256(wanted)) {
			t.Fatalf("artifact lost exact producer/storage/digest: %+v", row)
		}
		reader, err := store.S3Storage.DownloadStream(f.ctx, row.StoragePath)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, 1<<20))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || !bytes.Equal(data, wanted) {
			t.Fatalf("real object bytes changed: %s %v %v", row.Name, readErr, closeErr)
		}
	}
}
