//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/nova"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// containerAcceptanceControllerTimeout 区分控制面先截止的拒收审计与 Agent 超时正常确认
// 拒收不写 ACK；精确资源清理后保留运行时墓碑，重启由认证查询独立确认权威终态
func containerAcceptanceControllerTimeout(t *testing.T, f *controllerWorkerFixture, cfg *realContainerSettings, publisher executor.LogPublisher) {
	t.Helper()
	var delayed atomic.Bool
	delayed.Store(true)
	t.Cleanup(func() { delayed.Store(false) })
	observer := &controllerConflictObserver{}
	f.mu.Lock()
	f.beforeTerminal = func(_ context.Context, _ proto.Message) error {
		if delayed.Load() {
			return status.Error(codes.Unavailable, "acceptance delays terminal delivery until controller deadline")
		}
		return nil
	}
	f.observeTerminal = observer.observe
	f.mu.Unlock()
	run := containerAcceptanceSubmit(t, f, cfg, &spec.Job{Name: "controller-first-timeout", Timeout: "4s", Steps: []*spec.Step{
		controllerWorkerShell("wait", "printf x >> executions; printf started > started; sleep 60; printf forbidden > after-timeout"),
		controllerWorkerShell("never", "printf forbidden > later"),
	}})
	var runtimeRecord containerAcceptanceJournal
	f.wait("exact controller-timeout container begins execution", func() bool {
		records, err := containerAcceptanceRecords(f.conf.Agent.Sandbox.Containerd.JournalDir)
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.Record.RunID == run.RunID && record.Record.Phase == "executing" {
				if _, err := os.Stat(filepath.Join(f.conf.Agent.WorkspaceDir, record.Record.Directory, "started")); err == nil {
					runtimeRecord = record
					return true
				}
			}
		}
		return false
	})
	f.waitRun(run.RunID, model.PipelineStatusFailed)
	job, err := f.repos.JobRun.GetByJobRunID(f.ctx, runtimeRecord.Record.JobRunID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != model.JobRunStatusTimeout || job.TerminalReportDigest != "" || job.ClaimID != runtimeRecord.Record.Claim || job.RuntimeBindingDigest != runtimeRecord.Record.Binding || job.AgentID != f.conf.Agent.ID {
		t.Fatalf("controller-first deadline lost authoritative original identity: %+v", job)
	}
	steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID})
	if err != nil || count != 2 {
		t.Fatalf("controller timeout attempts=%d: %v", count, err)
	}
	var snapshots []*model.StepRun
	stepsByID := make(map[string]model.StepRun, len(steps))
	for _, step := range steps {
		stepsByID[step.StepRunID] = step
		copy := step
		snapshots = append(snapshots, &copy)
		if step.Status != model.JobRunStatusTimeout || step.TerminalReportDigest != "" {
			t.Fatalf("controller timeout did not precede late step report: %+v", step)
		}
		if step.Name == "never" && step.ClaimID != "" {
			t.Fatal("controller timeout invented unstarted execution ownership")
		}
		if step.Name == "wait" && step.ClaimID != agentidentity.StepClaimID(job.ClaimID, step.StepRunID) {
			t.Fatal("controller timeout changed running step claim")
		}
	}
	var original map[string]terminalRecord
	f.wait("original Agent outcome and exact cleanup complete before rejection", func() bool {
		if _, running := runningJobRunCancel.Load(job.JobRunID); running {
			return false
		}
		original = controllerTerminalRecords(f)
		if len(original) != 3 {
			return false
		}
		for _, record := range original {
			if record.Quarantine != nil {
				return false
			}
		}
		recoveries := f.worker.offline.controller.Recoveries()
		return len(recoveries) == 1 && recoveries[0].JobRunID == job.JobRunID && recoveries[0].ExecutionClaimID == job.ClaimID && recoveries[0].RuntimeBindingDigest == job.RuntimeBindingDigest && recoveries[0].CleanupComplete
	})
	containerAcceptanceRemoved(t, f, runtimeRecord)
	tombstones := containerAcceptanceJournalBytes(t, f)
	if len(tombstones) != 1 {
		t.Fatalf("expected exactly one cleanup-complete timeout tombstone: %d", len(tombstones))
	}
	parent, ok := original["job/"+job.JobRunID]
	if !ok || parent.ClaimID != job.ClaimID || len(parent.Dependencies) != 2 {
		t.Fatal("original timed-out parent receipt lost claim or dependencies")
	}
	var parentRequest agentv1.ReportJobRunStatusRequest
	if err := proto.Unmarshal(parent.Payload, &parentRequest); err != nil || parentRequest.RuntimeBindingDigest != job.RuntimeBindingDigest || (parentRequest.Status != model.JobRunStatusCancelled && parentRequest.Status != model.JobRunStatusTimeout) {
		t.Fatalf("original late job receipt lost runtime binding: %v", err)
	}
	delayed.Store(false)
	f.wait("controller-authenticated permanent timeout rejection is durable", func() bool {
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
		if before.ClaimID != after.ClaimID || before.Checksum != after.Checksum || !bytes.Equal(before.Payload, after.Payload) || !reflect.DeepEqual(before.Dependencies, after.Dependencies) {
			t.Fatalf("quarantine rewrote original outcome: %s", key)
		}
		q := after.Quarantine
		if q.ReceiptChecksum != before.Checksum || len(q.Checksum) != 64 {
			t.Fatalf("quarantine did not bind immutable receipt: %s", key)
		}
		if before.Kind == "job" {
			if q.Disposition != terminalDependencyDisposition || q.Conflict != nil || q.Dependency == nil {
				t.Fatal("parent rejection lacks its rejected-step dependency")
			}
			child, ok := quarantined["step/"+q.Dependency.RunID]
			if !ok || child.Quarantine == nil || q.Dependency.ReceiptChecksum != child.Checksum || q.Dependency.QuarantineChecksum != child.Quarantine.Checksum {
				t.Fatal("parent rejection changed exact dependency evidence")
			}
		} else {
			rejection := q.Conflict
			if q.Disposition != terminalConflictDisposition || rejection == nil || rejection.AgentID != f.conf.Agent.ID || rejection.RunID != before.RunID || rejection.ClaimID != before.ClaimID || rejection.PipelineID != f.pipelineID || rejection.PipelineRunID != run.RunID || rejection.JobRunID != job.JobRunID || rejection.AuthoritativeStatus != model.JobRunStatusTimeout || rejection.ReceiptDigest != "" {
				t.Fatalf("late step rejection lost original authenticated scope: %+v", rejection)
			}
			if before.ClaimID != agentidentity.StepClaimID(job.ClaimID, before.RunID) {
				t.Fatal("late step receipt changed original derived parent claim")
			}
			step, exists := stepsByID[before.RunID]
			claimSource := "execution_claim"
			if step.ClaimID == "" {
				claimSource = "terminal_parent_claim"
			}
			if !exists || rejection.ClaimSource != claimSource || rejection.Attempt != step.Attempt || rejection.StepIndex != step.StepIndex || rejection.PreviousStepRunID != step.PreviousStepRunID {
				t.Fatalf("late step rejection changed original attempt or claim provenance: %+v", rejection)
			}
			matched := false
			for _, call := range observer.snapshot(before.RunID) {
				info, valid := agentidentity.ParseTerminalConflict(call.err, call.message, call.claim)
				if valid && reflect.DeepEqual(info, *rejection) {
					if status.Code(call.err) != codes.FailedPrecondition || controllerTerminalAccepted(call.response) {
						t.Fatal("quarantine was misreported as accepted")
					}
					matched = true
				}
			}
			if !matched {
				t.Fatal("durable timeout rejection differs from observed TLS gRPC conflict")
			}
		}
		found, err := f.worker.reports.deliver(f.ctx, f.client, before.Kind, before.RunID)
		if !found || !terminalQuarantined(err) {
			t.Fatalf("permanent rejection falsely acknowledged: %s %v", key, err)
		}
	}
	assertControllerConflictSnapshot(t, f, job, snapshots...)
	if !reflect.DeepEqual(tombstones, containerAcceptanceJournalBytes(t, f)) {
		t.Fatal("quarantine silently acknowledged or rewrote runtime tombstone")
	}
	ids := []string{job.JobRunID}
	for _, step := range steps {
		ids = append(ids, step.StepRunID)
	}
	callsBefore := len(observer.snapshot(ids...))
	f.mu.Lock()
	duplicate := f.tasks[job.JobRunID]
	f.mu.Unlock()
	if _, err := f.producer.Enqueue(&duplicate, nova.Queue(taskpayload.AgentQueue(f.conf.Agent.ID))); err != nil {
		t.Fatal(err)
	}
	next := containerAcceptanceSubmit(t, f, cfg, &spec.Job{Name: "after-controller-timeout", Steps: []*spec.Step{controllerWorkerShell("healthy", "printf 'after-controller-timeout\\n'")}})
	f.waitRun(next.RunID, model.PipelineStatusSuccess)
	healthy := containerAcceptanceJob(t, f, next.RunID, model.JobRunStatusSuccess)
	f.wait("subsequent job acknowledges while timeout audit remains", func() bool {
		return !f.worker.offline.HasJob(healthy.JobRunID) && len(controllerTerminalRecords(f)) == 3
	})
	assertControllerQueueCommitted(t, f, 9)
	if len(observer.snapshot(ids...)) != callsBefore || !reflect.DeepEqual(quarantined, controllerTerminalRecords(f)) || !reflect.DeepEqual(tombstones, containerAcceptanceJournalBytes(t, f)) {
		t.Fatal("duplicate delivery repeated timed-out work, report, or runtime acknowledgment")
	}
	assertControllerConflictSnapshot(t, f, job, snapshots...)
	containerAcceptanceRemoved(t, f, runtimeRecord)

	// Same disk and identities: preserve quarantine, query the controller's exact
	// original runtime outcome, and acknowledge only the independent cleanup tombstone.
	inspections := make(chan *agentv1.InspectJobExecutionRequest, 2)
	f.mu.Lock()
	f.beforeTerminal = nil
	f.observeRequest = func(_ context.Context, request any) {
		if inspect, ok := request.(*agentv1.InspectJobExecutionRequest); ok {
			select {
			case inspections <- proto.Clone(inspect).(*agentv1.InspectJobExecutionRequest):
			default:
			}
		}
	}
	f.mu.Unlock()
	if err := f.worker.Stop(); err != nil {
		t.Fatal(err)
	}
	oldCtx := withTerminalOutbox(f.ctx, f.worker.reports)
	pendingJobReports.Delete(receiptKey(oldCtx, "job", job.JobRunID))
	completeExecutionClaim(oldCtx, "job", job.JobRunID)
	for _, step := range steps {
		pendingStepReports.Delete(receiptKey(oldCtx, "step", step.StepRunID))
		completeExecutionClaim(oldCtx, "step", step.StepRunID)
	}
	manager := executor.NewExecutorManager()
	manager.Register(executor.NewShellExecutor())
	manager.SetLogPublisher(publisher)
	queue, err := startContainerAcceptanceWorker(f.ctx, f.conf, f.client, manager)
	if err != nil {
		t.Fatalf("timeout quarantine restart: %v", err)
	}
	f.worker = queue.(*workerQueues)
	select {
	case inspect := <-inspections:
		if inspect.AgentId != f.conf.Agent.ID || inspect.JobRunId != job.JobRunID || inspect.OriginalClaimId != job.ClaimID || inspect.RuntimeBindingDigest != job.RuntimeBindingDigest {
			t.Fatalf("restart changed authoritative timeout query: %+v", inspect)
		}
	default:
		t.Fatal("restart cleared runtime tombstone without authenticated inspection")
	}
	if !f.worker.OfflineRuntimeReady() || f.worker.offline.HasJob(job.JobRunID) || len(containerAcceptanceJournalBytes(t, f)) != 0 || !reflect.DeepEqual(quarantined, controllerTerminalRecords(f)) {
		t.Fatal("restart lost quarantine evidence or failed exact runtime reconciliation")
	}
	afterRestart := containerAcceptanceSubmit(t, f, cfg, &spec.Job{Name: "after-timeout-restart", Steps: []*spec.Step{controllerWorkerShell("healthy", "printf 'after-timeout-restart\\n'")}})
	f.waitRun(afterRestart.RunID, model.PipelineStatusSuccess)
	restartedJob := containerAcceptanceJob(t, f, afterRestart.RunID, model.JobRunStatusSuccess)
	f.wait("post-restart healthy job completes without discarding audit", func() bool {
		return !f.worker.offline.HasJob(restartedJob.JobRunID) && len(controllerTerminalRecords(f)) == 3
	})
	assertControllerQueueCommitted(t, f, 10)
	if !reflect.DeepEqual(quarantined, controllerTerminalRecords(f)) {
		t.Fatal("healthy post-restart work discarded timeout quarantine")
	}
	assertControllerConflictSnapshot(t, f, job, snapshots...)
	t.Log("controller-first timeout verified: original late receipts rejected with exact authenticated conflict evidence; authority unchanged, resources removed, tombstone retained until same-identity restart inspection, quarantine audit survives and later jobs complete")
}

func containerAcceptanceJournalBytes(t *testing.T, f *controllerWorkerFixture) map[string][]byte {
	t.Helper()
	root := f.conf.Agent.Sandbox.Containerd.JournalDir
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	records := make(map[string][]byte)
	for _, entry := range entries {
		if entry.Name() == "owner.json" || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		records[entry.Name()] = data
	}
	return records
}
