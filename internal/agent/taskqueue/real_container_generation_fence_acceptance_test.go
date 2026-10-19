//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// testRealContainerGenerationCancellationFence 在已存在的真实 MySQL/TLS fixture 中验证事务顺序
// 这些明确标记的合成协议行不经队列派发，不创建任何容器、工作区或运行时会话
// 合成清理回执只用于控制面协议测试，不冒充真实运行时的清理验收证据
func testRealContainerGenerationCancellationFence(t *testing.T, f *controllerWorkerFixture) {
	t.Helper()
	for _, scenario := range []struct {
		name    string
		advance bool
		started bool
	}{{"cancel-before-advance", false, false}, {"advance-before-cancel-unused", true, false}, {"advance-before-cancel-started", true, true}} {
		t.Run("synthetic-protocol-"+scenario.name, func(t *testing.T) {
			jobs := f.repos.JobRun.(*repo.JobRunRepo)
			runID, jobID, claim := uuid.NewString(), uuid.NewString(), uuid.NewString()
			ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
			agentID := f.conf.Agent.ID
			run := &model.PipelineRun{RunID: runID, PipelineID: f.pipelineID, PipelineName: "synthetic-generation-cancellation-protocol", Status: model.PipelineStatusPending}
			if err := f.repos.Pipeline.CreateRun(f.ctx, run); err != nil {
				t.Fatal(err)
			}
			// Admit through the production boundary before creating protocol jobs;
			// CreateRun deliberately cannot manufacture the protected project pin.
			admitted, err := f.repos.Pipeline.(*repo.PipelineRepo).StartManualRunIfPending(f.ctx, run)
			if err != nil || !admitted {
				t.Fatalf("synthetic protocol run admission: changed=%t err=%v", admitted, err)
			}
			var stored model.PipelineRun
			if err := f.db.WithContext(f.ctx).Where("run_id = ?", runID).First(&stored).Error; err != nil || stored.ProjectID != f.projectID || stored.Status != model.PipelineStatusRunning || stored.StartTime == nil {
				t.Fatalf("synthetic protocol run did not acquire its exact project scope: %v", err)
			}
			if err := jobs.Create(f.ctx, &model.JobRun{JobRunID: jobID, PipelineID: f.pipelineID, PipelineRunID: runID, JobName: "synthetic-generation-cancellation-protocol", Status: model.JobRunStatusPending}); err != nil {
				t.Fatal(err)
			}
			policy := &pipelinev1.Retry{Mode: "whole_job", MaxAttempts: 3, Idempotent: true, AllowedExitCodes: []int32{9}}
			policyJSON, err := sonic.MarshalString(policy)
			if err != nil {
				t.Fatal(err)
			}
			for i, id := range ids {
				previous := ""
				if i > 0 {
					previous = ids[i-1]
				}
				if err := f.repos.StepRun.Create(f.ctx, &model.StepRun{StepRunID: id, Name: "synthetic-shell", JobRunID: jobID, PipelineID: f.pipelineID, PipelineRunID: runID, Attempt: i + 1, RetryMode: "whole_job", RetryPolicy: policyJSON, PreviousStepRunID: previous, Uses: "shell", Status: model.JobRunStatusPending}); err != nil {
					t.Fatal(err)
				}
			}
			runtime := &taskpayload.RuntimeRequest{Type: taskpayload.RuntimeContainerdOfflineV1, Image: "synthetic-protocol/image@sha256:" + strings.Repeat("a", 64), Resources: taskpayload.RuntimeResources{CPU: "500m", Memory: "64Mi"}}
			runtime.BindingDigest, err = taskpayload.RuntimeBindingDigest(f.pipelineID, runID, jobID, agentID, runtime)
			if err != nil {
				t.Fatal(err)
			}
			payload := &taskpayload.JobRunTaskPayload{PipelineID: f.pipelineID, PipelineRunID: runID, JobRunID: jobID, AgentID: agentID, Runtime: runtime, Retry: policy, Steps: []taskpayload.StepPayload{{Name: "synthetic-shell", StepRunID: ids[0], AttemptRunIDs: ids, Uses: "shell", Args: map[string]any{"run": "exit 9"}}}}
			plan, err := taskpayload.NewJobRetryPlan(payload)
			if err != nil {
				t.Fatal(err)
			}
			if err := jobs.CaptureRuntimeRetryExpectation(f.ctx, f.pipelineID, runID, jobID, agentID, nil, runtime, nil, nil, plan); err != nil {
				t.Fatal(err)
			}
			// 使用真实已认证客户端；绝不从测试上下文跳过服务身份校验
			rpcContext := func(id string) (context.Context, context.CancelFunc) {
				ctx, cancel := f.client.WithTimeoutAndAuth(f.ctx)
				return metadata.AppendToOutgoingContext(ctx, executionClaimMetadataKey, id), cancel
			}
			reportStep := func(request *agentv1.ReportStepRunStatusRequest) error {
				ctx, cancel := rpcContext(agentidentity.StepClaimID(claim, request.StepRunId))
				defer cancel()
				response, err := f.client.AgentClient.ReportStepRunStatus(ctx, request)
				if err == nil && (response == nil || !response.Success) {
					t.Fatalf("step report returned no acknowledgement: %+v", response)
				}
				return err
			}
			reportJob := func(request *agentv1.ReportJobRunStatusRequest) error {
				ctx, cancel := rpcContext(claim)
				defer cancel()
				response, err := f.client.AgentClient.ReportJobRunStatus(ctx, request)
				if err == nil && (response == nil || !response.Success) {
					t.Fatalf("job report returned no acknowledgement: %+v", response)
				}
				return err
			}
			reconcile := func(request taskpayload.JobGenerationTransition) (*taskpayload.JobGeneration, error) {
				body, err := request.Canonical()
				if err != nil {
					return nil, err
				}
				ctx, cancel := rpcContext(claim)
				defer cancel()
				response, err := f.client.AgentClient.ReconcileJobGeneration(ctx, &agentv1.ReconcileJobGenerationRequest{AgentId: agentID, TransitionJson: body})
				if err != nil {
					return nil, err
				}
				return taskpayload.ParseJobGeneration(response.GenerationJson)
			}
			if err := reportJob(&agentv1.ReportJobRunStatusRequest{AgentId: agentID, JobRunId: jobID, Status: 3, RuntimeBindingDigest: runtime.BindingDigest, StartTime: time.Now().Unix()}); err != nil {
				t.Fatal(err)
			}
			job, err := jobs.GetByJobRunID(f.ctx, jobID)
			if err != nil || job == nil {
				t.Fatalf("load synthetic job: %v", err)
			}
			admit := taskpayload.JobGenerationTransition{Version: 1, Identity: taskpayload.JobInputIdentity{PipelineID: f.pipelineID, PipelineRunID: runID, JobRunID: jobID, AgentID: agentID, ClaimID: claim, RuntimeBindingDigest: runtime.BindingDigest}, RetryPlanDigest: job.RetryPlanDigest, Operation: "admit"}
			admit.RequestDigest, err = admit.Digest()
			if err != nil {
				t.Fatal(err)
			}
			first, err := reconcile(admit)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := taskpayload.NewJobInputManifest("", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			body, err := manifest.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := rpcContext(claim)
			pin, err := f.client.AgentClient.GetOrRecordJobInputs(ctx, &agentv1.GetOrRecordJobInputsRequest{AgentId: agentID, PipelineId: f.pipelineID, PipelineRunId: runID, JobRunId: jobID, OriginalClaimId: claim, RuntimeBindingDigest: runtime.BindingDigest, ManifestJson: body})
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			failure := &agentv1.ReportStepRunStatusRequest{AgentId: agentID, StepRunId: ids[0], Status: 3, GenerationId: first.GenerationID, GenerationBindingDigest: first.BindingDigest}
			if err := reportStep(failure); err != nil {
				t.Fatal(err)
			}
			failure.Status, failure.ExitCode, failure.RetryableShellExit = 5, 9, true
			if err := reportStep(failure); err != nil {
				t.Fatal(err)
			}
			digest, err := agentidentity.TerminalReceiptDigest(failure, agentidentity.StepClaimID(claim, ids[0]), f.pipelineID, runID, jobID)
			if err != nil {
				t.Fatal(err)
			}
			advance := admit
			advance.Operation, advance.InputManifestDigest, advance.PreviousGenerationID = "advance", pin.ManifestDigest, first.GenerationID
			advance.FailedStepRunID, advance.FailedStepReceiptDigest = ids[0], digest
			advance.CleanupReceipt = &taskpayload.JobGenerationCleanupReceipt{Version: 1, PipelineRunID: runID, JobRunID: jobID, AgentID: agentID, OriginalClaimID: claim, RuntimeBindingDigest: runtime.BindingDigest, GenerationID: first.GenerationID, GenerationBindingDigest: first.BindingDigest, RuntimeClaimID: first.RuntimeClaimID, SessionID: first.RuntimeClaimID, Namespace: "synthetic-protocol-only", ContainerID: "arcentra-" + first.RuntimeClaimID, SnapshotID: "arcentra-" + first.RuntimeClaimID + "-snapshot", Snapshotter: "synthetic-protocol-only", ResourceOwnerDigest: strings.Repeat("a", 64), WorkspaceID: "job-" + first.RuntimeClaimID, WorkspaceDevice: 1, WorkspaceInode: 2, ProcessesAbsent: true, ContainerAbsent: true, SnapshotAbsent: true, WorkspaceAbsent: true}
			advance.RequestDigest, err = advance.Digest()
			if err != nil {
				t.Fatal(err)
			}
			before, err := jobs.GetByJobRunID(f.ctx, jobID)
			if err != nil {
				t.Fatal(err)
			}
			current := first
			if scenario.advance {
				current, err = reconcile(advance)
				if err != nil || current.Number != 2 {
					t.Fatalf("advance: %+v %v", current, err)
				}
				if err := jobs.FinalizeRetryJobIfCurrent(f.ctx, before, map[string]any{"status": 6, "end_time": time.Now()}); err == nil {
					t.Fatal("stale pre-handoff cancellation snapshot overwrote successor")
				}
			}
			if scenario.started {
				if err := reportStep(&agentv1.ReportStepRunStatusRequest{AgentId: agentID, StepRunId: ids[1], Status: 3, GenerationId: current.GenerationID, GenerationBindingDigest: current.BindingDigest}); err != nil {
					t.Fatal(err)
				}
			}
			job, err = jobs.GetByJobRunID(f.ctx, jobID)
			if err != nil {
				t.Fatal(err)
			}
			if err := jobs.FinalizeRetryJobIfCurrent(f.ctx, job, map[string]any{"status": 6, "end_time": time.Now()}); err != nil {
				t.Fatal(err)
			}
			if _, err := reconcile(advance); err == nil {
				t.Fatal("cancellation allowed a new or replayed execution admission")
			}
			inspect := admit
			inspect.Operation = "inspect"
			inspect.RequestDigest, err = inspect.Digest()
			if err != nil {
				t.Fatal(err)
			}
			observed, err := reconcile(inspect)
			if err != nil || observed.ExecutionAllowed || observed.GenerationID != current.GenerationID {
				t.Fatalf("cancelled handoff inspection: %+v %v", observed, err)
			}
			cancelJob := &agentv1.ReportJobRunStatusRequest{AgentId: agentID, JobRunId: jobID, Status: 6, RuntimeBindingDigest: runtime.BindingDigest}
			badProofs := []agentidentity.GenerationReport{{}, {ID: uuid.NewString(), BindingDigest: "v1:" + strings.Repeat("e", 64)}}
			if scenario.advance {
				badProofs = append(badProofs, agentidentity.GenerationReport{ID: first.GenerationID, BindingDigest: first.BindingDigest})
			}
			for range 2 {
				for _, proof := range badProofs {
					cancelJob.GenerationId, cancelJob.GenerationBindingDigest = proof.ID, proof.BindingDigest
					if err := reportJob(cancelJob); err == nil {
						t.Fatal("stale first job cancellation receipt accepted")
					}
					for _, id := range ids[1:] {
						if err := reportStep(&agentv1.ReportStepRunStatusRequest{AgentId: agentID, StepRunId: id, Status: 6, GenerationId: proof.ID, GenerationBindingDigest: proof.BindingDigest}); err == nil {
							t.Fatal("stale first step cancellation receipt accepted")
						}
					}
				}
			}
			job, err = jobs.GetByJobRunID(f.ctx, jobID)
			if err != nil || job.TerminalReportDigest != "" || job.ClaimID != claim || job.CurrentGenerationID != current.GenerationID {
				t.Fatalf("rejected receipt changed job evidence: %+v %v", job, err)
			}
			for _, id := range ids[1:] {
				step, err := f.repos.StepRun.GetByStepRunID(f.ctx, id)
				if err != nil || step.TerminalReportDigest != "" {
					t.Fatalf("rejected receipt poisoned step: %+v %v", step, err)
				}
			}
			cancelJob.GenerationId, cancelJob.GenerationBindingDigest = current.GenerationID, current.BindingDigest
			for range 2 {
				for _, id := range ids[1:] {
					if err := reportStep(&agentv1.ReportStepRunStatusRequest{AgentId: agentID, StepRunId: id, Status: 6, GenerationId: current.GenerationID, GenerationBindingDigest: current.BindingDigest}); err != nil {
						t.Fatal("exact current cancellation receipt lost ACK", err)
					}
				}
				if err := reportJob(cancelJob); err != nil {
					t.Fatal("exact job cancellation receipt lost ACK", err)
				}
				if err := reportStep(failure); err != nil {
					t.Fatal("historical failed generation receipt lost ACK", err)
				}
			}
			changed := proto.Clone(failure).(*agentv1.ReportStepRunStatusRequest)
			changed.RetryableShellExit = false
			if err := reportStep(changed); err == nil {
				t.Fatal("changed historical receipt was acknowledged")
			}
			future, err := f.repos.StepRun.GetByStepRunID(f.ctx, ids[2])
			if err != nil || future.ClaimID != "" || future.GenerationID != "" || future.TerminalReportDigest == "" {
				t.Fatalf("unused future cancellation invented execution: %+v %v", future, err)
			}
			var count int64
			if err := f.db.WithContext(f.ctx).Model(&model.JobRunGeneration{}).Where("job_run_id = ?", jobID).Count(&count).Error; err != nil || count != int64(current.Number) {
				t.Fatalf("cancel/handoff generation count: %d %v", count, err)
			}
			if err := f.db.WithContext(f.ctx).Model(&model.StepRun{}).Where("job_run_id = ? AND status IN ?", jobID, []int{1, 2, 3}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("cancel left open attempts: %d %v", count, err)
			}
			if err := f.db.WithContext(f.ctx).Model(&model.ProjectArtifact{}).Where("job_run_id = ?", jobID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("cancel published artifacts: %d %v", count, err)
			}
		})
	}
	t.Log("synthetic protocol rows on existing database: both cancellation/handoff orders, current-proof-only first receipts, stale snapshot CAS, unused future closure, exact cancellation/historical ACKs, no new runtime sessions")
}

// TestContainerGenerationCancellationFenceSQLiteControl 复用相同协议断言作普通本地回归
// 真正 MySQL 的执行由现有真实容器验收入口调用上述 helper，不在此声称 MySQL 验收
func TestContainerGenerationCancellationFenceSQLiteControl(t *testing.T) {
	f := newControllerWorkerFixture(t, false, controllerWorkerTLS(t))
	testRealContainerGenerationCancellationFence(t, f)
}
