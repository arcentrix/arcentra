//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/validation"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/sandbox"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func containerAcceptanceRetryApproval(t *testing.T, f *controllerWorkerFixture, cfg *realContainerSettings, store *containerAcceptanceStorage, retained map[string]string, services *controlservice.Services, policy *retryapproval.Policy, lifecycle *containerApprovalLifecycle, stepEvents ...*containerAcceptanceStepRuntimeEvents) {
	t.Helper()
	containerAcceptanceDrainWithAudit(t, f, retained)
	actor := "requester-" + id.GetUild()
	grantIDs := seedContainerApprovalActors(t, f, actor, policy.Candidates)
	// Only the disposable fixture is configured. No real user identity or credential is created.
	failedStep := len(stepEvents) > 0
	scenarios := []string{"approved-finite-plan", "cancel-pending", "revoke-before-successor"}
	if failedStep {
		scenarios = append(scenarios, "revoke-after-CAS-before-RUNNING", "lost-CAS-ACK-then-revoke")
	}
	for _, scenario := range scenarios {
		t.Run("durable-retry-approval-"+scenario, func(t *testing.T) {
			containerAcceptanceDrainWithAudit(t, f, retained)
			for _, grant := range grantIDs {
				if err := f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", grant).Update("is_enabled", 1).Error; err != nil {
					t.Fatal(err)
				}
			}
			source, initial, changed, advance := containerAcceptanceMutableRetrySource(t, f.ctx)
			command := `set -eu; test "$(cat source.txt)" = 'initial checkout'; test "$(cat seed/input.txt)" = 'approval-input'; printf approved > approval-command-marker; if [ "$ARCENTRA_JOB_GENERATION" = 1 ]; then exit 23; fi; mkdir -p proof; printf approved-result > proof/result.txt`
			command = strings.ReplaceAll(command, "ARCENTRA_JOB_GENERATION", taskpayload.RuntimeGenerationEnv)
			jobs := []*spec.Job{
				{Name: "approval-a", Steps: []*spec.Step{controllerWorkerShell("produce", "mkdir seed; printf approval-input > seed/input.txt"), containerAcceptanceArtifactStep("seed", "upload", map[string]any{"paths": []any{"seed/input.txt"}, "expireIn": "7d"})}},
				{Name: "approval-b", DependsOn: []string{"approval-a"}, Source: &spec.Source{Type: "git", Repo: source, Branch: "main"}, Retry: &spec.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{23}, Delay: "1s"}, Approval: &spec.Approval{Required: true, Type: "manual", Plugin: retryapproval.Plugin}, Steps: []*spec.Step{containerAcceptanceArtifactStep("fetch", "download", map[string]any{"job": "approval-a", "paths": []any{"seed/*"}}), controllerWorkerShell("approval-command", command), containerAcceptanceArtifactStep("publish", "upload", map[string]any{"paths": []any{"proof/result.txt"}, "expireIn": "7d"})}},
			}
			if failedStep {
				jobs[1].Retry.Mode = "failed_step"
				command = `set -eu; test "$(cat source.txt)" = 'initial checkout'; test "$(cat seed/input.txt)" = 'approval-input'; printf 'step-retry-enter\n'; printf approved > approval-command-marker; if [ ! -e failed-once ]; then touch failed-once; printf 'step-failed-first\n'; exit 23; fi; printf 'step-failed-second\n'; mkdir -p proof; printf approved-result > proof/result.txt`
				jobs[1].Steps[1] = controllerWorkerShell("approval-command", command)
				jobs[1].Steps = append(jobs[1].Steps[:1], append([]*spec.Step{controllerWorkerShell("earlier", "printf 'step-earlier-once\\n'")}, jobs[1].Steps[1:]...)...)
			}
			if failedStep && scenario == "revoke-before-successor" {
				delivery := stepEvents[0].approvalDelivery
				if delivery == nil {
					t.Fatal("missing transparent denial observer")
				}
				delivery.mu.Lock()
				delivery.targetName = "approval-b"
				delivery.mu.Unlock()
			}
			run := submitContainerApproval(t, f, cfg, actor, policy, jobs)
			var approval *model.ApprovalRequest
			f.wait("pinned durable retry approval", func() bool {
				values, err := services.Approval.ListByPipelineRun(f.ctx, run.RunID)
				if err != nil || len(values) != 1 {
					return false
				}
				approval = values[0]
				return approval.Status == model.ApprovalStatusPending
			})
			if scenario == "approved-finite-plan" {
				if !t.Run("personal-inbox-request-delivery", func(t *testing.T) {
					verifyContainerApprovalInbox(t, f, services, approval, actor, policy.Candidates[0], grantIDs[1])
				}) {
					t.FailNow()
				}
			}
			review, err := services.Approval.RetryReview(f.ctx, approval)
			if err != nil || review == nil || review.Evidence == nil {
				t.Fatalf("read exact retry evidence: %v", err)
			}
			evidence := review.Evidence
			if evidence.Inputs.SourceCommit != initial || initial == changed || evidence.Context.ActorID != actor || evidence.RetryPlan.Policy.MaxAttempts != 2 || len(evidence.Artifacts) != 1 || evidence.Artifacts[0].Name != "approval-a/seed/input.txt" || evidence.ExpiresAtUnixMilli != approval.ExpiresAt.UnixMilli() {
				t.Fatalf("approval omitted exact content/finite plan: %+v", evidence)
			}
			beforeJSON, _ := evidence.Canonical()
			jobID := evidence.Identity.JobRunID
			verifyContainerApprovalEvidence(t, f, evidence)
			pendingRecord := containerApprovalPending(t, f, jobID)
			lifecycle.requireNone(t, pendingRecord.Record.Resource.ID)
			if err := advance(f.ctx); err != nil {
				t.Fatal(err)
			}
			// A mutable branch move must not change the review or the eventual checkout.
			again, err := services.Approval.RetryReview(f.ctx, approval)
			if err != nil {
				t.Fatal(err)
			}
			againJSON, _ := again.Evidence.Canonical()
			if beforeJSON != againJSON {
				t.Fatal("branch move changed pinned approval")
			}
			if scenario == "cancel-pending" {
				if err := f.engine.CancelRun(run.RunID); err != nil {
					t.Fatal(err)
				}
				f.waitRun(run.RunID, model.PipelineStatusCancelled)
				containerAcceptanceDrainWithAudit(t, f, retained)
				if _, err := services.Approval.Decide(f.ctx, approval.ApprovalID, policy.Candidates[0], model.ApprovalDecisionApprove, ""); err == nil {
					t.Fatal("canceled pending gate accepted a new vote")
				}
				verifyContainerApprovalNeverStarted(t, f, pendingRecord)
				lifecycle.requireNone(t, pendingRecord.Record.Resource.ID)
				return
			}
			// One of two real votes must leave preparation blocked and task-free.
			if _, err := services.Approval.Decide(f.ctx, approval.ApprovalID, policy.Candidates[0], model.ApprovalDecisionApprove, "synthetic exact plan review"); err != nil {
				t.Fatal(err)
			}
			containerApprovalPending(t, f, jobID)
			lifecycle.requireNone(t, pendingRecord.Record.Resource.ID)
			store.mu.Lock()
			uploadsBefore := store.sealedUploads
			store.mu.Unlock()
			f.mu.Lock()
			initialLostACKs := f.lostACKs
			f.mu.Unlock()
			var mu sync.Mutex
			var admissionBodies []string
			lost := false
			var revokeErr error
			revoked := false
			f.mu.Lock()
			f.afterRetryApproval = func(_ context.Context, request *agentv1.ReconcileJobRetryApprovalRequest, response *agentv1.ReconcileJobRetryApprovalResponse, callErr error) error {
				if callErr != nil || response == nil {
					return nil
				}
				parsed, err := retryapproval.ParseRequest(request.RequestJson)
				if err != nil {
					return err
				}
				if parsed.Operation != "admit" || parsed.Identity.JobRunID != jobID {
					return nil
				}
				parsedResponse, err := retryapproval.ParseResponse(response.ResponseJson)
				if err != nil {
					return err
				}
				if parsedResponse.State != "approved" || parsedResponse.Admission == nil {
					return nil
				}
				mu.Lock()
				defer mu.Unlock()
				admissionBodies = append(admissionBodies, response.ResponseJson)
				if !lost {
					lost = true
					return status.Error(codes.Unavailable, "synthetic committed approval admission ACK loss")
				}
				return nil
			}
			if scenario == "revoke-before-successor" {
				f.observeTerminal = func(ctx context.Context, message proto.Message, _ any, callErr error) {
					step, ok := message.(*agentv1.ReportStepRunStatusRequest)
					if !ok || callErr != nil || int32(step.Status) != model.JobRunStatusFailed {
						return
					}
					row, err := f.repos.StepRun.GetByStepRunID(ctx, step.StepRunId)
					if err != nil || row.JobRunID != jobID || row.Name != "approval-command" || row.Attempt != 1 {
						return
					}
					mu.Lock()
					defer mu.Unlock()
					if revoked {
						return
					}
					revoked = true
					if failedStep {
						f.mu.Lock()
						f.loseStepTerminalACK = true
						f.mu.Unlock()
					}
					revokeErr = f.db.WithContext(ctx).Model(&model.RoleGrant{}).Where("grant_id = ?", grantIDs[1]).Update("is_enabled", 0).Error
				}
			}
			var stepBodies []string
			var stepRequests []string
			var liveScope *taskpayload.StepAttemptIdentity
			if failedStep {
				f.afterStepAttempt = func(ctx context.Context, req *agentv1.ReconcileStepAttemptRequest, resp *agentv1.ReconcileStepAttemptResponse, callErr error) error {
					if callErr != nil || resp == nil {
						return nil
					}
					tr, err := taskpayload.ParseStepAttemptTransition(req.TransitionJson)
					if err != nil {
						return err
					}
					if tr.Scope.Identity.JobRunID != jobID {
						return nil
					}
					row, err := f.repos.StepRun.GetByStepRunID(ctx, tr.Scope.StepRunID)
					if err != nil {
						return err
					}
					// 历史重放保留精确描述，独立检查当前不可执行权限
					structural := proto.Clone(resp).(*agentv1.ReconcileStepAttemptResponse)
					structural.ExecutionAllowed = true
					if err := containerAcceptanceStepCommittedAdmission(row, req, structural); err != nil {
						return err
					}
					current, err := f.repos.JobRun.GetByJobRunID(ctx, jobID)
					if err != nil {
						return err
					}
					if err := containerAcceptanceStepPinnedInputs(current, tr.Scope, initial); err != nil {
						return err
					}
					mu.Lock()
					if liveScope == nil {
						scope := tr.Scope
						liveScope = &scope
					}
					sameSession := liveScope.Session == tr.Scope.Session && liveScope.GenerationID == tr.Scope.GenerationID && liveScope.GenerationBindingDigest == tr.Scope.GenerationBindingDigest && liveScope.RuntimeClaimID == tr.Scope.RuntimeClaimID && liveScope.InputManifestDigest == tr.Scope.InputManifestDigest
					mu.Unlock()
					if !sameSession {
						return status.Error(codes.FailedPrecondition, "approved step replaced pinned live session/input")
					}
					if tr.Operation != "advance" {
						return nil
					}
					mu.Lock()
					defer mu.Unlock()
					stepBodies = append(stepBodies, resp.AdmissionJson)
					stepRequests = append(stepRequests, req.TransitionJson)
					if scenario == "revoke-after-CAS-before-RUNNING" || scenario == "lost-CAS-ACK-then-revoke" {
						if !revoked {
							revoked = true
							revokeErr = f.db.WithContext(ctx).Model(&model.RoleGrant{}).Where("grant_id = ?", grantIDs[1]).Update("is_enabled", 0).Error
						}
					}
					if len(stepBodies) == 1 && (scenario == "approved-finite-plan" || scenario == "lost-CAS-ACK-then-revoke") {
						return status.Error(codes.Unavailable, "synthetic committed step admission ACK loss")
					}
					if scenario == "lost-CAS-ACK-then-revoke" && resp.ExecutionAllowed {
						return status.Error(codes.FailedPrecondition, "historical ACK renewed permission")
					}
					return nil
				}
			}
			f.mu.Unlock()
			defer func() {
				f.mu.Lock()
				f.afterRetryApproval = nil
				f.observeTerminal = nil
				f.afterStepAttempt = nil
				f.mu.Unlock()
			}()
			// The final vote releases only the existing bounded wait; it does not dispatch a job.
			if _, err := services.Approval.Decide(f.ctx, approval.ApprovalID, policy.Candidates[1], model.ApprovalDecisionApprove, "synthetic exact plan review"); err != nil {
				t.Fatal(err)
			}
			if _, err := services.Approval.Decide(f.ctx, approval.ApprovalID, policy.Candidates[1], model.ApprovalDecisionApprove, "synthetic exact plan review"); err != nil {
				t.Fatalf("same committed vote replay: %v", err)
			}
			wanted := model.PipelineStatusSuccess
			if strings.HasPrefix(scenario, "revoke-") || scenario == "lost-CAS-ACK-then-revoke" {
				wanted = model.PipelineStatusFailed
			}
			f.waitRun(run.RunID, wanted)
			f.mu.Lock()
			f.afterRetryApproval = nil
			f.observeTerminal = nil
			f.afterStepAttempt = nil
			f.mu.Unlock()
			containerAcceptanceDrainWithAudit(t, f, retained)
			lifecycle.requireStarted(t, pendingRecord.Record.Resource.ID)
			job, err := f.repos.JobRun.GetByJobRunID(f.ctx, jobID)
			if err != nil {
				t.Fatal(err)
			}
			if job.SourceResolvedCommit != initial || job.RetryApprovalID != approval.ApprovalID || job.RetryApprovalEvidenceHash != review.EvidenceHash {
				t.Fatal("final job lost exact approved binding")
			}
			if !failedStep && scenario == "approved-finite-plan" && job.CurrentGeneration != 2 {
				t.Fatal("approved finite plan did not execute exactly two generations")
			}
			if scenario == "revoke-before-successor" && job.CurrentGeneration != 1 {
				t.Fatal("revoked approval granted successor")
			}
			mu.Lock()
			if scenario == "revoke-before-successor" && (!revoked || revokeErr != nil) {
				t.Errorf("revocation hook failed: %v", revokeErr)
			}
			if !lost || len(admissionBodies) < 2 || admissionBodies[0] != admissionBodies[1] {
				t.Error("committed approval admission ACK did not replay identically")
			}
			mu.Unlock()
			var votes int64
			if err := f.db.Model(&model.ApprovalDecision{}).Where("approval_id = ?", approval.ApprovalID).Count(&votes).Error; err != nil || votes != 2 {
				t.Fatalf("duplicate vote count=%d: %v", votes, err)
			}
			if failedStep {
				if job.CurrentGeneration != 1 {
					t.Fatal("approval failed-step advanced generation")
				}
				counts, pid, err := stepEvents[0].counts(pendingRecord.Record.Resource)
				expectedExec, expectedResume := 2, 0
				if scenario == "approved-finite-plan" {
					expectedExec, expectedResume = 3, 1
				}
				if err != nil || pid == 0 || counts["create"] != 1 || counts["task-start"] != 1 || counts["exec-create"] != expectedExec || counts["exec-start"] != expectedExec || counts["resume"] != expectedResume {
					t.Fatalf("approval runtime effects %v pid=%d err=%v", counts, pid, err)
				}
				var outputs int64
				if err := f.db.Model(&model.ProjectArtifact{}).Where("job_run_id = ?", jobID).Count(&outputs).Error; err != nil {
					t.Fatal(err)
				}
				wantOutputs := int64(0)
				if scenario == "approved-finite-plan" {
					wantOutputs = 1
				}
				if outputs != wantOutputs {
					t.Fatalf("registered publications=%d want=%d", outputs, wantOutputs)
				}
				mu.Lock()
				if (scenario == "approved-finite-plan" || scenario == "lost-CAS-ACK-then-revoke") && (len(stepBodies) != 2 || stepBodies[0] != stepBodies[1] || stepRequests[0] != stepRequests[1]) {
					t.Errorf("step admission synchronous ACK replay changed: responses=%d requests=%d; one injected committed ACK loss requires exactly two same-request calls", len(stepBodies), len(stepRequests))
				}
				if scenario == "revoke-before-successor" && len(stepBodies) != 0 {
					t.Error("revoked failure created successor CAS")
				}
				if strings.HasPrefix(scenario, "revoke-") || scenario == "lost-CAS-ACK-then-revoke" {
					if !revoked || revokeErr != nil {
						t.Errorf("step revocation hook: %v", revokeErr)
					}
				}
				mu.Unlock()
				store.mu.Lock()
				uploaded := store.sealedUploads - uploadsBefore
				store.mu.Unlock()
				if int64(uploaded) != wantOutputs {
					t.Fatalf("actual S3 uploads=%d want=%d", uploaded, wantOutputs)
				}
				steps, err := containerApprovalPersistedSteps(f.ctx, f.db, jobID)
				if err != nil {
					t.Fatal(err)
				}
				var ids []string
				for _, step := range steps {
					ids = append(ids, step.StepRunID)
					if scenario == "revoke-before-successor" && step.Name == "approval-command" && step.Attempt == 1 {
						f.wait("original revoked handler completion", func() bool {
							snapshot := stepEvents[0].approvalDelivery.snapshot()
							return len(snapshot.invocations) == 1 && snapshot.invocations[0].end != 0
						})
						snapshot := stepEvents[0].approvalDelivery.snapshot()
						f.mu.Lock()
						receipts := append([]controllerWorkerReceipt(nil), f.receipts["step/"+step.StepRunID]...)
						lostACKs := f.lostACKs - initialLostACKs
						f.mu.Unlock()
						admission, err := taskpayload.ParseStepAttemptAdmission(step.AttemptAdmission)
						t.Logf("revoked failed terminal delivery: %s committed=%d injectedLosses=%d persistedDigest=%s", snapshot.summary(), len(receipts), lostACKs, step.TerminalReportDigest)
						if err != nil {
							t.Fatal(err)
						}
						if step.Status != model.JobRunStatusFailed || !step.RetryableShellExit || step.ExitCode == nil || *step.ExitCode != 23 || step.GenerationID != admission.Scope.GenerationID || step.GenerationBindingDigest != admission.Scope.GenerationBindingDigest || step.ClaimID != admission.Scope.StepClaimID {
							t.Fatal("persisted failure/status/exit/claim/generation differs from immutable admission")
						}
						for index, call := range snapshot.stepCalls {
							computed, computeErr := agentidentity.TerminalReceiptDigest(call.message, call.claim, admission.Scope.Identity.PipelineID, admission.Scope.Identity.PipelineRunID, admission.Scope.Identity.JobRunID)
							payloadSHA := sha256.Sum256(call.payload)
							t.Logf("revoked failed terminal RPC%d source=%s callback=%d result=%s accepted=%t payloadSHA256=%x claimMatches=%t digest=%s digestMatches=%t digestError=%v", index+1, call.origin, call.invocation, call.code, call.success, payloadSHA, call.claim == step.ClaimID, computed, computed == step.TerminalReportDigest, computeErr)
						}
						if err := snapshot.validateDeniedStep(receipts, *admission, step.TerminalReportDigest, lostACKs); err != nil {
							t.Fatalf("revoked failed terminal receipt proof: %v", err)
						}
					}
				}
				wantMarkers := map[string]int{"step-earlier-once": 1, "step-failed-first": 1, "step-retry-enter": 1}
				if scenario == "approved-finite-plan" {
					wantMarkers["step-failed-second"] = 1
					wantMarkers["step-retry-enter"] = 2
				}
				f.wait("exact approved live command counts", func() bool {
					logs, err := f.repos.Log.ListByStepRunIDs(f.ctx, ids, 0, 1000)
					if err != nil {
						return false
					}
					markers, err := containerAcceptanceStepMarkerCounts(logs, f.conf.Agent.ID)
					if err != nil {
						return false
					}
					for marker, want := range wantMarkers {
						if markers[marker] != want {
							return false
						}
					}
					return markers["step-failed-second"] == wantMarkers["step-failed-second"]
				})
				containerAcceptanceRemoved(t, f, pendingRecord)
			}
			final, err := services.Approval.RetryReview(f.ctx, approval)
			if err != nil {
				t.Fatal(err)
			}
			finalJSON, _ := final.Evidence.Canonical()
			if finalJSON != beforeJSON {
				t.Fatal("execution changed immutable approval evidence")
			}
		})
	}
	if t.Failed() {
		return
	}
	t.Log("durable retry approval actual Process/Kafka/TLS Agent/MySQL/containerd/S3 verified: pending task-free pinned SHA and input evidence, two-voter finite plan, duplicate vote and admission ACK replay, cancellation and revoked-successor denial, exact cleanup")
}

func submitContainerApproval(t *testing.T, f *controllerWorkerFixture, cfg *realContainerSettings, actor string, policy *retryapproval.Policy, jobs []*spec.Job) *model.PipelineRun {
	t.Helper()
	ctx, cancel := f.client.WithTimeoutAndAuth(f.ctx)
	failedStep := false
	for _, job := range jobs {
		if job.Retry != nil && job.Retry.Mode == "failed_step" {
			failedStep = true
		}
	}
	// 与服务广告相同的门槛；就绪来自当前普通 StartWorker 的真实后端
	runtimePolicy := f.conf.Agent.Sandbox.Containerd.Policy
	ready := f.worker.OfflineRuntimeReady()
	wholeReady := ready && runtimePolicy.AllowWholeJobRetry
	stepReady := taskpayload.FailedStepRuntimeAccepted && ready && runtimePolicy.AllowFailedStepRetry && f.worker.FailedStepRetryReady()
	capabilities := &agentv1.RuntimeCapabilities{
		ExecutionMode:                       taskpayload.RuntimeContainerdOfflineV1,
		ContainerdOfflineV1:                 ready,
		ContainerdWholeJobRetryV1:           wholeReady,
		ContainerdRetryApprovalV1:           wholeReady && runtimePolicy.AllowWholeJobRetryApproval,
		ContainerdFailedStepRetryV1:         stepReady,
		ContainerdFailedStepRetryApprovalV1: taskpayload.FailedStepRetryApprovalAccepted && stepReady && runtimePolicy.AllowFailedStepRetryApproval,
	}
	if failedStep && taskpayload.FailedStepRetryApprovalAccepted && !capabilities.ContainerdFailedStepRetryApprovalV1 {
		t.Fatal("actual production worker did not advertise approved live-step readiness")
	}
	if failedStep && !taskpayload.FailedStepRetryApprovalAccepted {
		// 仅关闭门禁的专用候选组装可以注入能力；已接受路径必须使用真实运行时就绪
		runtimePolicy := f.conf.Agent.Sandbox.Containerd.Policy
		if !capabilities.ContainerdOfflineV1 || !runtimePolicy.AllowFailedStepRetry || !runtimePolicy.AllowFailedStepRetryApproval || os.Getenv("ARCENTRA_CONTAINERD_ACCEPTANCE") != "1" || os.Getenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE") != "1" {
			t.Fatal("candidate approval capability requires exact dedicated assembly")
		}
		capabilities.ContainerdFailedStepRetryV1, capabilities.ContainerdFailedStepRetryApprovalV1 = true, true
	}
	response, err := f.client.AgentClient.Heartbeat(ctx, &agentv1.HeartbeatRequest{AgentId: f.conf.Agent.ID, Status: agentv1.AgentStatus_AGENT_STATUS_IDLE, Timestamp: time.Now().Unix(), RuntimeCapabilities: capabilities})
	cancel()
	if err != nil || response == nil || !response.Success {
		t.Fatalf("authenticated approval capability: %v", err)
	}
	pipeline := &spec.Pipeline{Namespace: "container-approval-acceptance", Runtime: &spec.Runtime{Type: taskpayload.RuntimeContainerdOfflineV1, Image: cfg.image, Resources: &spec.Resources{CpuLimit: "500m", MemoryLimit: "64Mi"}}, Jobs: jobs}
	run := &model.PipelineRun{RunID: id.GetUild(), PipelineID: f.pipelineID, Status: model.PipelineStatusPending, Env: "{}", TotalJobs: len(jobs), TriggerType: 1, TriggeredBy: actor, DefinitionCommitSha: strings.Repeat("a", 40), DefinitionPath: ".arcentra/acceptance.yaml"}
	frozen := *policy
	frozen.Candidates = append([]string(nil), policy.Candidates...)
	frozen.Fingerprint, err = frozen.Digest()
	if err != nil {
		t.Fatal(err)
	}
	definition, err := validation.ResolvedDefinitionDigest(pipeline)
	if err != nil {
		t.Fatal(err)
	}
	captured := &retryapproval.RunContext{Version: 1, ProjectID: f.projectID, PipelineID: f.pipelineID, PipelineRunID: run.RunID, ActorID: actor, DefinitionCommitSHA: run.DefinitionCommitSha, DefinitionPath: run.DefinitionPath, ResolvedDefinitionDigest: definition, GatedJobs: []string{"approval-b"}, Policy: frozen}
	run.RetryApprovalContext, err = captured.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	run.RetryApprovalContextDigest, err = captured.Digest()
	if err != nil {
		t.Fatal(err)
	}
	creator, ok := f.repos.Pipeline.(repo.IVerifiedManualTriggerCreator)
	if !ok {
		t.Fatal("durable run capture missing")
	}
	if err := creator.CreateVerifiedManualTriggerRun(f.ctx, actor, run, authorizeContainerInboxFixture); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.SubmitVerified(controlservice.WithVerifiedPipelineActor(f.ctx, actor), run, pipeline); err != nil {
		t.Fatalf("actual verified Process submission: %v", err)
	}
	return run
}

func containerApprovalPending(t *testing.T, f *controllerWorkerFixture, jobID string) containerAcceptanceJournal {
	t.Helper()
	var pending containerAcceptanceJournal
	f.wait("approval holds exact private workspace before runtime creation", func() bool {
		records, err := containerAcceptanceRecords(f.conf.Agent.Sandbox.Containerd.JournalDir)
		if err != nil {
			return false
		}
		for _, record := range records {
			if record.Record != nil && record.Record.JobRunID == jobID && record.Record.Phase == "allocated" {
				pending = record
				return true
			}
		}
		return false
	})
	state, err := f.worker.offline.backend.InspectOwned(f.ctx, pending.Record.Resource)
	if err != nil || state != (sandbox.ResourceState{}) {
		t.Fatalf("pending approval already created runtime: %+v %v", state, err)
	}
	steps, err := containerApprovalPersistedSteps(f.ctx, f.db, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) == 0 {
		t.Fatal("pending approval has no persisted steps")
	}
	for _, step := range steps {
		if step.AttemptAdmission != "" || step.AttemptAdmissionRequest != "" || (int(step.Status) != 1 && int(step.Status) != 2) {
			t.Fatalf("pending approval executed a step: %+v", step)
		}
	}
	if _, err := os.Stat(filepath.Join(f.conf.Agent.WorkspaceDir, pending.Record.Directory, "approval-command-marker")); !os.IsNotExist(err) {
		t.Fatalf("pending approval command marker exists: %v", err)
	}
	var outputs int64
	if err := f.db.Model(&model.ProjectArtifact{}).Where("job_run_id = ?", jobID).Count(&outputs).Error; err != nil || outputs != 0 {
		t.Fatalf("pending approval published output count=%d: %v", outputs, err)
	}
	return pending
}
func verifyContainerApprovalNeverStarted(t *testing.T, f *controllerWorkerFixture, pending containerAcceptanceJournal) {
	t.Helper()
	state, err := f.worker.offline.backend.InspectOwned(f.ctx, pending.Record.Resource)
	if err != nil || state != (sandbox.ResourceState{}) {
		t.Fatalf("canceled approval resources remain: %+v %v", state, err)
	}
	if _, err := os.Stat(filepath.Join(f.conf.Agent.WorkspaceDir, pending.Record.Directory)); !os.IsNotExist(err) {
		t.Fatalf("canceled approval workspace remains: %v", err)
	}
}

func seedContainerApprovalActors(t *testing.T, f *controllerWorkerFixture, actor string, reviewers []string) []string {
	t.Helper()
	if err := f.db.Model(&model.Project{}).Where("project_id = ?", f.projectID).Updates(map[string]any{"is_enabled": 1, "status": model.ProjectStatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	users := append([]string{actor}, reviewers...)
	roles := []string{}
	grants := []string{}
	permissions := []string{}
	for index, user := range users {
		role := "role-" + id.GetUild()
		grant := "grant-" + id.GetUild()
		roles = append(roles, role)
		grants = append(grants, grant)
		for _, row := range []any{&model.User{UserID: user, IsEnabled: 1}, &model.Role{RoleID: role, Name: role, IsEnabled: 1}, &model.RoleGrant{GrantID: grant, SubjectType: "user", SubjectID: user, RoleID: role, ScopeType: "project", ScopeID: f.projectID, IsEnabled: 1}} {
			if err := f.db.Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
		actions := []string{"project:read", "pipeline:read", "approval:read"}
		if index == 0 {
			actions = append(actions, "pipeline:trigger")
		} else {
			actions = append(actions, "approval:approve", "approval:reject")
		}
		for _, action := range actions {
			parts := strings.Split(action, ":")
			permission := "permission-" + id.GetUild()
			permissions = append(permissions, permission)
			if err := f.db.Create(&model.Permission{PermissionID: permission, ResourceType: parts[0], Action: parts[1]}).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Create(&model.RolePermissionBinding{RoleID: role, PermissionID: permission}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var runIDs []string
		if err := f.db.WithContext(ctx).Model(&model.PipelineRun{}).Where("pipeline_id = ?", f.pipelineID).Pluck("run_id", &runIDs).Error; err != nil {
			t.Error(err)
			return
		}
		var approvalIDs []string
		if len(runIDs) > 0 {
			if err := f.db.WithContext(ctx).Model(&model.ApprovalRequest{}).Where("pipeline_run_id IN ?", runIDs).Pluck("approval_id", &approvalIDs).Error; err != nil {
				t.Error(err)
				return
			}
		}
		// Only this fixture's recipients and sources are removed; no global sweep state.
		for _, item := range []struct {
			entity any
			column string
			ids    []string
		}{
			{&model.InboxDelivery{}, "recipient_id", users},
			{&model.InboxCursor{}, "owner_id", users},
			{&model.InboxWork{}, "source_id", append(append([]string(nil), runIDs...), approvalIDs...)},
		} {
			if len(item.ids) > 0 {
				if err := f.db.WithContext(ctx).Where(item.column+" IN ?", item.ids).Delete(item.entity).Error; err != nil {
					t.Error(err)
				}
			}
		}
		if len(approvalIDs) > 0 {
			for _, entity := range []any{&model.ApprovalDecision{}, &model.ApprovalRequest{}} {
				if err := f.db.WithContext(ctx).Where("approval_id IN ?", approvalIDs).Delete(entity).Error; err != nil {
					t.Error(err)
				}
			}
		}
		for _, item := range []struct {
			entity any
			column string
			ids    []string
		}{{&model.RolePermissionBinding{}, "role_id", roles}, {&model.RoleGrant{}, "grant_id", grants}, {&model.Role{}, "role_id", roles}, {&model.Permission{}, "permission_id", permissions}, {&model.User{}, "user_id", users}} {
			if err := f.db.WithContext(ctx).Where(item.column+" IN ?", item.ids).Delete(item.entity).Error; err != nil {
				t.Error(err)
			}
		}
	})
	return grants
}

// Counts are captured before each real SDK RPC, including failed attempts.
// This proves absence across the entire pending/cancel lifecycle, not sampling.
type containerApprovalLifecycle struct {
	mu    sync.Mutex
	calls map[string]map[string]int
}

func (a *containerApprovalLifecycle) record(method, id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.calls[id] == nil {
		a.calls[id] = make(map[string]int)
	}
	a.calls[id][method]++
}
func (a *containerApprovalLifecycle) requireNone(t *testing.T, id string) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.calls[id]) != 0 {
		t.Fatalf("pending/canceled approval attempted runtime lifecycle RPC: %v", a.calls[id])
	}
}
func (a *containerApprovalLifecycle) requireStarted(t *testing.T, id string) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, method := range []string{"/containerd.services.containers.v1.Containers/Create", "/containerd.services.tasks.v1.Tasks/Create", "/containerd.services.tasks.v1.Tasks/Start"} {
		if a.calls[id][method] != 1 {
			t.Fatalf("expected one actual approved lifecycle call %s: %v", method, a.calls[id])
		}
	}
}

func verifyContainerApprovalEvidence(t *testing.T, f *controllerWorkerFixture, e *retryapproval.Evidence) {
	t.Helper()
	job, err := f.repos.JobRun.GetByJobRunID(f.ctx, e.Identity.JobRunID)
	if err != nil {
		t.Fatal(err)
	}
	planJSON, err := e.RetryPlan.Canonical()
	if err != nil || planJSON != job.RetryPlan || e.RetryPlanDigest != job.RetryPlanDigest || e.InputManifestDigest != job.InputManifestDigest {
		t.Fatal("review differs from complete captured plan/input manifest")
	}
	steps, err := containerApprovalPersistedSteps(f.ctx, f.db, job.JobRunID)
	count := int64(len(steps))
	if err != nil || count != int64(len(e.RetryPlan.Steps)*int(e.RetryPlan.Policy.MaxAttempts)) {
		t.Fatalf("complete preallocated review step count %d: %v", count, err)
	}
	seen := map[string]bool{}
	for _, stepPlan := range e.RetryPlan.Steps {
		for attempt, identity := range stepPlan.AttemptRunIDs {
			found := false
			for _, step := range steps {
				if step.StepRunID == identity {
					if seen[identity] || step.Name != stepPlan.Name || step.Attempt != attempt+1 || step.JobRunID != job.JobRunID {
						t.Fatal("review attempt identity mismatch")
					}
					seen[identity] = true
					found = true
				}
			}
			if !found {
				t.Fatal("review omitted a real persisted attempt")
			}
		}
	}
	for _, input := range e.Artifacts {
		rows, total, err := f.repos.Artifact.List(f.ctx, repo.ArtifactQuery{ProjectID: f.projectID, RunID: job.PipelineRunID, JobRunID: input.ProducerJobRunID, Limit: 100})
		if err != nil || total != 1 {
			t.Fatalf("review producer row count %d: %v", total, err)
		}
		row := rows[0]
		expiry := int64(0)
		if row.ExpiresAt != nil {
			expiry = row.ExpiresAt.Unix()
		}
		sum := sha256.Sum256([]byte("approval-input"))
		if row.StepRunID != input.ProducerStepRunID || row.StoragePath != input.StorageKey || row.StorageID != input.Reference.StorageID || row.StorageFingerprint != input.Reference.Fingerprint || row.SHA256 != input.Reference.SHA256 || row.SHA256 != hex.EncodeToString(sum[:]) || input.Reference.Size == nil || row.Size != *input.Reference.Size || row.Size != int64(len("approval-input")) || input.Reference.ExpiresAt != expiry {
			t.Fatal("review input differs from actual registered producer bytes/storage")
		}
	}
}

// Query the runtime identity, never the definition-level job_id or a paginated subset.
func containerApprovalPersistedSteps(ctx context.Context, db *gorm.DB, jobRunID string) ([]model.StepRun, error) {
	var steps []model.StepRun
	err := db.WithContext(ctx).Where("job_run_id = ?", jobRunID).Find(&steps).Error
	return steps, err
}
