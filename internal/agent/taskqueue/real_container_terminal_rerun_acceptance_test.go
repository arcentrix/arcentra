//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	controlconfig "github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

// This journey starts with the authenticated production manual trigger. No Run,
// Job, Step, snapshot, decision, protected payload or claim is inserted by it.
func containerAcceptanceTerminalRerun(t *testing.T, f *controllerWorkerFixture, cfg *realContainerSettings, services *controlservice.Services, cacheStore cache.ICache, conf *controlconfig.AppConfig, policy *retryapproval.Policy, store *containerAcceptanceStorage, lifecycle *containerApprovalLifecycle, events *containerAcceptanceStepRuntimeEvents, retained map[string]string, publisher executor.LogPublisher) {
	t.Helper()
	if !f.worker.OfflineRuntimeReady() || !f.conf.Agent.Sandbox.Containerd.TerminalRerunEnabled || !taskpayload.TerminalRerunAllowed(f.conf.Agent.Sandbox.Containerd.Policy) {
		t.Fatal("terminal worker is not actually ready and opted in")
	}
	reportReadiness := func() {
		ctx, cancel := f.client.WithTimeoutAndAuth(f.ctx)
		heartbeat, err := f.client.AgentClient.Heartbeat(ctx, &agentv1.HeartbeatRequest{AgentId: f.conf.Agent.ID, Status: agentv1.AgentStatus_AGENT_STATUS_IDLE, Timestamp: time.Now().Unix(), RuntimeCapabilities: &agentv1.RuntimeCapabilities{ContainerdOfflineV1: true, ContainerdWholeJobRetryV1: true, ContainerdRetryApprovalV1: true, CopilotWholeJobRerunPreparationV1: true, ExecutionMode: taskpayload.RuntimeContainerdOfflineV1}})
		cancel()
		if err != nil || heartbeat == nil || !heartbeat.Success {
			t.Fatal("authenticated terminal worker readiness report failed")
		}
	}
	reportReadiness()
	actor := "terminal-owner-" + id.GetUild()
	seedContainerApprovalActors(t, f, actor, policy.Candidates)
	httpFixture := newTerminalRerunHTTP(t, f, services, cacheStore, conf, append([]string{actor}, policy.Candidates...))
	source := newTerminalRerunSource(t, f.ctx, cfg.image, time.Now().UTC().Add(60*time.Second).Truncate(time.Second))
	var originalPipeline model.Pipeline
	var originalProject model.Project
	if err := f.db.Where("pipeline_id = ?", f.pipelineID).First(&originalPipeline).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Where("project_id = ?", f.projectID).First(&originalProject).Error; err != nil {
		t.Fatal(err)
	}
	// Only disposable configuration changes; original execution records are made
	// atomically by the normal trigger/capture/admission path.
	pipelineConfig := map[string]any{"repo_url": source.URL, "default_branch": "main", "pipeline_file_path": terminalRerunDefinitionPath, "is_enabled": 1}
	projectConfig := map[string]any{"repo_url": source.URL, "repo_type": model.RepoTypeGit}
	if err := f.db.Model(&model.Pipeline{}).Where("pipeline_id = ?", f.pipelineID).Updates(pipelineConfig).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.Project{}).Where("project_id = ?", f.projectID).Updates(projectConfig).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.db.Model(&model.Pipeline{}).Where("pipeline_id = ?", f.pipelineID).Updates(map[string]any{"repo_url": originalPipeline.RepoURL, "default_branch": originalPipeline.DefaultBranch, "pipeline_file_path": originalPipeline.PipelineFilePath, "is_enabled": originalPipeline.IsEnabled}).Error; err != nil {
			t.Error(err)
		}
		if err := f.db.Model(&model.Project{}).Where("project_id = ?", f.projectID).Updates(map[string]any{"repo_url": originalProject.RepoURL, "repo_type": originalProject.RepoType}).Error; err != nil {
			t.Error(err)
		}
	}()
	var trigger struct {
		RunID string `json:"runId"`
	}
	httpFixture.request(t, f.ctx, actor, "POST", "/pipelines/"+f.pipelineID+"/trigger", map[string]any{"requestId": id.GetUild(), "variables": source.Variables}, &trigger)
	if trigger.RunID == "" {
		t.Fatal("production trigger returned no original identity")
	}
	originalJob := terminalRerunWaitJob(t, f, trigger.RunID)
	originalApproval := terminalRerunWaitApproval(t, f, originalJob.JobRunID)
	originalPending := containerApprovalPending(t, f, originalJob.JobRunID)
	lifecycle.requireNone(t, originalPending.Record.Resource.ID)
	for _, candidate := range policy.Candidates {
		httpFixture.request(t, f.ctx, candidate, "POST", "/approvals/"+originalApproval.ApprovalID+"/approve", map[string]string{"reason": "explicit original fixed retry budget"}, nil)
	}
	originalRun := f.waitRun(trigger.RunID, model.PipelineStatusFailed)
	originalJob = terminalRerunReadJob(t, f, originalJob.JobRunID)
	if originalJob.Status != model.JobRunStatusFailed || originalJob.CurrentGeneration != 2 || originalJob.EndTime == nil || !originalJob.EndTime.Before(source.Cutoff) || originalJob.SourceResolvedCommit != source.Commit || originalJob.TerminalReportDigest == "" {
		t.Fatal("original did not exhaust the fixed budget before the immutable UTC cutoff")
	}
	originalSteps, err := containerApprovalPersistedSteps(f.ctx, f.db, originalJob.JobRunID)
	if err != nil {
		t.Fatal(err)
	}
	failures := 0
	for _, step := range originalSteps {
		if step.Name == "rerun-command" {
			if step.Status != model.JobRunStatusFailed || step.ExitCode == nil || *step.ExitCode != 23 || step.EndTime == nil || !step.EndTime.Before(source.Cutoff) {
				t.Fatal("original shell did not fail with exact exit 23 before cutoff")
			}
			failures++
		}
	}
	if failures != 2 {
		t.Fatal("original did not execute exactly two failed shell attempts")
	}
	terminalRerunNoArtifacts(t, f, originalJob.JobRunID)
	containerAcceptanceRemoved(t, f, originalPending)
	containerAcceptanceDrainWithAudit(t, f, retained)
	var snapshot model.CopilotDefinitionSnapshot
	if err := f.db.Where("run_id = ?", originalRun.RunID).First(&snapshot).Error; err != nil || snapshot.DefinitionRevision != source.Commit || snapshot.DefinitionPath != terminalRerunDefinitionPath || snapshot.DefinitionRepository != source.URL || len(snapshot.PayloadCiphertext) == 0 {
		t.Fatal("manual trigger did not atomically capture the exact prospective definition")
	}
	var oldGenerations []model.JobRunGeneration
	if err := f.db.Where("job_run_id = ?", originalJob.JobRunID).Order("number").Find(&oldGenerations).Error; err != nil || len(oldGenerations) != 2 {
		t.Fatal("original two-generation evidence missing")
	}
	oldApproval := terminalRerunWaitApproval(t, f, originalJob.JobRunID)
	var oldVotes []model.ApprovalDecision
	if err := f.db.Where("approval_id = ?", oldApproval.ApprovalID).Order("decision_id").Find(&oldVotes).Error; err != nil || len(oldVotes) != 2 {
		t.Fatal("original independent approvals not durable")
	}
	// Moving main cannot substitute a new definition or checkout in the rerun.
	if err := source.advance(f.ctx); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(time.Until(source.Cutoff))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	reportReadiness()
	// A separate, explicitly counted cancellation exercises the existing prepared
	// cancellation boundary. It must never create its reserved target execution.
	var cancelledPrepared controlservice.CopilotRerunIntent
	basePath := "/projects/" + f.projectID + "/copilot/rerun-intents"
	httpFixture.request(t, f.ctx, actor, "POST", basePath+"/", controlservice.CopilotRerunPrepareRequest{RequestID: id.GetUild(), OriginalJobRunID: originalJob.JobRunID, SnapshotDigest: snapshot.BindingDigest}, &cancelledPrepared)
	cancelRequest := controlservice.CopilotRerunCancelRequest{ExpectedVersion: cancelledPrepared.Version, IntentDigest: cancelledPrepared.IntentDigest, ReviewedRunContextDigest: cancelledPrepared.ReviewedRunContextDigest}
	var cancelled controlservice.CopilotRerunIntent
	httpFixture.request(t, f.ctx, actor, "POST", basePath+"/"+cancelledPrepared.IntentID+"/cancel-prepared", cancelRequest, &cancelled)
	if cancelled.State != "cancelled" || cancelled.CancelledAt == nil {
		t.Fatal("prepared cancellation did not persist")
	}
	httpFixture.reject(t, f.ctx, actor, basePath+"/"+cancelledPrepared.IntentID+"/confirm", controlservice.CopilotRerunConfirmRequest{ExpectedVersion: cancelledPrepared.Version, IntentDigest: cancelledPrepared.IntentDigest, ReviewedRunContextDigest: cancelledPrepared.ReviewedRunContextDigest, AcknowledgeNewExecution: true}, http.StatusConflict)
	var cancelledTargets int64
	if err := f.db.Model(&model.PipelineRun{}).Where("run_id = ?", cancelledPrepared.NewRunID).Count(&cancelledTargets).Error; err != nil || cancelledTargets != 0 {
		t.Fatal("cancelled preparation created an execution")
	}
	scope := map[string]any{"version": "copilot-terminal-rerun-browser-v1", "actorId": actor, "projectId": f.projectID, "pipelineId": f.pipelineID, "originalRunId": originalRun.RunID, "originalJobRunId": originalJob.JobRunID, "definitionRevision": snapshot.DefinitionRevision, "expectedSourceCommit": source.Commit, "resolvedSpecDigest": snapshot.ResolvedSpecDigest, "snapshotDigest": snapshot.BindingDigest}
	var preparation struct {
		Phase            string                            `json:"phase"`
		Prepared         controlservice.CopilotRerunIntent `json:"prepared"`
		Confirmed        controlservice.CopilotRerunIntent `json:"confirmed"`
		PreparePosts     int                               `json:"preparePosts"`
		ConfirmPosts     int                               `json:"confirmPosts"`
		ApprovalPosts    int                               `json:"approvalPosts"`
		ResponseRecovery terminalRerunResponseRecovery     `json:"responseRecovery"`
	}
	httpFixture.browser(t, f, "prepare", actor, scope, &preparation)
	// The browser login replaces the actor's one cached session. Reauthenticate
	// through the real route before subsequent Go requests; never inject a token.
	httpFixture.login(t, f.ctx, actor)
	if !preparation.ResponseRecovery.valid() {
		t.Fatal("browser did not recover both committed response losses through exactly two reloads and read-only inspections")
	}
	confirmed := preparation.Confirmed
	if preparation.Phase != "confirmed-awaiting-independent-approval" || preparation.PreparePosts != 1 || preparation.ConfirmPosts != 1 || preparation.ApprovalPosts != 0 || confirmed.IntentID == "" || confirmed.IntentID != preparation.Prepared.IntentID || confirmed.RequestID == "" || confirmed.RequestID != preparation.Prepared.RequestID || confirmed.ProjectID != f.projectID || confirmed.PipelineID != f.pipelineID || confirmed.OriginalRunID != originalRun.RunID || confirmed.OriginalJobRunID != originalJob.JobRunID || confirmed.NewRunID != preparation.Prepared.NewRunID || confirmed.NewJobRunID != preparation.Prepared.NewJobRunID || confirmed.NewRunID == originalRun.RunID || confirmed.NewJobRunID == originalJob.JobRunID || confirmed.NewRunID == "" || confirmed.NewJobRunID == "" {
		t.Fatal("browser did not confirm one fixed independent execution")
	}
	freshApproval := terminalRerunWaitApproval(t, f, confirmed.NewJobRunID)
	httpFixture.reject(t, f.ctx, actor, "/approvals/"+freshApproval.ApprovalID+"/approve", map[string]string{"reason": "requester cannot replace independent reviewer"}, http.StatusForbidden)
	var beforeVotes int64
	if err := f.db.Model(&model.ApprovalDecision{}).Where("approval_id = ?", freshApproval.ApprovalID).Count(&beforeVotes).Error; err != nil || beforeVotes != 0 {
		t.Fatal("confirmation/requester or old votes authorized new execution")
	}

	freshJob := terminalRerunReadJob(t, f, confirmed.NewJobRunID)
	if freshApproval.ApprovalID == oldApproval.ApprovalID || freshJob.ClaimID == originalJob.ClaimID || freshJob.ClaimID == "" || freshJob.ExpectedSourceCommit != source.Commit || freshJob.SourceResolvedCommit != source.Commit || freshJob.PipelineRunID != confirmed.NewRunID || freshJob.CopilotRerunIntentID != confirmed.IntentID {
		t.Fatal("new observed checkout/claim/approval identities do not match fixed confirmed execution")
	}
	evidence, err := retryapproval.ParseEvidence(freshApproval.EvidenceJSON)
	if err != nil {
		t.Fatal(err)
	}
	verifyContainerApprovalEvidence(t, f, evidence)
	f.mu.Lock()
	actualPayload, hasPayload := f.rerunPayloads[freshJob.JobRunID]
	originalTask := f.tasks[originalJob.JobRunID]
	f.mu.Unlock()
	var originalPayload taskpayload.JobRunTaskPayload
	if !hasPayload || json.Unmarshal(originalTask.Payload, &originalPayload) != nil {
		t.Fatal("actual original dispatch or authenticated protected payload observation missing")
	}
	if actualPayload.SourceExpectation == nil || actualPayload.SourceExpectation.ExpectedSourceCommit != source.Commit || actualPayload.Source == nil || originalPayload.Source == nil || actualPayload.Source.Repo != originalPayload.Source.Repo || actualPayload.Source.Branch != originalPayload.Source.Branch || !reflect.DeepEqual(actualPayload.Variables, originalPayload.Variables) || !reflect.DeepEqual(actualPayload.Env, originalPayload.Env) || len(actualPayload.Steps) != len(originalPayload.Steps) {
		t.Fatal("new payload changed frozen source/variables/environment")
	}
	if actualPayload.Runtime == nil || originalPayload.Runtime == nil || actualPayload.Runtime.Type != originalPayload.Runtime.Type || actualPayload.Runtime.Image != originalPayload.Runtime.Image || actualPayload.Runtime.Resources != originalPayload.Runtime.Resources || actualPayload.Runtime.BindingDigest == originalPayload.Runtime.BindingDigest || !reflect.DeepEqual(actualPayload.Retry, originalPayload.Retry) || actualPayload.RetryPlan == nil || originalPayload.RetryPlan == nil || !reflect.DeepEqual(*actualPayload.RetryPlan, evidence.RetryPlan) {
		t.Fatal("new approval does not bind the actual frozen runtime/retry plan")
	}
	oldAttemptIDs := make(map[string]bool)
	for _, step := range originalPayload.Steps {
		for _, attempt := range step.AttemptRunIDs {
			oldAttemptIDs[attempt] = true
		}
	}
	for i, newStep := range actualPayload.Steps {
		for _, attempt := range newStep.AttemptRunIDs {
			if attempt == "" || oldAttemptIDs[attempt] {
				t.Fatal("new execution reused original reserved attempt identity")
			}
		}
		oldStep := originalPayload.Steps[i]
		if newStep.StepRunID == oldStep.StepRunID || newStep.Name != oldStep.Name || newStep.Uses != oldStep.Uses || newStep.Action != oldStep.Action || !reflect.DeepEqual(newStep.Args, oldStep.Args) || !reflect.DeepEqual(newStep.Env, oldStep.Env) {
			t.Fatal("new execution changed frozen step commands/environment or reused identity")
		}
	}

	pending := containerApprovalPending(t, f, freshJob.JobRunID)
	lifecycle.requireNone(t, pending.Record.Resource.ID)
	if counts, _, err := events.counts(pending.Record.Resource); err != nil || counts["exec-start"] != 0 || counts["task-start"] != 0 {
		t.Fatal("pending fresh approval already started execution")
	}
	httpFixture.request(t, f.ctx, policy.Candidates[0], "POST", "/approvals/"+freshApproval.ApprovalID+"/approve", map[string]string{"reason": "first independent terminal rerun review"}, nil)
	first := terminalRerunWaitApproval(t, f, freshJob.JobRunID)
	if first.Status != model.ApprovalStatusPending || first.ApprovedCount != 1 {
		t.Fatal("first candidate bypassed required independent quorum")
	}
	lifecycle.requireNone(t, pending.Record.Resource.ID)
	if counts, _, err := events.counts(pending.Record.Resource); err != nil || counts["exec-start"] != 0 || counts["task-start"] != 0 {
		t.Fatal("pending fresh approval already started execution")
	}
	terminalRerunNoArtifacts(t, f, freshJob.JobRunID)
	steps := make([]map[string]any, 0, len(evidence.RetryPlan.Steps))
	for _, step := range evidence.RetryPlan.Steps {
		steps = append(steps, map[string]any{"name": step.Name, "stepIndex": step.StepIndex, "uses": step.Uses, "action": step.Action, "attemptRunIds": step.AttemptRunIDs})
	}
	oldStepIDs := make([]string, 0, len(originalSteps))
	for _, step := range originalSteps {
		oldStepIDs = append(oldStepIDs, step.StepRunID)
	}
	deadline, _ := f.ctx.Deadline()
	hash := sha256.Sum256([]byte(terminalRerunOutput))
	wantHash := hex.EncodeToString(hash[:])
	contract := map[string]any{"version": "copilot-terminal-rerun-outcome-browser-v1", "scope": scope, "actorId": policy.Candidates[1], "firstApproverId": policy.Candidates[0], "deadlineUnixMilli": deadline.UnixMilli(), "fixedTime": map[string]any{"cutoffUnixMilli": source.Cutoff.UnixMilli(), "originalFinishedAtUnixMilli": originalJob.EndTime.UnixMilli()}, "original": map[string]any{"claimId": originalJob.ClaimID, "approvalId": oldApproval.ApprovalID, "stepRunIds": oldStepIDs}, "target": map[string]any{"requestId": confirmed.RequestID, "intentId": confirmed.IntentID, "runId": confirmed.NewRunID, "jobRunId": confirmed.NewJobRunID, "agentId": freshJob.AgentID, "claimId": freshJob.ClaimID, "approvalId": freshApproval.ApprovalID, "evidenceHash": freshApproval.EvidenceHash, "approvalExpiresAtUnixMilli": freshApproval.ExpiresAt.UnixMilli(), "steps": steps}, "expectedOutput": map[string]any{"name": "proof/result.txt", "size": len(terminalRerunOutput), "sha256": wantHash, "logMarker": "terminal-rerun-command"}}
	var outcome struct {
		BrowserOutcomeComplete bool   `json:"browserOutcomeComplete"`
		NewRunID               string `json:"newRunId"`
		NewJobRunID            string `json:"newJobRunId"`
		ClaimID                string `json:"claimId"`
		ApprovalID             string `json:"approvalId"`
		ArtifactID             string `json:"artifactId"`
		ArtifactSHA256         string `json:"artifactSha256"`
		ApprovalPosts          int    `json:"approvalPosts"`
		ArtifactDownloads      int    `json:"artifactDownloads"`
	}
	store.mu.Lock()
	uploadsBeforeNew := store.sealedUploads
	store.mu.Unlock()
	httpFixture.browser(t, f, "outcome", policy.Candidates[1], contract, &outcome)
	if !outcome.BrowserOutcomeComplete || outcome.NewRunID != confirmed.NewRunID || outcome.NewJobRunID != confirmed.NewJobRunID || outcome.ClaimID != freshJob.ClaimID || outcome.ApprovalID != freshApproval.ApprovalID || outcome.ArtifactSHA256 != wantHash || outcome.ApprovalPosts != 1 || outcome.ArtifactDownloads != 1 {
		t.Fatal("browser outcome receipt differs from actual fixed execution")
	}
	f.waitRun(confirmed.NewRunID, model.PipelineStatusSuccess)
	freshJob = terminalRerunReadJob(t, f, freshJob.JobRunID)
	if freshJob.Status != model.JobRunStatusSuccess || freshJob.CurrentGeneration != 1 || freshJob.TerminalReportDigest == "" || freshJob.SourceResolvedCommit != source.Commit {
		t.Fatal("fresh execution did not complete generation one at captured source")
	}
	lifecycle.requireStarted(t, pending.Record.Resource.ID)
	if counts, _, err := events.counts(pending.Record.Resource); err != nil || counts["create"] != 1 || counts["task-start"] != 1 || counts["exec-create"] != 1 || counts["exec-start"] != 1 || counts["exec-delete"] != 1 {
		t.Fatal("new execution or Confirm replay duplicated actual runtime command calls")
	}
	containerAcceptanceRemoved(t, f, pending)
	containerAcceptanceDrainWithAudit(t, f, retained)
	store.mu.Lock()
	newUploads := store.sealedUploads - uploadsBeforeNew
	store.mu.Unlock()
	if newUploads != 1 {
		t.Fatal("new execution must perform exactly one real S3 upload")
	}
	var votes []model.ApprovalDecision
	if err := f.db.Where("approval_id = ?", freshApproval.ApprovalID).Find(&votes).Error; err != nil || len(votes) != 2 {
		t.Fatal("fresh independent votes missing")
	}
	seen := map[string]bool{}
	for _, vote := range votes {
		if vote.Decision != model.ApprovalDecisionApprove || vote.UserID == actor || seen[vote.UserID] {
			t.Fatal("invalid fresh independent decisions")
		}
		seen[vote.UserID] = true
		for _, old := range oldVotes {
			if old.DecisionID == vote.DecisionID {
				t.Fatal("old approval decision reused")
			}
		}
	}
	for _, candidate := range policy.Candidates {
		if !seen[candidate] {
			t.Fatal("fresh candidate decision missing")
		}
	}
	var artifacts []model.ProjectArtifact
	if err := f.db.Where("job_run_id = ?", freshJob.JobRunID).Find(&artifacts).Error; err != nil || len(artifacts) != 1 {
		t.Fatal("new execution must publish exactly one canonical artifact")
	}
	artifact := artifacts[0]
	if artifact.ArtifactID != outcome.ArtifactID || artifact.ProjectID != f.projectID || artifact.PipelineID != f.pipelineID || artifact.PipelineRunID != freshJob.PipelineRunID || artifact.StepRunID != evidence.RetryPlan.Steps[1].AttemptRunIDs[0] || artifact.Name != "proof/result.txt" || artifact.SHA256 != wantHash || artifact.Size != int64(len(terminalRerunOutput)) || artifact.StorageID != freshJob.ArtifactStorageID || artifact.StorageFingerprint != freshJob.ArtifactStorageFingerprint {
		t.Fatal("canonical artifact lost exact new producer/storage/hash binding")
	}
	expectedKey, keyErr := artifactutil.RemotePath(freshJob.PipelineRunID, freshJob.JobRunID, artifact.StepRunID, wantHash, "proof/result.txt")
	if keyErr != nil || artifact.StoragePath != expectedKey || artifact.ArtifactID != artifactutil.ID(f.projectID, freshJob.PipelineRunID, freshJob.JobRunID, artifact.StepRunID, "proof/result.txt") {
		t.Fatal("output storage key/ID does not use exact new execution namespace")
	}
	newSteps, stepErr := containerApprovalPersistedSteps(f.ctx, f.db, freshJob.JobRunID)
	if stepErr != nil {
		t.Fatal(stepErr)
	}
	active := 0
	for _, step := range newSteps {
		if step.Attempt == 1 {
			if step.Status != model.JobRunStatusSuccess || step.ClaimID == "" || step.TerminalReportDigest == "" || step.GenerationID != freshJob.CurrentGenerationID {
				t.Fatal("new command/output terminal receipt incomplete")
			}
			f.assertReceipt("step", step.StepRunID, step.ClaimID, step.TerminalReportDigest, freshJob.PipelineRunID, freshJob.JobRunID)
			active++
		} else if step.AttemptAdmission != "" || step.ClaimID != "" {
			t.Fatal("new successful generation unexpectedly executed another attempt")
		}
	}
	if active != 2 {
		t.Fatal("new execution did not complete exact shell and output steps")
	}
	f.assertReceipt("job", freshJob.JobRunID, freshJob.ClaimID, freshJob.TerminalReportDigest, freshJob.PipelineRunID, freshJob.JobRunID)
	reader, err := store.S3Storage.DownloadStream(f.ctx, artifact.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	actual, readErr := io.ReadAll(io.LimitReader(reader, int64(len(terminalRerunOutput))+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(actual, []byte(terminalRerunOutput)) {
		t.Fatal("real S3 output differs from immutable expected bytes")
	}
	var gate model.CopilotRerunAdmissionGate
	var schedule model.CopilotRerunSchedule
	if err := f.db.Where("gate_id = ?", freshJob.CopilotRerunGateID).First(&gate).Error; err != nil || gate.State != "claimed" || gate.ClaimID != freshJob.ClaimID || gate.AgentID != freshJob.AgentID || gate.ClaimedAt == nil {
		t.Fatal("new execution lacks exact committed preparation admission")
	}
	if err := f.db.Where("intent_id = ?", confirmed.IntentID).First(&schedule).Error; err != nil || schedule.ClaimedID != freshJob.ClaimID || schedule.RunID != freshJob.PipelineRunID {
		t.Fatal("new execution lacks durable schedule claim")
	}
	f.mu.Lock()
	reference := f.rerunReferences[freshJob.JobRunID]
	f.mu.Unlock()
	if reference.AgentID != freshJob.AgentID || reference.RunID != freshJob.PipelineRunID || reference.JobRunID != freshJob.JobRunID || reference.GateID != gate.GateID || reference.ScheduleID != schedule.ScheduleID || reference.SealID != gate.SealID || reference.SealDigest != gate.SealDigest || reference.PayloadDigest != schedule.PayloadDigest {
		t.Fatal("actual reference-only queue notification differs from fixed seal/admission")
	}
	// The original requester independently observes the same completed output in
	// a fresh real browser login. Reuse the reviewer contract unchanged: its
	// actorId remains the second candidate; this phase selects scope.actorId.
	var ownerOutcome terminalRerunOwnerOutcomeReceipt
	httpFixture.browser(t, f, "owner-outcome", actor, contract, &ownerOutcome)
	wantOwnerOutcome := terminalRerunOwnerOutputBinding{
		Version: "copilot-terminal-rerun-outcome-browser-v1", ActorID: actor, RequesterID: actor,
		ProjectID: f.projectID, PipelineID: f.pipelineID, OriginalRunID: originalRun.RunID, OriginalJobRunID: originalJob.JobRunID,
		OriginalRunStatus: originalRun.Status, OriginalJobStatus: originalJob.Status,
		RequestID: confirmed.RequestID, IntentID: confirmed.IntentID, NewRunID: freshJob.PipelineRunID, NewJobRunID: freshJob.JobRunID,
		NewRunStatus: model.PipelineStatusSuccess, NewJobStatus: freshJob.Status, SourceCommit: source.Commit,
		CommandStepRunID: evidence.RetryPlan.Steps[0].AttemptRunIDs[0], UploadStepRunID: artifact.StepRunID, CommandMarkerObserved: true,
		ArtifactID: artifact.ArtifactID, ArtifactSize: artifact.Size, ArtifactSHA256: artifact.SHA256,
	}
	if !ownerOutcome.validFor(wantOwnerOutcome) {
		t.Fatal("original owner did not read the exact original/new output without mutations")
	}
	// This fresh browser login also replaces the owner's one cached session.
	// Reauthenticate through the real route before the Go Confirm replay.
	httpFixture.login(t, f.ctx, actor)
	// Replay exact Confirm only. It must remain a receipt and cannot publish again.
	f.mu.Lock()
	notifications := f.rerunNotifications[freshJob.JobRunID]
	claims := f.rerunClaimReceipts[freshJob.JobRunID]
	f.mu.Unlock()
	store.mu.Lock()
	uploads := store.sealedUploads
	store.mu.Unlock()
	var replay controlservice.CopilotRerunIntent
	httpFixture.request(t, f.ctx, actor, "POST", "/projects/"+f.projectID+"/copilot/rerun-intents/"+confirmed.IntentID+"/confirm", controlservice.CopilotRerunConfirmRequest{ExpectedVersion: preparation.Prepared.Version, IntentDigest: preparation.Prepared.IntentDigest, ReviewedRunContextDigest: preparation.Prepared.ReviewedRunContextDigest, AcknowledgeNewExecution: true}, &replay)
	if replay.IntentID != confirmed.IntentID || replay.NewRunID != confirmed.NewRunID || replay.NewJobRunID != confirmed.NewJobRunID || !reflect.DeepEqual(replay.ConfirmedAt, confirmed.ConfirmedAt) {
		t.Fatal("Confirm replay changed durable execution receipt")
	}
	auxiliary := terminalRerunCancellationAndRestart(t, f, services, cacheStore, conf, httpFixture, actor, originalJob.JobRunID, snapshot.BindingDigest, publisher, lifecycle, events, retained, reportReadiness)
	// Joining the only publisher closes delayed-submit races. A committed stable
	// Kafka watermark then covers already accepted notifications, including lost ACKs.
	stopCtx, stopCancel := context.WithTimeout(f.ctx, 5*time.Second)
	stopErr := services.StopTerminalRerun(stopCtx)
	stopCancel()
	if stopErr != nil {
		t.Fatal("terminal dispatcher did not join before final replay proof")
	}
	watermark := terminalRerunQueueDrained(t, f)
	containerAcceptanceDrainWithAudit(t, f, retained)
	var scheduleAfter model.CopilotRerunSchedule
	if err := f.db.Where("schedule_id = ?", schedule.ScheduleID).First(&scheduleAfter).Error; err != nil || scheduleAfter.State != "completed" || scheduleAfter.CompletedAt == nil || !reflect.DeepEqual(schedule, scheduleAfter) {
		t.Fatal("Confirm replay or pending publisher changed completed durable schedule")
	}
	f.mu.Lock()
	afterNotifications := f.rerunNotifications[freshJob.JobRunID]
	afterClaims := f.rerunClaimReceipts[freshJob.JobRunID]
	f.mu.Unlock()
	store.mu.Lock()
	afterUploads := store.sealedUploads
	store.mu.Unlock()
	if notifications < 1 || claims < 1 || notifications != afterNotifications || claims != afterClaims || uploads != afterUploads {
		t.Fatal("Confirm replay dispatched or uploaded again")
	}
	lifecycle.requireStarted(t, pending.Record.Resource.ID)
	if counts, _, err := events.counts(pending.Record.Resource); err != nil || counts["create"] != 1 || counts["task-start"] != 1 || counts["exec-create"] != 1 || counts["exec-start"] != 1 || counts["exec-delete"] != 1 {
		t.Fatal("new execution or Confirm replay duplicated actual runtime command calls")
	}
	var gateAfter model.CopilotRerunAdmissionGate
	if err := f.db.Where("gate_id = ?", gate.GateID).First(&gateAfter).Error; err != nil || !reflect.DeepEqual(gate, gateAfter) {
		t.Fatal("Confirm replay changed the committed admission")
	}
	var rereadRun model.PipelineRun
	if err := f.db.Where("run_id = ?", originalRun.RunID).First(&rereadRun).Error; err != nil || !reflect.DeepEqual(*originalRun, rereadRun) {
		t.Fatal("new execution changed the original terminal Run")
	}
	if !reflect.DeepEqual(*originalJob, *terminalRerunReadJob(t, f, originalJob.JobRunID)) {
		t.Fatal("new execution changed the original terminal Job")
	}
	rereadSteps, err := containerApprovalPersistedSteps(f.ctx, f.db, originalJob.JobRunID)
	if err != nil || !reflect.DeepEqual(originalSteps, rereadSteps) {
		t.Fatal("new execution changed original attempt evidence")
	}
	var rereadGenerations []model.JobRunGeneration
	var rereadVotes []model.ApprovalDecision
	var rereadApproval model.ApprovalRequest
	var rereadSnapshot model.CopilotDefinitionSnapshot
	if err := f.db.Where("job_run_id = ?", originalJob.JobRunID).Order("number").Find(&rereadGenerations).Error; err != nil || !reflect.DeepEqual(oldGenerations, rereadGenerations) {
		t.Fatal("original generation evidence changed")
	}
	if err := f.db.Where("approval_id = ?", oldApproval.ApprovalID).Order("decision_id").Find(&rereadVotes).Error; err != nil || !reflect.DeepEqual(oldVotes, rereadVotes) {
		t.Fatal("original decision evidence changed")
	}
	if err := f.db.Where("approval_id = ?", oldApproval.ApprovalID).First(&rereadApproval).Error; err != nil || !reflect.DeepEqual(*oldApproval, rereadApproval) {
		t.Fatal("original approval evidence changed")
	}
	if err := f.db.Where("snapshot_id = ?", snapshot.SnapshotID).First(&rereadSnapshot).Error; err != nil || !reflect.DeepEqual(snapshot, rereadSnapshot) {
		t.Fatal("prospective snapshot changed")
	}
	terminalRerunNoArtifacts(t, f, originalJob.JobRunID)
	targetCounts := terminalRerunAssertTargetCounts(t, f, actor, confirmed, cancelled, auxiliary)
	receipt := map[string]any{"version": "terminal-rerun-runtime-proof-v1", "runtimeAcceptanceComplete": true, "requiresParentTestPass": true, "frontendCommit": os.Getenv("ARCENTRA_COPILOT_BROWSER_FRONTEND_COMMIT"), "projectId": f.projectID, "pipelineId": f.pipelineID, "originalRunId": originalRun.RunID, "originalJobRunId": originalJob.JobRunID, "originalClaimId": originalJob.ClaimID, "originalApprovalId": oldApproval.ApprovalID, "newRunId": freshJob.PipelineRunID, "newJobRunId": freshJob.JobRunID, "newClaimId": freshJob.ClaimID, "newApprovalId": freshApproval.ApprovalID, "intentId": confirmed.IntentID, "requestId": confirmed.RequestID, "gateId": gate.GateID, "scheduleId": schedule.ScheduleID, "sealId": gate.SealID, "sourceCommit": source.Commit, "laterBranchCommit": source.LaterCommit, "definitionRevision": snapshot.DefinitionRevision, "resolvedSpecDigest": snapshot.ResolvedSpecDigest, "snapshotDigest": snapshot.BindingDigest, "cutoffUnixMilli": source.Cutoff.UnixMilli(), "originalFinishedAtUnixMilli": originalJob.EndTime.UnixMilli(), "originalRunStatus": originalRun.Status, "originalJobStatus": originalJob.Status, "newRunStatus": model.PipelineStatusSuccess, "newJobStatus": freshJob.Status, "newGeneration": freshJob.CurrentGeneration, "independentVotes": len(votes), "requesterSelfApprovalRejected": true, "cancelledPreparedIntentId": cancelled.IntentID, "cancelledPreparationCreatedTargets": 0, "artifactId": artifact.ArtifactID, "artifactSize": artifact.Size, "artifactSha256": artifact.SHA256, "actualUploadCount": newUploads, "actualCommandStarts": 1, "confirmReplayStable": true, "dispatcherJoined": true, "scheduleState": scheduleAfter.State, "scheduleCompletedAtUnixMilli": scheduleAfter.CompletedAt.UnixMilli(), "committedQueueWatermark": watermark, "postJoinNotifications": afterNotifications, "postJoinClaimReceipts": afterClaims, "postJoinUploadCount": afterUploads, "originalUnchanged": true, "runtimeAndOutboxDrained": true}
	receipt["auxiliary"] = auxiliary
	receipt["responseRecovery"] = preparation.ResponseRecovery
	receipt["ownerObserved"] = true
	receipt["ownerArtifactDownloads"] = *ownerOutcome.ArtifactDownloads
	receipt["ownerMutationPosts"] = *ownerOutcome.PreparePosts + *ownerOutcome.ConfirmPosts + *ownerOutcome.ApprovalPosts
	receipt["targetCounts"] = targetCounts
	raw, encodeErr := json.Marshal(receipt)
	if encodeErr != nil || len(raw) > 16384 {
		t.Fatal("bounded runtime proof encoding failed")
	}
	output := filepath.Join(os.Getenv("ARCENTRA_COPILOT_BROWSER_EVIDENCE"), "terminal-rerun-runtime-receipt.json")
	file, writeErr := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if writeErr != nil {
		t.Fatal("fresh private runtime proof file required")
	}
	_, writeErr = file.Write(append(raw, '\n'))
	closeErr = file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal("runtime proof write failed")
	}
	t.Log("terminal-rerun-runtime-proof-v1 complete; parent test PASS and final dependency cleanup remain required")
	t.Log("terminal whole_job rerun: authenticated prospective trigger; original failed before fixed UTC cutoff; browser lost Prepare/Confirm responses recovered through reloads and reads; fresh two-person approval; exact captured SHA; new generation-one success and real S3 bytes; original owner independently observed the same output without mutations; original unchanged; exact Confirm replay observed")
}

func terminalRerunReadJob(t *testing.T, f *controllerWorkerFixture, id string) *model.JobRun {
	t.Helper()
	job, err := f.repos.JobRun.GetByJobRunID(f.ctx, id)
	if err != nil || job == nil {
		t.Fatal("actual job unavailable")
	}
	return job
}
func terminalRerunWaitJob(t *testing.T, f *controllerWorkerFixture, run string) *model.JobRun {
	t.Helper()
	var job *model.JobRun
	f.wait("actual manual-trigger job", func() bool {
		jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run)
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) > 1 {
			t.Fatal("terminal fixture requires exactly one job")
		}
		if len(jobs) == 1 {
			job = jobs[0]
			return true
		}
		return false
	})
	return job
}
func terminalRerunWaitApproval(t *testing.T, f *controllerWorkerFixture, job string) *model.ApprovalRequest {
	t.Helper()
	var row model.ApprovalRequest
	f.wait("actual current job approval", func() bool {
		var rows []model.ApprovalRequest
		if err := f.db.Where("retry_job_run_id = ?", job).Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) > 1 {
			t.Fatal("more than one actual job approval")
		}
		if len(rows) == 1 {
			row = rows[0]
			return true
		}
		return false
	})
	if row.ExpiresAt == nil {
		t.Fatal("approval deadline missing")
	}
	return &row
}
func terminalRerunNoArtifacts(t *testing.T, f *controllerWorkerFixture, job string) {
	t.Helper()
	for _, table := range []any{&model.ProjectArtifact{}, &model.StepRunArtifact{}} {
		var count int64
		if err := f.db.Model(table).Where("job_run_id = ?", job).Count(&count).Error; err != nil || count != 0 {
			t.Fatal(fmt.Sprintf("unexpected output evidence for %T", table))
		}
	}
}

// Called only after the terminal publisher has been cancelled and joined.
func terminalRerunQueueDrained(t *testing.T, f *controllerWorkerFixture) int64 {
	t.Helper()
	consumer, err := ckafka.NewConsumer(&ckafka.ConfigMap{"bootstrap.servers": f.conf.MessageQueue.Kafka.BootstrapServers, "group.id": "arcentra-agent-" + f.conf.Agent.ID, "enable.auto.commit": false})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	topic := strings.ToUpper(taskpayload.AgentQueue(f.conf.Agent.ID))
	_, watermark, err := consumer.QueryWatermarkOffsets(topic, 0, 3000)
	if err != nil || watermark < 1 {
		t.Fatal("actual terminal task watermark unavailable")
	}
	f.wait("all accepted terminal notifications committed", func() bool {
		positions, err := consumer.Committed([]ckafka.TopicPartition{{Topic: &topic, Partition: 0}}, 1000)
		return err == nil && len(positions) == 1 && positions[0].Error == nil && int64(positions[0].Offset) == watermark
	})
	_, final, err := consumer.QueryWatermarkOffsets(topic, 0, 3000)
	if err != nil || final != watermark {
		t.Fatal("task publication continued after dispatcher join")
	}
	return watermark
}
