//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
)

// Only the text model is synthetic and local. It has no network, credentials,
// tools or runtime adapters. Its evidence is the producer's actual registered
// S3 object; the surrounding test still requires real Process/Kafka/TLS/MySQL/
// containerd/S3 execution and all its original exact-count witnesses.
type containerCopilotModel struct {
	project        string
	artifactID     string
	expectedPrompt string
	calls          int
}

func (m *containerCopilotModel) Options(project string) []copilot.ModelOption {
	if project != m.project {
		return nil
	}
	return []copilot.ModelOption{{ProviderID: "local-acceptance", Model: "bounded-review"}}
}
func (m *containerCopilotModel) Resolve(project, provider, name string) (copilot.Provider, error) {
	if project != m.project || provider != "local-acceptance" || name != "bounded-review" {
		return nil, copilot.ErrUnconfigured
	}
	return m, nil
}
func (m *containerCopilotModel) Secrets() []string { return nil }
func (m *containerCopilotModel) Binding(project, provider, name string) (string, error) {
	if _, err := m.Resolve(project, provider, name); err != nil {
		return "", err
	}
	return "local-acceptance-model-v1", nil
}
func (m *containerCopilotModel) Analyze(ctx context.Context, in copilot.Input) (copilot.Output, error) {
	if err := ctx.Err(); err != nil {
		return copilot.Output{}, err
	}
	if m.artifactID == "" || m.expectedPrompt == "" || in.User != m.expectedPrompt || !strings.Contains(in.User, containerCopilotArtifactBlock(m.artifactID)) || strings.Count(in.User, "<evidence ") != 1 {
		return copilot.Output{}, copilot.ErrInvalidResponse
	}
	m.calls++
	return copilot.Output{Text: "The selected producer artifact contains approval-input. Review the separately bound finite retry budget before explicitly approving it; this text does not execute anything."}, nil
}

type containerCopilotExecution struct {
	service    *controlservice.CopilotService
	subjects   []authz.Subject
	intents    []controlservice.CopilotExecutionIntent
	confirms   []controlservice.CopilotExecutionConfirmRequest
	decisions  []string
	approvalID string
	jobID      string
}

func prepareContainerCopilotExecution(t *testing.T, f *controllerWorkerFixture, services *controlservice.Services, approval *model.ApprovalRequest, requester string, policy *retryapproval.Policy) *containerCopilotExecution {
	t.Helper()
	// Disposable synthetic managed-key fixture, restored by testing.T. It is never
	// emitted, sent to a model, or substituted into the production configuration.
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")))
	authorizer, err := authz.NewLiveAuthorizer(f.ctx, f.repos)
	if err != nil {
		t.Fatal(err)
	}
	localModel := &containerCopilotModel{project: f.projectID}
	service := controlservice.NewCopilotService(f.repos, authorizer, services.Artifact, localModel)
	if err := service.ConfigureHistory(copilot.HistoryConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	service.ConfigureRetryExecution(copilot.ExecutionConfig{RetryApprovalEnabled: true})
	session := &containerCopilotExecution{service: service, approvalID: approval.ApprovalID, jobID: *approval.RetryJobRunID}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, entity := range []any{&model.CopilotExecutionIntent{}, &model.CopilotActionPlan{}, &model.CopilotTurn{}, &model.CopilotConversation{}} {
			if err := f.db.WithContext(ctx).Where("project_id = ? AND owner_user_id IN ?", f.projectID, policy.Candidates).Delete(entity).Error; err != nil {
				t.Error(err)
			}
		}
	})
	artifacts, total, err := f.repos.Artifact.List(f.ctx, repo.ArtifactQuery{ProjectID: f.projectID, RunID: approval.PipelineRunID, Limit: 100})
	if err != nil || total != 1 || len(artifacts) != 1 {
		t.Fatalf("actual producer context: rows=%d total=%d err=%v", len(artifacts), total, err)
	}
	review, err := services.Approval.RetryReview(f.ctx, approval)
	if err != nil || review == nil || review.Evidence == nil || review.EvidenceHash != approval.EvidenceHash || review.Evidence.Context.ProjectID != f.projectID || review.Evidence.Context.PipelineID != f.pipelineID || review.Evidence.Context.PipelineRunID != approval.PipelineRunID {
		t.Fatalf("exact approved producer evidence unavailable: %v", err)
	}
	producer, err := f.repos.JobRun.GetByJobRunID(f.ctx, artifacts[0].JobRunID)
	if err != nil {
		t.Fatal(err)
	}
	producerStep, err := f.repos.StepRun.GetByStepRunID(f.ctx, artifacts[0].StepRunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := containerCopilotArtifactBinding(&artifacts[0], producer, producerStep, review.Evidence); err != nil {
		t.Fatal(err)
	}
	localModel.artifactID = artifacts[0].ArtifactID
	for index, candidate := range policy.Candidates {
		if candidate == requester {
			t.Fatal("candidate must own an independent private conversation")
		}
		subject := authz.Subject{UserID: candidate}
		conversation, err := service.CreateConversation(f.ctx, subject, f.projectID, controlservice.CopilotCreateConversationRequest{RunID: approval.PipelineRunID, RequestID: id.GetUild(), AcknowledgeRetention: true})
		if err != nil {
			t.Fatalf("candidate conversation: %v", err)
		}
		if _, err := service.GetConversation(f.ctx, authz.Subject{UserID: requester}, f.projectID, conversation.ConversationID, 0, 20); err == nil {
			t.Fatal("requester read another candidate's private conversation")
		}
		request := controlservice.CopilotHistoryTurnRequest{CopilotRequest: controlservice.CopilotRequest{RequestID: id.GetUild(), RunID: approval.PipelineRunID, ProviderID: "local-acceptance", Model: "bounded-review", Question: "Explain the selected producer input before I review the bounded execution and retry budget.", Context: controlservice.CopilotSelection{ArtifactIDs: []string{artifacts[0].ArtifactID}}, ShareSelectedContext: true}}
		preview, err := service.PreviewConversationTurn(f.ctx, subject, f.projectID, conversation.ConversationID, request)
		if err != nil {
			t.Fatalf("actual producer preview: %v", err)
		}
		if err := containerCopilotContextEvidence(preview.Evidence, &artifacts[0]); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(preview.Text, containerCopilotArtifactBlock(artifacts[0].ArtifactID)) || strings.Count(preview.Text, "<evidence ") != 1 {
			t.Fatal("preview changed actual registered artifact identity or bytes")
		}
		localModel.expectedPrompt = preview.Text
		request.ContextDigest = preview.Digest
		turn, err := service.SendConversationTurn(f.ctx, subject, f.projectID, conversation.ConversationID, request)
		if err != nil || turn.State != "completed" || !turn.ContentAvailable || len(turn.Evidence) != 1 || turn.Evidence[0].ID != artifacts[0].ArtifactID {
			t.Fatalf("durable model turn did not retain actual artifact evidence: state=%s err=%v", turn.State, err)
		}
		if err := containerCopilotContextEvidence(turn.Evidence, &artifacts[0]); err != nil {
			t.Fatal(err)
		}
		var stored model.CopilotTurn
		if err := f.db.Where("turn_id = ?", turn.TurnID).First(&stored).Error; err != nil || len(stored.PayloadCiphertext) == 0 || strings.Contains(string(stored.PayloadCiphertext), "approval-input") {
			t.Fatal("private turn was not encrypted")
		}
		plan, err := service.CreateConversationPlan(f.ctx, subject, f.projectID, conversation.ConversationID, controlservice.CopilotPlanRequest{RequestID: id.GetUild(), TurnID: turn.TurnID, Kind: "retry_approval_review", RetryApproval: &controlservice.CopilotRetryReviewInput{ApprovalID: approval.ApprovalID}})
		if err != nil {
			t.Fatalf("create bound review: %v", err)
		}
		if plan.Payload == nil || plan.Payload.RetryApproval == nil {
			t.Fatal("typed retry review payload missing")
		}
		target := plan.Payload.RetryApproval
		if target.ApprovalID != approval.ApprovalID || target.JobRunID != session.jobID || target.EvidenceDigest != review.EvidenceHash || target.RetryPlanDigest != review.Evidence.RetryPlanDigest || target.InputManifestDigest != review.Evidence.InputManifestDigest || target.SourceBindingDigest != review.Evidence.Identity.SourceBindingDigest {
			t.Fatal("typed plan changed frozen approval binding")
		}
		plan, err = service.ReviewConversationPlan(f.ctx, subject, f.projectID, conversation.ConversationID, plan.PlanID, controlservice.CopilotPlanReviewRequest{ExpectedVersion: plan.Version, PlanDigest: plan.PlanDigest, AcknowledgeNotExecuted: true})
		if err != nil || plan.State != "reviewed" || !plan.Eligible {
			t.Fatalf("review bound finite plan: %v", err)
		}
		prepare := func() controlservice.CopilotExecutionIntent {
			intent, err := service.PrepareRetryExecution(f.ctx, subject, f.projectID, conversation.ConversationID, plan.PlanID, controlservice.CopilotExecutionPrepareRequest{RequestID: id.GetUild(), ExpectedVersion: plan.Version, PlanDigest: plan.PlanDigest, AcknowledgeDetachedIntent: true})
			if err != nil || intent.State != "prepared" || intent.JobRunID != session.jobID || intent.ApprovalID != approval.ApprovalID || intent.SourcePlanDigest != plan.PlanDigest {
				t.Fatalf("prepare detached bound intent: %v", err)
			}
			return intent
		}
		if index == 0 {
			cancelled := prepare()
			cancelled, err = service.CancelRetryExecution(f.ctx, subject, f.projectID, cancelled.IntentID, controlservice.CopilotExecutionCancelRequest{ExpectedVersion: cancelled.Version, IntentDigest: cancelled.IntentDigest})
			if err != nil || cancelled.State != "cancelled" {
				t.Fatalf("cancel prepared intent: %v", err)
			}
			if _, err := service.ConfirmRetryExecution(f.ctx, subject, f.projectID, cancelled.IntentID, controlservice.CopilotExecutionConfirmRequest{ExpectedVersion: cancelled.Version, IntentDigest: cancelled.IntentDigest, AcknowledgePermanentApproval: true}); err == nil {
				t.Fatal("cancelled intent recorded a vote")
			}
		}
		intent := prepare()
		if index == 0 {
			verifyContainerCopilotReadRevocation(t, f, service, subject, intent)
		}
		session.subjects = append(session.subjects, subject)
		session.intents = append(session.intents, intent)
		session.confirms = append(session.confirms, controlservice.CopilotExecutionConfirmRequest{ExpectedVersion: intent.Version, IntentDigest: intent.IntentDigest, AcknowledgePermanentApproval: true})
		session.decisions = append(session.decisions, "")
		// Deletion hides encrypted source history but cannot cancel an acknowledged,
		// detached intent or retract a future permanent domain decision.
		if index == 0 {
			current, err := service.GetConversation(f.ctx, subject, f.projectID, conversation.ConversationID, 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.DeleteConversation(f.ctx, subject, f.projectID, conversation.ConversationID, current.Conversation.Version); err != nil {
				t.Fatal(err)
			}
			if _, err := service.GetConversation(f.ctx, subject, f.projectID, conversation.ConversationID, 0, 20); err == nil {
				t.Fatal("deleted source history remains readable")
			}
			detached, err := service.GetRetryExecution(f.ctx, subject, f.projectID, intent.IntentID)
			if err != nil || detached.State != "prepared" {
				t.Fatalf("source deletion cancelled detached intent: %v", err)
			}
		}
	}
	if localModel.calls != 2 {
		t.Fatalf("local text model calls=%d want=2", localModel.calls)
	}
	var votes int64
	if err := f.db.Model(&model.ApprovalDecision{}).Where("approval_id = ?", approval.ApprovalID).Count(&votes).Error; err != nil || votes != 0 {
		t.Fatalf("conversation/review/prepare/cancel recorded votes=%d err=%v", votes, err)
	}
	return session
}

func (s *containerCopilotExecution) confirm(t *testing.T, f *controllerWorkerFixture, index int) {
	t.Helper()
	intent, err := s.service.ConfirmRetryExecution(f.ctx, s.subjects[index], f.projectID, s.intents[index].IntentID, s.confirms[index])
	if err != nil || intent.State != "submitted" || intent.DecisionID == "" {
		t.Fatalf("explicit candidate confirmation: %v", err)
	}
	if s.decisions[index] != "" && s.decisions[index] != intent.DecisionID {
		t.Fatal("duplicate confirmation changed permanent decision")
	}
	s.decisions[index] = intent.DecisionID
	var vote model.ApprovalDecision
	if err := f.db.Where("decision_id = ?", intent.DecisionID).First(&vote).Error; err != nil || vote.UserID != s.subjects[index].UserID || vote.ApprovalID != s.approvalID || vote.Decision != model.ApprovalDecisionApprove {
		t.Fatal("Copilot confirmation was not the authenticated candidate's exact domain vote")
	}
	if index == 0 && (intent.ApprovalStatus == nil || *intent.ApprovalStatus != model.ApprovalStatusPending || intent.ApprovedCount == nil || *intent.ApprovedCount != 1 || intent.RuntimeAdmissionObserved == nil || *intent.RuntimeAdmissionObserved) {
		t.Fatal("first candidate vote released the two-person gate")
	}
}

func (s *containerCopilotExecution) verifyTerminal(t *testing.T, f *controllerWorkerFixture) {
	t.Helper()
	steps, err := containerApprovalPersistedSteps(f.ctx, f.db, s.jobID)
	if err != nil {
		t.Fatal(err)
	}
	commands := map[int]model.StepRun{}
	for _, step := range steps {
		if step.Name == "approval-command" {
			if _, exists := commands[step.Attempt]; exists {
				t.Fatal("duplicate actual command attempt")
			}
			commands[step.Attempt] = step
		}
	}
	first, second := commands[1], commands[2]
	if len(commands) != 2 || first.Status != model.JobRunStatusFailed || first.ExitCode == nil || *first.ExitCode != 23 || !first.RetryableShellExit || second.Status != model.JobRunStatusSuccess {
		t.Fatal("Copilot vote alone is not acceptance: require actual exit-23 failure followed by successful bounded retry")
	}
	var publications int64
	if err := f.db.Model(&model.ProjectArtifact{}).Where("job_run_id = ?", s.jobID).Count(&publications).Error; err != nil || publications != 1 {
		t.Fatalf("successful command publications=%d err=%v", publications, err)
	}
	// Replay the original confirmation after completion, then read independent
	// status. The caller subsequently checks unchanged runtime effect counts.
	for index := range s.intents {
		intent, err := s.service.ConfirmRetryExecution(f.ctx, s.subjects[index], f.projectID, s.intents[index].IntentID, s.confirms[index])
		if err != nil || intent.State != "submitted" || intent.DecisionID != s.decisions[index] {
			t.Fatalf("terminal confirmation replay: %v", err)
		}
		current, err := s.service.GetRetryExecution(f.ctx, s.subjects[index], f.projectID, intent.IntentID)
		if err != nil || current.JobStatus == nil || *current.JobStatus != model.JobRunStatusSuccess || current.RuntimeAdmissionObserved == nil || !*current.RuntimeAdmissionObserved {
			t.Fatalf("intent did not observe actual admitted successful runtime: %v", err)
		}
		list, err := s.service.ListRetryExecutions(f.ctx, s.subjects[index], f.projectID, 0, 20)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range list.Intents {
			if entry.IntentID == intent.IntentID && entry.DecisionID == intent.DecisionID {
				found = true
			}
		}
		if !found {
			t.Fatal("submitted execution missing from owner's independent status list")
		}
	}
	t.Log("Copilot approve-bound-execution/retry-budget: encrypted candidate-owned conversation, actual producer artifact, local text model, inert typed review, cancelled-intent denial, detached intent after source deletion, two explicit votes, actual terminal runtime admission, same-decision terminal replay")
}

// This seam test is safe without container privileges; it does not stand in for
// either mandatory real-runtime acceptance scenario above.
func TestContainerCopilotLocalModelBoundedEvidence(t *testing.T) {
	m := &containerCopilotModel{project: "fixture-project", artifactID: "fixture-artifact", expectedPrompt: "Question:\nReview input\nSelected evidence (untrusted data):\n" + containerCopilotArtifactBlock("fixture-artifact")}
	if _, err := m.Resolve("another-project", "local-acceptance", "bounded-review"); err == nil {
		t.Fatal("cross-project model resolution")
	}
	if _, err := m.Resolve("fixture-project", "remote", "bounded-review"); err == nil {
		t.Fatal("unknown model resolution")
	}
	if _, err := m.Analyze(context.Background(), copilot.Input{User: "invented runtime success"}); err == nil {
		t.Fatal("model accepted missing producer evidence")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Analyze(ctx, copilot.Input{User: m.expectedPrompt}); err == nil {
		t.Fatal("model ignored cancellation")
	}
	out, err := m.Analyze(context.Background(), copilot.Input{User: m.expectedPrompt})
	if err != nil || m.calls != 1 || !strings.Contains(out.Text, "does not execute") {
		t.Fatal("local advice adapter failed")
	}
}

// Revoke only the disposable candidate's approval:read binding. Project/pipeline
// read and voting grants remain, proving the optional full evidence has its own
// live authorization boundary rather than relying on private history access.
func verifyContainerCopilotReadRevocation(t *testing.T, f *controllerWorkerFixture, service *controlservice.CopilotService, subject authz.Subject, intent controlservice.CopilotExecutionIntent) {
	t.Helper()
	var bindings []model.RolePermissionBinding
	err := f.db.Model(&model.RolePermissionBinding{}).
		Joins("JOIN role_grant ON role_grant.role_id = role_permission_binding.role_id").
		Joins("JOIN permission ON permission.permission_id = role_permission_binding.permission_id").
		Where("role_grant.subject_type = ? AND role_grant.subject_id = ? AND role_grant.scope_type = ? AND role_grant.scope_id = ? AND permission.resource_type = ? AND permission.action = ?", "user", subject.UserID, "project", f.projectID, "approval", "read").
		Select("role_permission_binding.*").Find(&bindings).Error
	if err != nil || len(bindings) != 1 {
		t.Fatalf("exact synthetic approval-read binding: count=%d err=%v", len(bindings), err)
	}
	binding := bindings[0]
	if err := f.db.Where("role_id = ? AND permission_id = ?", binding.RoleID, binding.PermissionID).Delete(&model.RolePermissionBinding{}).Error; err != nil {
		t.Fatal(err)
	}
	restored := false
	defer func() {
		if !restored {
			if err := f.db.Create(&binding).Error; err != nil {
				t.Error(err)
			}
		}
	}()
	current, err := service.GetRetryExecution(f.ctx, subject, f.projectID, intent.IntentID)
	if err != nil || current.State != "prepared" || current.RetryReview != nil {
		t.Fatalf("revoked approval-read exposed evidence or lost metadata: %v", err)
	}
	list, err := service.ListRetryExecutions(f.ctx, subject, f.projectID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range list.Intents {
		if entry.RetryReview != nil {
			t.Fatal("revoked approval-read exposed list evidence")
		}
		if entry.IntentID == intent.IntentID {
			found = true
		}
	}
	if !found {
		t.Fatal("scope-retained candidate lost independent intent metadata")
	}
	if err := f.db.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}
	restored = true
	current, err = service.GetRetryExecution(f.ctx, subject, f.projectID, intent.IntentID)
	if err != nil || current.RetryReview == nil || current.RetryReview.Evidence == nil {
		t.Fatalf("restored exact binding did not restore safe full evidence: %v", err)
	}
}

// Registration stores a producer-relative name. The input manifest and frozen
// retry evidence qualify that name with the producer job, never the model text.
func containerCopilotArtifactBinding(a *model.ProjectArtifact, producer *model.JobRun, step *model.StepRun, e *retryapproval.Evidence) error {
	if a == nil || producer == nil || step == nil || e == nil || len(e.Artifacts) != 1 {
		return fmt.Errorf("actual producer binding is incomplete")
	}
	input := e.Artifacts[0]
	if a.Name != "seed/input.txt" || producer.JobName != "approval-a" || input.Name != producer.JobName+"/"+a.Name || a.ProjectID != e.Context.ProjectID || a.PipelineID != e.Context.PipelineID || a.PipelineRunID != e.Context.PipelineRunID || a.JobRunID != input.ProducerJobRunID || a.StepRunID != input.ProducerStepRunID || a.JobRunID == e.Identity.JobRunID {
		return fmt.Errorf("registered artifact differs from approved producer identity")
	}
	if producer.JobRunID != a.JobRunID || producer.PipelineID != a.PipelineID || producer.PipelineRunID != a.PipelineRunID || producer.Status != model.JobRunStatusSuccess || step.StepRunID != a.StepRunID || step.JobRunID != a.JobRunID || step.PipelineID != a.PipelineID || step.PipelineRunID != a.PipelineRunID || step.Status != model.JobRunStatusSuccess || step.Name != "seed" {
		return fmt.Errorf("artifact producer job/step does not match successful upstream execution")
	}
	digest := sha256.Sum256([]byte("approval-input"))
	expiry := int64(0)
	if a.ExpiresAt != nil {
		expiry = a.ExpiresAt.Unix()
	}
	key, err := artifactutil.RemotePath(a.PipelineRunID, a.JobRunID, a.StepRunID, a.SHA256, a.Name)
	if err != nil || a.ArtifactID != artifactutil.ID(a.ProjectID, a.PipelineRunID, a.JobRunID, a.StepRunID, a.Name) || a.StorageType != "s3" || a.StoragePath != key || a.StoragePath != input.StorageKey || a.StorageID != input.Reference.StorageID || a.StorageFingerprint != input.Reference.Fingerprint || a.SHA256 != hex.EncodeToString(digest[:]) || a.SHA256 != input.Reference.SHA256 || input.Reference.Size == nil || a.Size != *input.Reference.Size || a.Size != int64(len("approval-input")) || expiry != input.Reference.ExpiresAt {
		return fmt.Errorf("registered artifact differs from exact approved content/storage reference")
	}
	return nil
}
func containerCopilotArtifactBlock(artifactID string) string {
	return fmt.Sprintf("\n<evidence kind=%q id=%q name=%q>\napproval-input\n</evidence>\n", "code-artifact", artifactID, "seed/input.txt")
}
func containerCopilotContextEvidence(values []controlservice.CopilotEvidence, a *model.ProjectArtifact) error {
	if a == nil || len(values) != 1 {
		return fmt.Errorf("actual Copilot artifact context missing or ambiguous")
	}
	v := values[0]
	if v.Kind != "code-artifact" || v.ID != a.ArtifactID || v.Name != a.Name || v.Bytes != int(a.Size) || v.Truncated {
		return fmt.Errorf("Copilot context differs from exact registered artifact")
	}
	return nil
}
