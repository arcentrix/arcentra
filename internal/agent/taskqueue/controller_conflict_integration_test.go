// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/nova"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// controllerWorkerRecordingQueue 只旁观真实 Process 投递的任务字节，重投仍走原 Nova/Kafka 协议
// 不手工构造作业、步骤或绕过控制面调度
type controllerWorkerRecordingQueue struct {
	nova.TaskQueue
	fixture *controllerWorkerFixture
}

func (q *controllerWorkerRecordingQueue) Enqueue(task *nova.Task, opts ...nova.Option) (*nova.Result, error) {
	if task != nil && task.Type == taskpayload.TaskTypeJobRun {
		var payload taskpayload.JobRunTaskPayload
		if err := sonic.Unmarshal(task.Payload, &payload); err != nil {
			return nil, err
		}
		q.fixture.mu.Lock()
		q.fixture.tasks[payload.JobRunID] = nova.Task{Type: task.Type, Payload: bytes.Clone(task.Payload)}
		q.fixture.mu.Unlock()
	}
	return q.TaskQueue.Enqueue(task, opts...)
}

// Preserve the optional context capability of the real queue. Recording a
// reference counts publication attempts only, never execution or completion.
func (q *controllerWorkerRecordingQueue) EnqueueContext(ctx context.Context, task *nova.Task, opts ...nova.Option) (*nova.Result, error) {
	publisher, ok := q.TaskQueue.(interface {
		EnqueueContext(context.Context, *nova.Task, ...nova.Option) (*nova.Result, error)
	})
	if !ok {
		return nil, fmt.Errorf("fixture queue lacks context publication")
	}
	if task != nil && task.Type == taskpayload.TaskTypeCopilotRerunReference {
		var ref taskpayload.RerunClaimReference
		if err := sonic.Unmarshal(task.Payload, &ref); err != nil {
			return nil, err
		}
		if err := ref.Validate(); err != nil {
			return nil, err
		}
		canonical, err := json.Marshal(ref)
		if err != nil || !bytes.Equal(canonical, task.Payload) {
			return nil, fmt.Errorf("fixture queue payload is not an exact reference-only message")
		}
		q.fixture.mu.Lock()
		q.fixture.rerunNotifications[ref.JobRunID]++
		q.fixture.rerunReferences[ref.JobRunID] = ref
		q.fixture.mu.Unlock()
	}
	return publisher.EnqueueContext(ctx, task, opts...)
}

func controllerWorkerTLS(t *testing.T) controllerWorkerFixtureOptions {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "local conflict fixture"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(t.TempDir(), "controller-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return controllerWorkerFixtureOptions{
		serverTLS: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}},
		clientTLS: agentgrpc.ClientTLSConfig{Enabled: true, CAFile: caPath, ServerName: "localhost"},
	}
}

type controllerConflictObservation struct {
	kind, runID, claim string
	message            proto.Message
	response           any
	err                error
}

type controllerConflictObserver struct {
	mu    sync.Mutex
	calls []controllerConflictObservation
}

func (o *controllerConflictObserver) observe(ctx context.Context, message proto.Message, response any, err error) {
	kind, runID := controllerTerminalIdentity(message)
	md, _ := metadata.FromIncomingContext(ctx)
	claims := md.Get(executionClaimMetadataKey)
	claim := ""
	if len(claims) == 1 {
		claim = claims[0]
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, controllerConflictObservation{kind: kind, runID: runID, claim: claim, message: proto.Clone(message), response: response, err: err})
}

func (o *controllerConflictObserver) snapshot(runIDs ...string) []controllerConflictObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	var result []controllerConflictObservation
	for _, call := range o.calls {
		for _, runID := range runIDs {
			if call.runID == runID {
				result = append(result, call)
				break
			}
		}
	}
	return result
}

func controllerTerminalAccepted(response any) bool {
	switch r := response.(type) {
	case *agentv1.ReportJobRunStatusResponse:
		return r.GetSuccess()
	case *agentv1.ReportStepRunStatusResponse:
		return r.GetSuccess()
	}
	return false
}

func controllerTerminalIdentity(message proto.Message) (string, string) {
	switch req := message.(type) {
	case *agentv1.ReportJobRunStatusRequest:
		return "job", req.JobRunId
	case *agentv1.ReportStepRunStatusRequest:
		return "step", req.StepRunId
	}
	return "", ""
}

func controllerTerminalRecords(f *controllerWorkerFixture) map[string]terminalRecord {
	f.worker.reports.mu.Lock()
	defer f.worker.reports.mu.Unlock()
	result := make(map[string]terminalRecord)
	for _, entry := range f.worker.reports.entries {
		record := entry.record
		record.Payload = bytes.Clone(record.Payload)
		record.Dependencies = append([]string(nil), record.Dependencies...)
		result[record.Kind+"/"+record.RunID] = record
	}
	return result
}

// TestControllerWorkerTerminalConflictRoundTrip 覆盖真实命令结束后，原始回执迟于控制面终态到达
// 唯一替身是本地 librdkafka broker 与 gRPC 投递故障；调度、执行、认证、仓储及磁盘重放均为真实实现
func TestControllerWorkerTerminalConflictRoundTrip(t *testing.T) {
	for _, authority := range []string{"cancelled", "timeout", "offline_recovered"} {
		for _, outcome := range []string{"success", "failed"} {
			for _, kind := range []string{"step", "job"} {
				t.Run(authority+"/"+outcome+"/"+kind, func(t *testing.T) {
					testControllerWorkerTerminalConflict(t, authority, outcome, kind)
				})
			}
		}
	}
}

func testControllerWorkerTerminalConflict(t *testing.T, authority, outcome, kind string) {
	f := newControllerWorkerFixture(t, false, controllerWorkerTLS(t))
	if !f.client.ControllerAuthenticated() {
		t.Fatal("fixture must authenticate the actual controller transport")
	}
	var gated atomic.Bool
	gated.Store(true)
	observer := &controllerConflictObserver{}
	f.mu.Lock()
	f.beforeTerminal = func(_ context.Context, message proto.Message) error {
		if actual, _ := controllerTerminalIdentity(message); actual == kind && gated.Load() {
			return status.Error(codes.Unavailable, "local test delays original terminal receipt")
		}
		return nil
	}
	f.observeTerminal = observer.observe
	f.mu.Unlock()
	command, localStatus := "printf x >> executions", int32(4)
	if outcome == "failed" {
		command += "; exit 9"
		localStatus = 5
	}
	jobSpec := &spec.Job{Name: "late-terminal", Steps: []*spec.Step{controllerWorkerShell("once", command)}}
	wantSteps := int64(1)
	if kind == "step" {
		jobSpec.Retry = &spec.Retry{Mode: "failed_step", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}
		wantSteps = 2
	}
	run := f.submit(jobSpec)
	var job *model.JobRun
	var step model.StepRun
	var allSteps []model.StepRun
	var original map[string]terminalRecord
	f.wait("original shell result and parent receipt durable before controller terminal", func() bool {
		jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) != 1 {
			return false
		}
		steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID})
		if err != nil {
			t.Fatal(err)
		}
		if count != wantSteps {
			return false
		}
		job, allSteps = jobs[0], steps
		for _, candidate := range steps {
			if candidate.Attempt == 1 {
				step = candidate
			}
		}
		original = controllerTerminalRecords(f)
		_, parentDurable := original["job/"+job.JobRunID]
		_, executing := runningJobRunCancel.Load(job.JobRunID)
		return parentDurable && !executing && job.Status == 3 && job.ClaimID != "" && step.ClaimID != "" &&
			(kind != "job" || step.Status == int(localStatus) && step.TerminalReportDigest != "")
	})
	if job.Status != 3 || job.ClaimID == "" || step.ClaimID == "" || step.Attempt != 1 || step.PreviousStepRunID != "" {
		t.Fatalf("real Agent did not acquire original execution identity: job=%+v step=%+v", job, step)
	}
	workspace := filepath.Join(f.conf.Agent.WorkspaceDir, run.RunID, job.JobRunID)
	if got := artifactTestRead(t, workspace, "executions"); got != "x" {
		t.Fatalf("original shell execution=%q", got)
	}
	originalJob := &agentv1.ReportJobRunStatusRequest{}
	if err := proto.Unmarshal(original["job/"+job.JobRunID].Payload, originalJob); err != nil {
		t.Fatal(err)
	}
	if originalJob.Status != localStatus {
		t.Fatalf("delivery failure rewrote original job result: %v", originalJob)
	}
	if kind == "step" {
		originalStep := &agentv1.ReportStepRunStatusRequest{}
		if err := proto.Unmarshal(original["step/"+step.StepRunID].Payload, originalStep); err != nil {
			t.Fatal(err)
		}
		if int32(originalStep.Status) != localStatus {
			t.Fatalf("delivery failure rewrote original step result: %v", originalStep)
		}
		if len(observer.snapshot(job.JobRunID)) != 0 {
			t.Fatal("parent terminal overtook its pending step")
		}
	} else if step.Status != int(localStatus) || step.TerminalReportDigest == "" {
		t.Fatal("job report was sent before the step terminal committed")
	}

	controllerStatus, pipelineStatus := model.JobRunStatusCancelled, model.PipelineStatusCancelled
	switch authority {
	case "cancelled":
		if err := f.engine.CancelRun(run.RunID); err != nil {
			t.Fatal(err)
		}
	case "timeout":
		controllerStatus, pipelineStatus = model.JobRunStatusTimeout, model.PipelineStatusFailed
		stamp := time.Now()
		if kind == "step" {
			for _, candidate := range allSteps {
				if err := f.repos.StepRun.(*repo.StepRunRepo).PatchStatusIfCurrent(f.ctx, candidate.StepRunID, candidate.Status, map[string]any{"status": controllerStatus, "end_time": stamp, "error_message": "controller timeout won before delayed receipt"}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := f.repos.JobRun.(*repo.JobRunRepo).PatchStatusIfCurrent(f.ctx, job.JobRunID, 3, map[string]any{"status": controllerStatus, "end_time": stamp, "error_message": "controller timeout won before delayed receipt"}); err != nil {
			t.Fatal(err)
		}
	case "offline_recovered":
		controllerStatus, pipelineStatus = model.JobRunStatusFailed, model.PipelineStatusFailed
		// 所有真实 Shell 已退出；仅在隔离数据库注入操作员离线恢复结果，模拟迟到的旧投递
		summary, err := repo.RecoverInterruptedRuns(f.ctx, f.db, []string{run.RunID}, true)
		wantRecoveredSteps := int64(0)
		if kind == "step" {
			wantRecoveredSteps = wantSteps
		}
		if err != nil || summary.Runs != 1 || summary.Jobs != 1 || summary.Steps != wantRecoveredSteps {
			t.Fatalf("offline recovery=%+v error=%v", summary, err)
		}
	}
	f.waitRun(run.RunID, pipelineStatus)
	authoritativeJob, err := f.repos.JobRun.GetByJobRunID(f.ctx, job.JobRunID)
	if err != nil {
		t.Fatal(err)
	}
	authoritativeStep, err := f.repos.StepRun.GetByStepRunID(f.ctx, step.StepRunID)
	if err != nil {
		t.Fatal(err)
	}
	if authoritativeJob.Status != controllerStatus || authoritativeJob.TerminalReportDigest != "" || authoritativeJob.ClaimID != job.ClaimID {
		t.Fatalf("wrong authoritative job snapshot: %+v", authoritativeJob)
	}
	if kind == "step" && (authoritativeStep.Status != controllerStatus || authoritativeStep.TerminalReportDigest != "" || authoritativeStep.ClaimID != step.ClaimID) {
		t.Fatalf("wrong authoritative step snapshot: %+v", authoritativeStep)
	}
	var authoritativeSteps []*model.StepRun
	oldIDs := []string{job.JobRunID}
	for _, candidate := range allSteps {
		stored, err := f.repos.StepRun.GetByStepRunID(f.ctx, candidate.StepRunID)
		if err != nil {
			t.Fatal(err)
		}
		authoritativeSteps = append(authoritativeSteps, stored)
		oldIDs = append(oldIDs, stored.StepRunID)
		if kind == "step" && stored.Attempt > 1 && (stored.ClaimID != "" || stored.Status != controllerStatus || stored.TerminalReportDigest != "") {
			t.Fatalf("unused attempt acquired a fabricated execution claim: %+v", stored)
		}
	}
	gated.Store(false)

	f.wait("durable conflict quarantine", func() bool {
		records := controllerTerminalRecords(f)
		for key := range original {
			record, ok := records[key]
			if !ok || record.Quarantine == nil {
				return false
			}
		}
		return true
	})
	quarantined := controllerTerminalRecords(f)
	for key, before := range original {
		after := quarantined[key]
		if after.Checksum != before.Checksum || after.ClaimID != before.ClaimID || !bytes.Equal(after.Payload, before.Payload) || !reflect.DeepEqual(after.Dependencies, before.Dependencies) {
			t.Fatalf("quarantine rewrote original receipt %s", key)
		}
		q := after.Quarantine
		if q.ReceiptChecksum != before.Checksum || len(q.Checksum) != 64 || q.Version != 1 {
			t.Fatalf("quarantine metadata does not bind original receipt %s: %+v", key, q)
		}
		if _, err := time.Parse(time.RFC3339Nano, q.QuarantinedAt); err != nil {
			t.Fatalf("quarantine timestamp: %v", err)
		}
		if kind == "step" && before.Kind == "job" {
			child := quarantined["step/"+step.StepRunID]
			if q.Disposition != terminalDependencyDisposition || q.Conflict != nil || q.Dependency == nil || q.Dependency.RunID != step.StepRunID || q.Dependency.ReceiptChecksum != child.Checksum || q.Dependency.QuarantineChecksum != child.Quarantine.Checksum {
				t.Fatalf("parent quarantine does not retain explicit rejected-step ordering evidence: %+v", q)
			}
		} else if q.Disposition != terminalConflictDisposition || q.Conflict == nil || q.Dependency != nil || q.Conflict.RunID != before.RunID || q.Conflict.ClaimID != before.ClaimID || q.Conflict.AuthoritativeStatus != controllerStatus {
			t.Fatalf("direct quarantine lacks original controller rejection: %+v", q)
		}
		if q.Conflict != nil {
			expectedSource := "execution_claim"
			if before.Kind == "step" && before.RunID != step.StepRunID {
				expectedSource = "terminal_parent_claim"
				if authority == "cancelled" {
					expectedSource = "cancelled_parent_claim"
				}
				unused := &agentv1.ReportStepRunStatusRequest{}
				if err := proto.Unmarshal(before.Payload, unused); err != nil {
					t.Fatal(err)
				}
				if int32(unused.Status) != 8 || q.Conflict.Attempt != 2 || q.Conflict.PreviousStepRunID != step.StepRunID || before.ClaimID != agentidentity.StepClaimID(job.ClaimID, before.RunID) {
					t.Fatalf("unused attempt conflict lost its original derived parent scope: %+v", q.Conflict)
				}
			}
			if q.Conflict.ClaimSource != expectedSource {
				t.Fatalf("conflict claim source=%q want=%q", q.Conflict.ClaimSource, expectedSource)
			}
		}
		found, err := f.worker.reports.deliver(f.ctx, f.client, before.Kind, before.RunID)
		if !found || !terminalQuarantined(err) {
			t.Fatalf("quarantine falsely treated as successful controller ACK: found=%v err=%v", found, err)
		}
		if acknowledgeCompleted(err) != nil {
			t.Fatalf("durable quarantine did not unblock Kafka task handling: %v", err)
		}
	}
	conflictID := job.JobRunID
	if kind == "step" {
		conflictID = step.StepRunID
	}
	calls := observer.snapshot(conflictID)
	verifiedConflict := false
	for _, call := range calls {
		info, ok := agentidentity.ParseTerminalConflict(call.err, call.message, call.claim)
		if !ok {
			continue
		}
		verifiedConflict = true
		retained := quarantined[kind+"/"+conflictID]
		if retained.Quarantine == nil || retained.Quarantine.Conflict == nil || !reflect.DeepEqual(*retained.Quarantine.Conflict, info) {
			t.Fatal("durable marker does not retain the exact observed controller rejection")
		}
		if controllerTerminalAccepted(call.response) || status.Code(call.err) != codes.FailedPrecondition || info.Kind != kind || info.RunID != conflictID || info.AgentID != f.conf.Agent.ID || info.AuthoritativeStatus != controllerStatus || info.PipelineID != f.pipelineID || info.PipelineRunID != run.RunID || info.JobRunID != job.JobRunID || info.ReceiptDigest != "" {
			t.Fatalf("authenticated conflict identity/result mismatch: %+v", info)
		}
		if kind == "step" && (info.Attempt != step.Attempt || info.StepIndex != step.StepIndex || info.PreviousStepRunID != step.PreviousStepRunID) {
			t.Fatalf("conflict lost attempt scope: %+v", info)
		}
	}
	if !verifiedConflict {
		t.Fatal("quarantine lacks controller-authenticated structured conflict evidence")
	}
	if kind == "step" && len(observer.snapshot(job.JobRunID)) != 0 {
		t.Fatal("quarantined step was treated as ACK and parent terminal reached controller")
	}
	assertControllerConflictSnapshot(t, f, authoritativeJob, authoritativeSteps...)

	// 丢弃内存收据/认领并重开同一真实 Agent 的日志，保留原 Kafka 分区和 SQLite
	if err := f.worker.Stop(); err != nil {
		t.Fatal(err)
	}
	oldCtx := withTerminalOutbox(f.ctx, f.worker.reports)
	pendingJobReports.Delete(receiptKey(oldCtx, "job", job.JobRunID))
	completeExecutionClaim(oldCtx, "job", job.JobRunID)
	for _, candidate := range allSteps {
		pendingStepReports.Delete(receiptKey(oldCtx, "step", candidate.StepRunID))
		completeExecutionClaim(oldCtx, "step", candidate.StepRunID)
	}
	beforeRestartCalls := len(observer.snapshot(oldIDs...))
	manager := executor.NewExecutorManager()
	manager.Register(executor.NewShellExecutor())
	manager.SetLogPublisher(&capturedLogs{})
	queue, err := StartWorker(f.ctx, f.conf, f.client, manager)
	if err != nil {
		t.Fatal(err)
	}
	f.worker = queue.(*workerQueues)
	if !reflect.DeepEqual(quarantined, controllerTerminalRecords(f)) {
		t.Fatal("restart changed or discarded durable quarantine evidence")
	}
	f.mu.Lock()
	duplicate := f.tasks[job.JobRunID]
	f.mu.Unlock()
	if len(duplicate.Payload) == 0 {
		t.Fatal("original Process Kafka payload missing")
	}
	if _, err := f.producer.Enqueue(&duplicate, nova.Queue(taskpayload.AgentQueue(f.conf.Agent.ID))); err != nil {
		t.Fatal(err)
	}
	// 同一分区串行消费：健康新作业完成证明旧任务已按隔离结果处理，而非阻塞无限重试
	next := f.submit(&spec.Job{Name: "after-quarantine", Steps: []*spec.Step{controllerWorkerShell("healthy-once", "printf x >> executions")}})
	f.waitRun(next.RunID, model.PipelineStatusSuccess)
	nextJobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, next.RunID)
	if err != nil || len(nextJobs) != 1 || nextJobs[0].AgentID != job.AgentID {
		t.Fatalf("same Agent continuation failed: %+v %v", nextJobs, err)
	}
	if got := artifactTestRead(t, filepath.Join(f.conf.Agent.WorkspaceDir, next.RunID, nextJobs[0].JobRunID), "executions"); got != "x" {
		t.Fatalf("subsequent command executions=%q", got)
	}
	if got := artifactTestRead(t, workspace, "executions"); got != "x" {
		t.Fatalf("duplicate Kafka task reran quarantined command: %q", got)
	}
	if len(observer.snapshot(oldIDs...)) != beforeRestartCalls {
		t.Fatal("restart/redelivery retried quarantined terminal RPC")
	}
	assertControllerConflictSnapshot(t, f, authoritativeJob, authoritativeSteps...)
	f.wait("successful later receipts drain while old quarantine persists", func() bool { return reflect.DeepEqual(quarantined, controllerTerminalRecords(f)) })
	assertControllerQueueCommitted(t, f, 3)
}

func assertControllerConflictSnapshot(t *testing.T, f *controllerWorkerFixture, job *model.JobRun, steps ...*model.StepRun) {
	t.Helper()
	actualJob, err := f.repos.JobRun.GetByJobRunID(f.ctx, job.JobRunID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(job, actualJob) {
		t.Fatalf("late report changed immutable controller job: %+v", actualJob)
	}
	for _, step := range steps {
		actualStep, err := f.repos.StepRun.GetByStepRunID(f.ctx, step.StepRunID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(step, actualStep) {
			t.Fatalf("late report changed immutable controller step: %+v", actualStep)
		}
	}
}

func assertControllerQueueCommitted(t *testing.T, f *controllerWorkerFixture, minimum int64) {
	t.Helper()
	consumer, err := ckafka.NewConsumer(&ckafka.ConfigMap{"bootstrap.servers": f.conf.MessageQueue.Kafka.BootstrapServers, "group.id": "arcentra-agent-" + f.conf.Agent.ID, "enable.auto.commit": false})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	topic := strings.ToUpper(taskpayload.AgentQueue(f.conf.Agent.ID))
	f.wait("Kafka commits every handled task offset", func() bool {
		partitions, err := consumer.Committed([]ckafka.TopicPartition{{Topic: &topic, Partition: 0}}, 1000)
		return err == nil && len(partitions) == 1 && partitions[0].Error == nil && int64(partitions[0].Offset) >= minimum
	})
}

// TestControllerWorkerConflictStopsRemainingEffects 隔离是执行边界，不能被 continueOnError 或重试策略忽略
func TestControllerWorkerConflictStopsRemainingEffects(t *testing.T) {
	for _, mode := range []string{"failed_step", "whole_job"} {
		for _, continueOnError := range []bool{false, true} {
			name := mode + "/stop_on_error"
			if continueOnError {
				name = mode + "/continue_on_error"
			}
			t.Run(name, func(t *testing.T) {
				f := newControllerWorkerFixture(t, false, controllerWorkerTLS(t))
				var injected atomic.Bool
				observer := &controllerConflictObserver{}
				f.mu.Lock()
				f.observeTerminal = observer.observe
				f.beforeTerminal = func(ctx context.Context, message proto.Message) error {
					req, ok := message.(*agentv1.ReportStepRunStatusRequest)
					if !ok || int32(req.Status) != 5 || !injected.CompareAndSwap(false, true) {
						return nil
					}
					return f.repos.StepRun.(*repo.StepRunRepo).PatchStatusIfCurrent(ctx, req.StepRunId, 3, map[string]any{"status": 7, "end_time": time.Now(), "error_message": "controller closed first attempt before terminal receipt"})
				}
				f.mu.Unlock()
				first := controllerWorkerShell("first", "printf x >> executions; exit 9")
				first.ContinueOnError = continueOnError
				run := f.submit(&spec.Job{Name: "fenced-effects", Retry: &spec.Retry{Mode: mode, MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}, Steps: []*spec.Step{
					first,
					controllerWorkerShell("never", "printf unexpected > later"),
					{Name: "never-upload", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"paths": []any{"executions"}})},
				}})
				var job *model.JobRun
				var firstAttempt model.StepRun
				f.wait("first rejected step and parent dependency quarantine", func() bool {
					jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
					if err != nil {
						t.Fatal(err)
					}
					if len(jobs) != 1 {
						return false
					}
					job = jobs[0]
					record := controllerTerminalRecords(f)["job/"+job.JobRunID]
					return record.Quarantine != nil && record.Quarantine.Disposition == terminalDependencyDisposition
				})
				steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID})
				if err != nil || count != 6 {
					t.Fatalf("retry attempts not preallocated by controller: %d %v", count, err)
				}
				for _, step := range steps {
					if step.Name == "first" && step.Attempt == 1 {
						firstAttempt = step
						if step.Status != 7 || step.ClaimID == "" || step.TerminalReportDigest != "" {
							t.Fatalf("first rejection was overwritten: %+v", step)
						}
					} else if step.Status != 8 || step.StartTime == nil || step.TerminalReportDigest == "" {
						t.Fatalf("unexecuted attempts were not explicitly skipped: %+v", step)
					}
				}
				workspace := filepath.Join(f.conf.Agent.WorkspaceDir, run.RunID, job.JobRunID)
				if got := artifactTestRead(t, workspace, "executions"); got != "x" {
					t.Fatalf("quarantined attempt was retried: %q", got)
				}
				if _, err := os.Stat(filepath.Join(workspace, "later")); !os.IsNotExist(err) {
					t.Fatalf("continueOnError ran a later command: %v", err)
				}
				f.store.mu.Lock()
				uploads := f.store.uploads
				f.store.mu.Unlock()
				if uploads != 0 {
					t.Fatalf("quarantined execution uploaded artifacts: %d", uploads)
				}
				_, artifactCount, err := f.repos.Artifact.List(f.ctx, repo.ArtifactQuery{ProjectID: f.projectID, Limit: 20})
				if err != nil || artifactCount != 0 {
					t.Fatalf("quarantined parent registered artifacts: %d %v", artifactCount, err)
				}
				if len(observer.snapshot(job.JobRunID)) != 0 || job.Status != 3 || job.TerminalReportDigest != "" {
					t.Fatal("rejected child was treated as successful parent ACK")
				}
				if firstAttempt.StepRunID == "" {
					t.Fatal("missing first controller attempt")
				}
				// 控制面负责最终作业处置；本地隔离不能偷偷替它提交成功或失败
				if err := f.repos.JobRun.(*repo.JobRunRepo).PatchStatusIfCurrent(f.ctx, job.JobRunID, 3, map[string]any{"status": model.JobRunStatusTimeout, "end_time": time.Now()}); err != nil {
					t.Fatal(err)
				}
				f.waitRun(run.RunID, model.PipelineStatusFailed)
				next := f.submit(&spec.Job{Name: "after-effects-fence", Steps: []*spec.Step{controllerWorkerShell("healthy", "printf x >> executions")}})
				f.waitRun(next.RunID, model.PipelineStatusSuccess)
				assertControllerQueueCommitted(t, f, 2)
			})
		}
	}
}
