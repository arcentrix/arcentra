// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCopilotExecutionServiceDefaultOffAndConsent(t *testing.T) {
	s, db, p, c, _ := historyFixture(t)
	require.NoError(t, db.AutoMigrate(&model.CopilotExecutionIntent{}, &model.ApprovalDecision{}))
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	prepare := CopilotExecutionPrepareRequest{RequestID: "execution-request-123456", ExpectedVersion: 1, PlanDigest: "digest", AcknowledgeDetachedIntent: true}
	confirm := CopilotExecutionConfirmRequest{ExpectedVersion: 1, IntentDigest: "digest", AcknowledgePermanentApproval: true}
	_, err := s.PrepareRetryExecution(ctx, owner, "p", c.ConversationID, "plan", prepare)
	require.ErrorIs(t, err, ErrCopilotHistoryUnavailable)
	_, err = s.ConfirmRetryExecution(ctx, owner, "p", "intent", confirm)
	require.ErrorIs(t, err, ErrCopilotHistoryUnavailable)
	_, err = s.CancelRetryExecution(ctx, owner, "p", "intent", CopilotExecutionCancelRequest{ExpectedVersion: 1, IntentDigest: "digest"})
	require.ErrorIs(t, err, ErrCopilotHistoryUnavailable)
	_, err = s.GetRetryExecution(ctx, owner, "p", "intent")
	require.ErrorIs(t, err, ErrCopilotHistoryUnavailable)
	_, err = s.ListRetryExecutions(ctx, owner, "p", 0, 20)
	require.ErrorIs(t, err, ErrCopilotHistoryUnavailable)
	s.ConfigureRetryExecution(copilot.ExecutionConfig{RetryApprovalEnabled: true})
	_, err = s.ListRetryExecutions(ctx, authz.Subject{UserID: "owner", IsAgent: true}, "p", 0, 20)
	require.ErrorIs(t, err, ErrCopilotDenied)
	prepare.AcknowledgeDetachedIntent = false
	_, err = s.PrepareRetryExecution(ctx, owner, "p", c.ConversationID, "plan", prepare)
	require.ErrorIs(t, err, ErrCopilotInput)
	confirm.AcknowledgePermanentApproval = false
	_, err = s.ConfirmRetryExecution(ctx, owner, "p", "intent", confirm)
	require.ErrorIs(t, err, ErrCopilotInput)
	for _, entity := range []any{&model.CopilotExecutionIntent{}, &model.ApprovalDecision{}} {
		var count int64
		require.NoError(t, db.Model(entity).Count(&count).Error)
		require.Zero(t, count)
	}
	require.Zero(t, p.count(), "validation must not invoke a provider")
}

func TestCopilotExecutionServiceOwnerIsolationBoundedListAndLiveRevocation(t *testing.T) {
	s, db, p, _, _ := historyFixture(t)
	require.NoError(t, db.AutoMigrate(&model.CopilotExecutionIntent{}))
	s.ConfigureRetryExecution(copilot.ExecutionConfig{RetryApprovalEnabled: true})
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	// Deliberately unreadable foreign metadata must neither leak nor poison the
	// owner's list. This seed tests isolation only, not execution authorization.
	require.NoError(t, db.Create(&model.CopilotExecutionIntent{IntentID: "foreign-intent", OwnerUserID: "foreign-owner", ProjectID: "p", RequestID: "foreign-request", DecisionID: "foreign-decision", State: "prepared", Version: 1, ExpiresAt: time.Now().Add(time.Hour)}).Error)
	_, err := s.GetRetryExecution(ctx, owner, "p", "foreign-intent")
	require.ErrorIs(t, err, ErrCopilotNotFound)
	_, err = s.CancelRetryExecution(ctx, owner, "p", "foreign-intent", CopilotExecutionCancelRequest{ExpectedVersion: 1, IntentDigest: "forged"})
	require.ErrorIs(t, err, ErrCopilotNotFound)
	_, err = s.ConfirmRetryExecution(ctx, owner, "p", "foreign-intent", CopilotExecutionConfirmRequest{ExpectedVersion: 1, IntentDigest: "forged", AcknowledgePermanentApproval: true})
	require.ErrorIs(t, err, ErrCopilotNotFound)
	for _, limit := range []int{-1, 21, 1000} {
		_, err = s.ListRetryExecutions(ctx, owner, "p", 0, limit)
		require.ErrorIs(t, err, ErrCopilotInput)
	}
	for _, limit := range []int{0, 1, 20} {
		list, e := s.ListRetryExecutions(ctx, owner, "p", 0, limit)
		require.NoError(t, e)
		require.Empty(t, list.Intents)
	}
	_, err = s.ListRetryExecutions(ctx, owner, "other-project", 0, 20)
	require.Error(t, err)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error)
	_, err = s.ListRetryExecutions(ctx, owner, "p", 0, 20)
	require.ErrorIs(t, err, ErrCopilotDenied)
	_, err = s.GetRetryExecution(ctx, owner, "p", "foreign-intent")
	require.ErrorIs(t, err, ErrCopilotNotFound) // Foreign identity stays undisclosed after revocation.
	require.Zero(t, p.count())
}

func TestCopilotExecutionServiceMetadataWireAllowlist(t *testing.T) {
	// DTO metadata has an explicit wire schema; no persistence model or private
	// history payload is embedded. Full review content is tested in retry suites.
	value := CopilotExecutionIntent{IntentID: "intent", RequestID: "request", ProjectID: "project", PipelineID: "pipeline", RunID: "run", JobRunID: "job", ApprovalID: "approval", SourcePlanID: "plan", SourcePlanVersion: 2, SourcePlanDigest: "digest", IntentDigest: "digest", Version: 1, State: "prepared", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &wire))
	allowed := map[string]bool{}
	for _, key := range strings.Fields("intentId requestId projectId pipelineId runId jobRunId approvalId sourcePlanId sourcePlanVersion sourcePlanDigest intentDigest version state createdAt expiresAt submittedAt decisionId approvalStatus approvedCount requiredApproverCount jobStatus currentGeneration runtimeAdmissionObserved retryReview") {
		allowed[key] = true
	}
	for key := range wire {
		require.True(t, allowed[key], key)
	}
	for _, forbidden := range []string{"ownerUserId", "conversationId", "payloadCiphertext", "question", "prompt", "environment", "secretValue", "providerResponse", "evidenceJSON"} {
		require.NotContains(t, string(raw), forbidden)
	}
	raw, err = json.Marshal(&model.CopilotExecutionIntent{OwnerUserID: "PRIVATE_OWNER", PlanDigest: "PRIVATE_BINDING"})
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(raw))
	s := &CopilotService{}
	_, err = s.executionDTO(context.Background(), authz.Subject{UserID: "owner"}, &model.CopilotExecutionIntent{OwnerUserID: "other"})
	require.ErrorIs(t, err, ErrCopilotDenied)
}

func TestCopilotExecutionServiceReadCancelMetadataAndRevocation(t *testing.T) {
	s, db, p, c, _ := historyFixture(t)
	require.NoError(t, db.AutoMigrate(&model.CopilotExecutionIntent{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}))
	s.ConfigureRetryExecution(copilot.ExecutionConfig{RetryApprovalEnabled: true})
	// Synthetic read-only receipt fixture. It does not establish valid approval
	// evidence or an Agent execution; confirms are deliberately not exercised.
	now := time.Now().UTC().Truncate(time.Millisecond)
	row := model.CopilotExecutionIntent{IntentID: "readable-intent", OwnerUserID: "owner", ProjectID: "p", PipelineID: "pipe-run", RunID: "run", JobRunID: "job", ApprovalID: "readable-approval", RequestID: "readable-request", ConversationID: c.ConversationID, PlanID: "plan", PlanVersion: 2, PlanDigest: "opaque-plan", EvidenceHash: "opaque-evidence", EvidenceDigest: "opaque-evidence", DecisionID: "stable-decision", State: "prepared", Version: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}
	binding, err := json.Marshal([]any{row.IntentID, row.OwnerUserID, row.ProjectID, row.RequestID, row.PipelineID, row.RunID, row.JobRunID, row.ApprovalID, row.ConversationID, row.PlanID, row.PlanVersion, row.PlanDigest, row.EvidenceHash, row.EvidenceDigest, row.DecisionID, row.ExpiresAt.UnixMilli()})
	require.NoError(t, err)
	sum := sha256.Sum256(binding)
	row.IntentDigest = hex.EncodeToString(sum[:])
	require.NoError(t, db.Create(&row).Error)
	jobID := "job"
	require.NoError(t, db.Create(&model.ApprovalRequest{ApprovalID: row.ApprovalID, PipelineRunID: "run", RetryJobRunID: &jobID, EvidenceHash: row.EvidenceHash, EvidenceJSON: "SENSITIVE_RAW_EVIDENCE_CANARY", Status: model.ApprovalStatusPending, RequiredApproverCount: 2}).Error)
	require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"retry_approval_id": row.ApprovalID, "retry_approval_evidence_hash": row.EvidenceHash, "current_generation_id": ""}).Error)
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	value, err := s.GetRetryExecution(ctx, owner, "p", row.IntentID)
	require.NoError(t, err)
	require.Equal(t, "prepared", value.State)
	require.Empty(t, value.DecisionID)
	require.Nil(t, value.RetryReview)
	require.NotNil(t, value.RuntimeAdmissionObserved)
	require.False(t, *value.RuntimeAdmissionObserved)
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	for _, canary := range []string{"SENSITIVE_RAW_EVIDENCE_CANARY", "registered-old-key", "provider-secret", "payloadCiphertext", "question", "ownerUserId"} {
		require.NotContains(t, string(raw), canary)
	}
	list, err := s.ListRetryExecutions(ctx, owner, "p", 0, 1)
	require.NoError(t, err)
	require.Len(t, list.Intents, 1)
	cancelled, err := s.CancelRetryExecution(ctx, owner, "p", row.IntentID, CopilotExecutionCancelRequest{ExpectedVersion: row.Version, IntentDigest: row.IntentDigest})
	require.NoError(t, err)
	require.Equal(t, "cancelled", cancelled.State)
	var votes int64
	require.NoError(t, db.Model(&model.ApprovalDecision{}).Count(&votes).Error)
	require.Zero(t, votes)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error)
	_, err = s.GetRetryExecution(ctx, owner, "p", row.IntentID)
	require.ErrorIs(t, err, ErrCopilotDenied)
	require.Zero(t, p.count())
}

func executionAdmissionEvidenceFixture(t *testing.T) *retryapproval.Evidence {
	t.Helper()
	p := retryapproval.Policy{ID: "policy", Candidates: []string{"voter"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300}
	p.Fingerprint, _ = p.Digest()
	c := retryapproval.RunContext{Version: 1, ProjectID: "project", PipelineID: "pipeline", PipelineRunID: "run", ActorID: "actor", DefinitionCommitSHA: strings.Repeat("a", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: "v1:" + strings.Repeat("b", 64), GatedJobs: []string{"build"}, Policy: p}
	r := taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: "example/image@sha256:" + strings.Repeat("a", 64), Resources: taskqueue.RuntimeResources{CPU: "1", Memory: "128Mi"}}
	r.BindingDigest, _ = taskqueue.RuntimeBindingDigest("pipeline", "run", "job", "agent", &r)
	id := taskqueue.JobInputIdentity{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", ClaimID: "claim", RuntimeBindingDigest: r.BindingDigest}
	payload := &taskqueue.JobRunTaskPayload{RetryApprovalRequired: true, RetryApprovalTimeoutSeconds: 300, PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", JobName: "build", Runtime: &r, Retry: &pipelinev1.Retry{Mode: "whole_job", Idempotent: true, MaxAttempts: 2, AllowedExitCodes: []int32{1}}, Steps: []taskqueue.StepPayload{{Name: "step", StepRunID: "step1", AttemptRunIDs: []string{"step1", "step2"}, Uses: "shell", Args: map[string]any{"script": "SECRET_COMMAND"}, Env: map[string]string{"TOKEN": "SECRET_TOKEN"}}}}
	plan, err := taskqueue.NewJobRetryPlan(payload)
	if err != nil {
		t.Fatal(err)
	}
	pd, _ := plan.Digest("pipeline", "run", "job", "agent", r.BindingDigest)
	m, err := taskqueue.NewJobInputManifest("", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	md, _ := m.Digest(id)
	return &retryapproval.Evidence{Version: 1, Kind: retryapproval.Kind, ApprovalID: "approval", Context: c, Identity: id, JobName: "build", RetryPlan: *plan, RetryPlanDigest: pd, Runtime: r, Inputs: *m, InputManifestDigest: md, Artifacts: []retryapproval.Artifact{}, CreatedAtUnixMilli: 1000, ExpiresAtUnixMilli: 301000}
}

func TestCopilotExecutionAdmissionMatchesCompleteFrozenBinding(t *testing.T) {
	e := executionAdmissionEvidenceFixture(t)
	frozen, err := e.Canonical()
	require.NoError(t, err)
	e, err = retryapproval.ParseEvidence(frozen)
	require.NoError(t, err)
	evidenceDigest, err := e.Digest()
	require.NoError(t, err)
	row := model.CopilotExecutionIntent{ApprovalID: e.ApprovalID, PipelineID: e.Identity.PipelineID, RunID: e.Identity.PipelineRunID, JobRunID: e.Identity.JobRunID, EvidenceHash: evidenceDigest, EvidenceDigest: evidenceDigest}
	review := retryapproval.Review{ApprovalID: e.ApprovalID, EvidenceHash: evidenceDigest, Evidence: e}
	baseG := taskqueue.JobGeneration{Version: 1, PipelineID: e.Identity.PipelineID, PipelineRunID: e.Identity.PipelineRunID, JobRunID: e.Identity.JobRunID, AgentID: e.Identity.AgentID, OriginalClaimID: e.Identity.ClaimID, RuntimeBindingDigest: e.Identity.RuntimeBindingDigest, SourceBindingDigest: e.Identity.SourceBindingDigest, InputManifestDigest: e.InputManifestDigest, RetryPlanDigest: e.RetryPlanDigest, GenerationID: uuid.NewString(), RuntimeClaimID: uuid.NewString(), Number: 1}
	baseG.BindingDigest, err = baseG.Digest()
	require.NoError(t, err)
	baseA := retryapproval.Admission{Version: 1, ApprovalID: e.ApprovalID, EvidenceHash: evidenceDigest, AdmittedAtUnixMilli: 2000, ExpiresAtUnixMilli: e.ExpiresAtUnixMilli, Request: retryapproval.Request{Version: 1, Operation: "admit", Identity: e.Identity, GenerationID: baseG.GenerationID, GenerationBindingDigest: baseG.BindingDigest, RetryPlanDigest: e.RetryPlanDigest, InputManifestDigest: e.InputManifestDigest}}
	// Recanonicalize and digest every altered receipt, so a failure demonstrates
	// cross-record binding validation rather than a stale checksum rejection.
	matches := func(t *testing.T, a retryapproval.Admission, g taskqueue.JobGeneration) bool {
		t.Helper()
		g.BindingDigest, err = g.Digest()
		require.NoError(t, err)
		encodedG, err := g.Canonical()
		require.NoError(t, err)
		parsedG, err := taskqueue.ParseJobGeneration(encodedG)
		require.NoError(t, err)
		a.Request.GenerationBindingDigest = g.BindingDigest
		encodedA, err := a.Canonical()
		require.NoError(t, err)
		parsedA, err := retryapproval.ParseAdmission(encodedA)
		require.NoError(t, err)
		digest, err := parsedA.Digest()
		require.NoError(t, err)
		generation := model.JobRunGeneration{GenerationID: g.GenerationID, Number: g.Number, GenerationJSON: encodedG, RetryApprovalAdmission: encodedA, RetryApprovalAdmissionDigest: digest}
		return executionAdmissionMatches(&row, &generation, parsedA, parsedG, &review)
	}
	require.True(t, matches(t, baseA, baseG))
	bootstrap := baseG
	bootstrap.InputManifestDigest = ""
	require.True(t, matches(t, baseA, bootstrap), "first generation keeps its pre-pin descriptor")
	later := bootstrap
	later.Number = 2
	later.PreviousGenerationID = uuid.NewString()
	later.BindingDigest, err = later.Digest()
	require.NoError(t, err)
	// Parsing a later empty-input generation fails before the helper in production.
	require.Error(t, later.Validate())
	laterAdmission := baseA
	laterAdmission.Request.GenerationBindingDigest = later.BindingDigest
	laterDigest, err := laterAdmission.Digest()
	require.NoError(t, err)
	require.False(t, executionAdmissionMatches(&row, &model.JobRunGeneration{GenerationID: later.GenerationID, Number: later.Number, RetryApprovalAdmissionDigest: laterDigest}, &laterAdmission, &later, &review))
	alternateDigest := "v1:" + strings.Repeat("f", 64)
	for name, mutate := range map[string]func(*retryapproval.Admission){
		"receipt-agent":          func(a *retryapproval.Admission) { a.Request.Identity.AgentID = "other-agent" },
		"receipt-claim":          func(a *retryapproval.Admission) { a.Request.Identity.ClaimID = "other-claim" },
		"receipt-runtime":        func(a *retryapproval.Admission) { a.Request.Identity.RuntimeBindingDigest = alternateDigest },
		"receipt-source":         func(a *retryapproval.Admission) { a.Request.Identity.SourceBindingDigest = alternateDigest },
		"receipt-input-manifest": func(a *retryapproval.Admission) { a.Request.InputManifestDigest = alternateDigest },
		"receipt-retry-plan":     func(a *retryapproval.Admission) { a.Request.RetryPlanDigest = alternateDigest },
		"receipt-expiry":         func(a *retryapproval.Admission) { a.ExpiresAtUnixMilli++ },
	} {
		t.Run(name, func(t *testing.T) { a := baseA; mutate(&a); require.False(t, matches(t, a, baseG)) })
	}
	for name, mutate := range map[string]func(*taskqueue.JobGeneration){
		"generation-agent":          func(g *taskqueue.JobGeneration) { g.AgentID = "other-agent" },
		"generation-claim":          func(g *taskqueue.JobGeneration) { g.OriginalClaimID = "other-claim" },
		"generation-runtime":        func(g *taskqueue.JobGeneration) { g.RuntimeBindingDigest = alternateDigest },
		"generation-source":         func(g *taskqueue.JobGeneration) { g.SourceBindingDigest = alternateDigest },
		"generation-input-manifest": func(g *taskqueue.JobGeneration) { g.InputManifestDigest = alternateDigest },
		"generation-retry-plan":     func(g *taskqueue.JobGeneration) { g.RetryPlanDigest = alternateDigest },
	} {
		t.Run(name, func(t *testing.T) { g := baseG; mutate(&g); require.False(t, matches(t, baseA, g)) })
	}
	require.False(t, executionAdmissionMatches(nil, nil, nil, nil, nil))
}

func TestCopilotExecutionPayloadGuardUsesCurrentSecretsInReview(t *testing.T) {
	s, db, _, _, _ := historyFixture(t)
	row := &model.CopilotExecutionIntent{OwnerUserID: "owner", ProjectID: "p", PipelineID: "pipe-run", RunID: "run", JobRunID: "job"}
	e := executionAdmissionEvidenceFixture(t)
	out := &CopilotExecutionIntent{IntentID: "safe-intent", RetryReview: &retryapproval.Review{Evidence: e}}
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	require.NoError(t, s.executionPayloadGuard(ctx, owner, row, out))
	e.JobName = "registered-old-key"
	require.ErrorIs(t, s.executionPayloadGuard(ctx, owner, row, out), ErrCopilotSecrets)
	e.JobName = "safe-name"
	require.NoError(t, s.executionPayloadGuard(ctx, owner, row, out))
	// A secret newly present in the target job's environment must guard already
	// frozen review strings at response time, not just when a plan was prepared.
	require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("env", `{"TOKEN":"newly-rotated-secret-value"}`).Error)
	e.Context.DefinitionPath = "newly-rotated-secret-value"
	require.ErrorIs(t, s.executionPayloadGuard(ctx, owner, row, out), ErrCopilotSecrets)
}
