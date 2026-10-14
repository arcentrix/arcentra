// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/nova"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type controllerReceiptExpectation struct {
	jobID, runID, claim, runtimeBinding string
	status                              int
	steps                               map[string]int
}

// controllerWorkerReceiptsReady 只有原执行终态和全部步骤回执已持久确认才允许验收继续
// 控制面取消意图、空发件箱或 Recoveries 中没有活跃会话都不等于 Agent 已完成
func controllerWorkerReceiptsReady(t *testing.T, f *controllerWorkerFixture, expected controllerReceiptExpectation) (*model.JobRun, bool) {
	t.Helper()
	job, err := f.repos.JobRun.GetByJobRunID(f.ctx, expected.jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.PipelineRunID != expected.runID || job.AgentID != f.conf.Agent.ID || job.ClaimID != expected.claim || job.RuntimeBindingDigest != expected.runtimeBinding {
		t.Fatalf("awaiting receipts changed immutable execution identity: %+v", job)
	}
	if job.Status != expected.status || job.EndTime == nil || len(job.TerminalReportDigest) != 64 {
		return job, false
	}
	steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: expected.runID})
	if err != nil {
		t.Fatal(err)
	}
	if count != int64(len(expected.steps)) {
		t.Fatalf("unexpected receipt attempt count: %d", count)
	}
	for _, step := range steps {
		want, ok := expected.steps[step.Name]
		if !ok || step.JobRunID != expected.jobID || step.AgentID != f.conf.Agent.ID {
			t.Fatalf("awaiting receipts changed step ownership: %+v", step)
		}
		if step.Status != want || step.EndTime == nil || len(step.TerminalReportDigest) != 64 {
			return job, false
		}
	}
	if _, running := runningJobRunCancel.Load(expected.jobID); running {
		return job, false
	}
	f.worker.reports.mu.Lock()
	empty := len(f.worker.reports.entries) == 0
	f.worker.reports.mu.Unlock()
	if !empty {
		return job, false
	}
	if f.worker.offline != nil && (f.worker.offline.HasJob(expected.jobID) || !f.worker.OfflineRuntimeReady()) {
		return job, false
	}
	return job, true
}

func controllerWorkerWaitReceipts(t *testing.T, f *controllerWorkerFixture, expected controllerReceiptExpectation) *model.JobRun {
	t.Helper()
	var job *model.JobRun
	f.wait("matching persisted Agent job/step receipts, execution exit and exact runtime completion", func() bool { var ready bool; job, ready = controllerWorkerReceiptsReady(t, f, expected); return ready })
	return job
}

// TestControllerWorkerCancellationWaitsForAgentReceipt 再现控制面取消已终态而 Agent 回执尚未到达的窗口
// 真实 Process、Shell、TLS RPC 和 SQLite 保持原路径，只有 Kafka broker 使用本地 MockCluster
func TestControllerWorkerCancellationWaitsForAgentReceipt(t *testing.T) {
	f := newControllerWorkerFixture(t, false, controllerWorkerTLS(t))
	var delayed atomic.Bool
	delayed.Store(true)
	t.Cleanup(func() { delayed.Store(false) })
	f.mu.Lock()
	f.beforeTerminal = func(_ context.Context, message proto.Message) error {
		if _, ok := message.(*agentv1.ReportJobRunStatusRequest); ok && delayed.Load() {
			return status.Error(codes.Unavailable, "synthetic delayed cancellation receipt")
		}
		return nil
	}
	f.mu.Unlock()
	run := f.submit(&spec.Job{Name: "cancel-receipt-window", Steps: []*spec.Step{
		controllerWorkerShell("wait", "printf x >> executions; printf started > started; sleep 30 & wait; printf forbidden > after-cancel"),
		controllerWorkerShell("never", "printf forbidden > later"),
	}})
	var original *model.JobRun
	f.wait("original shell claim and command start", func() bool {
		jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) != 1 {
			return false
		}
		job := jobs[0]
		_, err = os.Stat(filepath.Join(f.conf.Agent.WorkspaceDir, run.RunID, job.JobRunID, "started"))
		if err != nil || job.Status != 3 || job.ClaimID == "" {
			return false
		}
		original = job
		return true
	})
	expected := controllerReceiptExpectation{jobID: original.JobRunID, runID: run.RunID, claim: original.ClaimID, runtimeBinding: original.RuntimeBindingDigest, status: model.JobRunStatusCancelled, steps: map[string]int{"wait": 6, "never": 6}}
	if err := f.engine.CancelRun(run.RunID); err != nil {
		t.Fatal(err)
	}
	f.waitRun(run.RunID, model.PipelineStatusCancelled)
	intent, ready := controllerWorkerReceiptsReady(t, f, expected)
	if ready || intent.Status != 6 || intent.TerminalReportDigest != "" || intent.CancellationClaimID != original.ClaimID {
		t.Fatalf("controller terminal intent falsely passed receipt completion: %+v ready=%v", intent, ready)
	}
	f.wait("Agent stopped but matching original job receipt remains durably pending", func() bool {
		if _, running := runningJobRunCancel.Load(original.JobRunID); running {
			return false
		}
		records := controllerTerminalRecords(f)
		record, ok := records["job/"+original.JobRunID]
		return ok && record.ClaimID == original.ClaimID && record.Quarantine == nil
	})
	if _, ready := controllerWorkerReceiptsReady(t, f, expected); ready {
		t.Fatal("unacknowledged original receipt falsely passed completion")
	}
	delayed.Store(false)
	completed := controllerWorkerWaitReceipts(t, f, expected)
	if completed.CancellationClaimID != original.ClaimID {
		t.Fatal("cancellation receipt changed original claim")
	}
	f.assertReceipt("job", completed.JobRunID, original.ClaimID, completed.TerminalReportDigest, run.RunID, completed.JobRunID)
	steps, _, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		claim := step.ClaimID
		if step.Name == "never" {
			if claim != "" || step.CancellationClaimID != original.ClaimID {
				t.Fatal("unstarted cancellation invented execution ownership")
			}
			claim = agentidentity.StepClaimID(original.ClaimID, step.StepRunID)
		} else if claim != agentidentity.StepClaimID(original.ClaimID, step.StepRunID) {
			t.Fatal("running cancellation changed the original derived execution claim")
		}
		f.assertReceipt("step", step.StepRunID, claim, step.TerminalReportDigest, run.RunID, completed.JobRunID)
	}
	workspace := filepath.Join(f.conf.Agent.WorkspaceDir, run.RunID, completed.JobRunID)
	if value := artifactTestRead(t, workspace, "executions"); value != "x" {
		t.Fatalf("original command count=%q", value)
	}
	for _, name := range []string{"later", "after-cancel"} {
		if _, err := os.Stat(filepath.Join(workspace, name)); !os.IsNotExist(err) {
			t.Fatalf("canceled command continued: %s %v", name, err)
		}
	}
	f.mu.Lock()
	duplicate := f.tasks[completed.JobRunID]
	f.mu.Unlock()
	if _, err := f.producer.Enqueue(&duplicate, nova.Queue(taskpayload.AgentQueue(f.conf.Agent.ID))); err != nil {
		t.Fatal(err)
	}
	next := f.submit(&spec.Job{Name: "after-cancel-receipt", Steps: []*spec.Step{controllerWorkerShell("healthy", "printf healthy")}})
	f.waitRun(next.RunID, model.PipelineStatusSuccess)
	assertControllerQueueCommitted(t, f, 3)
	after, err := f.repos.JobRun.GetByJobRunID(f.ctx, completed.JobRunID)
	if err != nil || !reflect.DeepEqual(completed, after) {
		t.Fatalf("duplicate changed accepted canceled result: %+v %v", after, err)
	}
	if value := artifactTestRead(t, workspace, "executions"); value != "x" {
		t.Fatalf("duplicate repeated canceled command: %q", value)
	}
}

// TestControllerWorkerAgentDeadlineReceiptPrecedesController 验证步骤先超时可正常确认原认领回执
func TestControllerWorkerAgentDeadlineReceiptPrecedesController(t *testing.T) {
	f := newControllerWorkerFixture(t, false, controllerWorkerTLS(t))
	step := controllerWorkerShell("wait", "printf started > started; sleep 30 & wait")
	step.Timeout = "1s"
	run := f.submit(&spec.Job{Name: "agent-deadline", Timeout: "20s", Steps: []*spec.Step{step, controllerWorkerShell("never", "printf forbidden > later")}})
	var original *model.JobRun
	f.wait("Agent command before its own deadline", func() bool {
		jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) != 1 {
			return false
		}
		job := jobs[0]
		_, err = os.Stat(filepath.Join(f.conf.Agent.WorkspaceDir, run.RunID, job.JobRunID, "started"))
		if err != nil || job.ClaimID == "" {
			return false
		}
		original = job
		return true
	})
	f.waitRun(run.RunID, model.PipelineStatusFailed)
	completed := controllerWorkerWaitReceipts(t, f, controllerReceiptExpectation{jobID: original.JobRunID, runID: run.RunID, claim: original.ClaimID, runtimeBinding: original.RuntimeBindingDigest, status: model.JobRunStatusTimeout, steps: map[string]int{"wait": 7, "never": 8}})
	f.assertReceipt("job", completed.JobRunID, original.ClaimID, completed.TerminalReportDigest, run.RunID, completed.JobRunID)
	if _, err := os.Stat(filepath.Join(f.conf.Agent.WorkspaceDir, run.RunID, completed.JobRunID, "later")); !os.IsNotExist(err) {
		t.Fatalf("later command ran after step timeout: %v", err)
	}
}
