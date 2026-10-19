// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

// ConfigureRetryExecution is a startup-only opt-in separate from private history.
// It permits one explicit operation: recording the current human's bound vote.
func (s *CopilotService) ConfigureRetryExecution(config copilot.ExecutionConfig) {
	s.retryExecutionEnabled = config.RetryApprovalEnabled
}
func (s *CopilotService) retryExecutionReady() bool {
	return s != nil && s.retryExecutionEnabled && s.historyReady()
}
func (s *CopilotService) requireRetryExecution(subject authz.Subject, project string) error {
	if !s.retryExecutionReady() {
		return ErrCopilotHistoryUnavailable
	}
	return s.requireHistory(subject, project)
}

type CopilotExecutionPrepareRequest struct {
	RequestID                 string `json:"requestId"`
	ExpectedVersion           uint64 `json:"expectedVersion"`
	PlanDigest                string `json:"planDigest"`
	AcknowledgeDetachedIntent bool   `json:"acknowledgeDetachedIntent"`
}
type CopilotExecutionConfirmRequest struct {
	ExpectedVersion              uint64 `json:"expectedVersion"`
	IntentDigest                 string `json:"intentDigest"`
	AcknowledgePermanentApproval bool   `json:"acknowledgePermanentApproval"`
}
type CopilotExecutionCancelRequest struct {
	ExpectedVersion uint64 `json:"expectedVersion"`
	IntentDigest    string `json:"intentDigest"`
}
type CopilotExecutionIntent struct {
	IntentID                 string                `json:"intentId"`
	RequestID                string                `json:"requestId"`
	ProjectID                string                `json:"projectId"`
	PipelineID               string                `json:"pipelineId"`
	RunID                    string                `json:"runId"`
	JobRunID                 string                `json:"jobRunId"`
	ApprovalID               string                `json:"approvalId"`
	SourcePlanID             string                `json:"sourcePlanId"`
	SourcePlanVersion        uint64                `json:"sourcePlanVersion"`
	SourcePlanDigest         string                `json:"sourcePlanDigest"`
	IntentDigest             string                `json:"intentDigest"`
	Version                  uint64                `json:"version"`
	State                    string                `json:"state"`
	CreatedAt                time.Time             `json:"createdAt"`
	ExpiresAt                time.Time             `json:"expiresAt"`
	SubmittedAt              *time.Time            `json:"submittedAt,omitempty"`
	DecisionID               string                `json:"decisionId,omitempty"`
	ApprovalStatus           *int                  `json:"approvalStatus,omitempty"`
	ApprovedCount            *int                  `json:"approvedCount,omitempty"`
	RequiredApproverCount    *int                  `json:"requiredApproverCount,omitempty"`
	JobStatus                *int                  `json:"jobStatus,omitempty"`
	CurrentGeneration        *int                  `json:"currentGeneration,omitempty"`
	RuntimeAdmissionObserved *bool                 `json:"runtimeAdmissionObserved,omitempty"`
	RetryReview              *retryapproval.Review `json:"retryReview,omitempty"`
}
type CopilotExecutionList struct {
	Intents   []CopilotExecutionIntent `json:"intents"`
	NextAfter uint64                   `json:"nextAfter,omitempty"`
}

func (s *CopilotService) PrepareRetryExecution(ctx context.Context, subject authz.Subject, project, conversation, planID string, in CopilotExecutionPrepareRequest) (CopilotExecutionIntent, error) {
	if err := s.requireRetryExecution(subject, project); err != nil {
		return CopilotExecutionIntent{}, err
	}
	if !in.AcknowledgeDetachedIntent || !copilot.ValidID(in.RequestID) || len(in.RequestID) < 16 || in.ExpectedVersion == 0 || len(in.PlanDigest) > 128 || in.PlanDigest == "" {
		return CopilotExecutionIntent{}, ErrCopilotInput
	}
	// Review remains inert. This read verifies the decrypted payload and current
	// evidence before copying only its identifiers/digests into an independent intent.
	p, err := s.GetConversationPlan(ctx, subject, project, conversation, planID)
	if err != nil {
		return CopilotExecutionIntent{}, err
	}
	if p.State != "reviewed" || !p.Eligible || p.Kind != "retry_approval_review" || p.Version != in.ExpectedVersion || p.PlanDigest != in.PlanDigest || p.Payload == nil || p.Payload.RetryApproval == nil {
		return CopilotExecutionIntent{}, ErrCopilotHistoryConflict
	}
	primary, err := s.historyPrimary(ctx)
	if err != nil || primary == nil || primary.Approval == nil {
		return CopilotExecutionIntent{}, ErrCopilotHistoryUnavailable
	}
	target := p.Payload.RetryApproval
	reader, ok := primary.Approval.(interface {
		ReadRetryApproval(context.Context, string) (*retryapproval.Review, error)
	})
	if !ok {
		return CopilotExecutionIntent{}, ErrCopilotHistoryUnavailable
	}
	review, err := reader.ReadRetryApproval(ctx, target.ApprovalID)
	if err != nil || review == nil || review.Evidence == nil || review.EvidenceHash != target.EvidenceDigest || review.RetryJobRunID != target.JobRunID || review.Evidence.RetryPlanDigest != target.RetryPlanDigest || review.Evidence.InputManifestDigest != target.InputManifestDigest || review.Evidence.Identity.SourceBindingDigest != target.SourceBindingDigest || review.Evidence.Context.Policy.Fingerprint != target.PolicyFingerprint {
		return CopilotExecutionIntent{}, ErrCopilotHistoryConflict
	}
	// Eligibility is checked now for useful feedback and independently under the
	// authoritative domain locks when confirmation actually records the vote.
	validator, ok := primary.Approval.(interface {
		ValidateRetryApprovalDecision(context.Context, string, string, string) error
	})
	if !ok {
		return CopilotExecutionIntent{}, ErrCopilotHistoryUnavailable
	}
	if err = validator.ValidateRetryApprovalDecision(ctx, target.ApprovalID, subject.UserID, model.ApprovalDecisionApprove); err != nil {
		return CopilotExecutionIntent{}, ErrCopilotDenied
	}
	row, _, err := s.repos.CopilotExecutionPrepare(ctx, repo.CopilotExecutionPrepareInput{OwnerUserID: subject.UserID, ProjectID: project, ConversationID: conversation, PlanID: planID, PlanDigest: in.PlanDigest, RequestID: in.RequestID, PlanVersion: in.ExpectedVersion, Review: review}, historyAuthorize)
	if err != nil {
		return CopilotExecutionIntent{}, historyError(err)
	}
	return s.executionDTO(ctx, subject, row)
}
func (s *CopilotService) GetRetryExecution(ctx context.Context, subject authz.Subject, project, id string) (CopilotExecutionIntent, error) {
	if err := s.requireRetryExecution(subject, project); err != nil {
		return CopilotExecutionIntent{}, err
	}
	row, err := s.repos.CopilotExecutionGet(ctx, subject.UserID, project, id, historyAuthorize)
	if err != nil {
		return CopilotExecutionIntent{}, historyError(err)
	}
	return s.executionDTO(ctx, subject, row)
}
func (s *CopilotService) ListRetryExecutions(ctx context.Context, subject authz.Subject, project string, after uint64, limit int) (CopilotExecutionList, error) {
	out := CopilotExecutionList{Intents: []CopilotExecutionIntent{}}
	if err := s.requireRetryExecution(subject, project); err != nil {
		return out, err
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 20 {
		return out, ErrCopilotInput
	}
	page, err := s.repos.CopilotExecutionList(ctx, subject.UserID, project, after, limit, historyAuthorize)
	if err != nil {
		return out, historyError(err)
	}
	out.NextAfter = page.NextAfter
	size := 0
	for _, row := range page.List {
		value, err := s.executionDTO(ctx, subject, row)
		if err != nil {
			return CopilotExecutionList{}, err
		}
		b, err := json.Marshal(value)
		if err != nil {
			return CopilotExecutionList{}, ErrCopilotHistoryUnavailable
		}
		if size+len(b) > 512*1024 {
			if len(out.Intents) == 0 {
				return CopilotExecutionList{}, ErrCopilotHistoryUnavailable
			}
			out.NextAfter = page.List[len(out.Intents)-1].ID
			break
		}
		size += len(b)
		out.Intents = append(out.Intents, value)
	}
	return out, nil
}
func (s *CopilotService) ConfirmRetryExecution(ctx context.Context, subject authz.Subject, project, id string, in CopilotExecutionConfirmRequest) (CopilotExecutionIntent, error) {
	if err := s.requireRetryExecution(subject, project); err != nil {
		return CopilotExecutionIntent{}, err
	}
	if !in.AcknowledgePermanentApproval || in.ExpectedVersion == 0 || in.IntentDigest == "" || len(in.IntentDigest) > 128 {
		return CopilotExecutionIntent{}, ErrCopilotInput
	}
	row, err := s.repos.CopilotExecutionConfirm(ctx, repo.CopilotExecutionMutationInput{OwnerUserID: subject.UserID, ProjectID: project, IntentID: id, IntentDigest: in.IntentDigest, Version: in.ExpectedVersion}, historyAuthorize)
	if err != nil {
		return CopilotExecutionIntent{}, historyError(err)
	}
	return s.executionDTO(ctx, subject, row)
}
func (s *CopilotService) CancelRetryExecution(ctx context.Context, subject authz.Subject, project, id string, in CopilotExecutionCancelRequest) (CopilotExecutionIntent, error) {
	if err := s.requireRetryExecution(subject, project); err != nil {
		return CopilotExecutionIntent{}, err
	}
	if in.ExpectedVersion == 0 || in.IntentDigest == "" || len(in.IntentDigest) > 128 {
		return CopilotExecutionIntent{}, ErrCopilotInput
	}
	row, err := s.repos.CopilotExecutionCancel(ctx, repo.CopilotExecutionMutationInput{OwnerUserID: subject.UserID, ProjectID: project, IntentID: id, IntentDigest: in.IntentDigest, Version: in.ExpectedVersion}, historyAuthorize)
	if err != nil {
		return CopilotExecutionIntent{}, historyError(err)
	}
	return s.executionDTO(ctx, subject, row)
}
func (s *CopilotService) executionDTO(ctx context.Context, subject authz.Subject, row *model.CopilotExecutionIntent) (CopilotExecutionIntent, error) {
	if row == nil || row.OwnerUserID != subject.UserID {
		return CopilotExecutionIntent{}, ErrCopilotDenied
	}
	out := CopilotExecutionIntent{IntentID: row.IntentID, RequestID: row.RequestID, ProjectID: row.ProjectID, PipelineID: row.PipelineID, RunID: row.RunID, JobRunID: row.JobRunID, ApprovalID: row.ApprovalID, SourcePlanID: row.PlanID, SourcePlanVersion: row.PlanVersion, SourcePlanDigest: row.PlanDigest, IntentDigest: row.IntentDigest, Version: row.Version, State: row.State, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, SubmittedAt: row.SubmittedAt}
	if row.State == "prepared" && !time.Now().Before(row.ExpiresAt) {
		out.State = "expired"
	}
	if row.State == "submitted" {
		out.DecisionID = row.DecisionID
	}
	primary, err := s.historyPrimary(ctx)
	if err != nil || primary == nil {
		return CopilotExecutionIntent{}, ErrCopilotHistoryUnavailable
	}
	if primary.Approval == nil || primary.JobRun == nil {
		return CopilotExecutionIntent{}, ErrCopilotHistoryUnavailable
	}
	a, err := primary.Approval.GetByApprovalID(ctx, row.ApprovalID)
	if err != nil || a == nil || a.EvidenceHash != row.EvidenceHash || a.PipelineRunID != row.RunID || a.RetryJobRunID == nil || *a.RetryJobRunID != row.JobRunID {
		return CopilotExecutionIntent{}, ErrCopilotHistoryConflict
	}
	job, err := primary.JobRun.GetByJobRunID(ctx, row.JobRunID)
	if err != nil || job == nil || job.PipelineID != row.PipelineID || job.PipelineRunID != row.RunID || job.RetryApprovalID != row.ApprovalID || job.RetryApprovalEvidenceHash != row.EvidenceHash {
		return CopilotExecutionIntent{}, ErrCopilotHistoryConflict
	}
	out.ApprovalStatus = &a.Status
	out.ApprovedCount = &a.ApprovedCount
	out.RequiredApproverCount = &a.RequiredApproverCount
	out.JobStatus = &job.Status
	out.CurrentGeneration = &job.CurrentGeneration
	reader, ok := primary.Approval.(interface {
		ReadRetryApproval(context.Context, string) (*retryapproval.Review, error)
	})
	if !ok {
		return CopilotExecutionIntent{}, ErrCopilotHistoryUnavailable
	}
	review, readErr := reader.ReadRetryApproval(ctx, row.ApprovalID)
	if readErr == nil && review != nil && review.Evidence != nil && review.ApprovalID == row.ApprovalID && review.RetryJobRunID == row.JobRunID && review.EvidenceHash == row.EvidenceHash {
		digest, e := review.Evidence.Digest()
		if e == nil && digest == row.EvidenceDigest {
			out.RetryReview = review
		}
	}

	generation, gerr := s.repos.CopilotExecutionGeneration(ctx, subject.UserID, row.ProjectID, row.IntentID, historyAuthorize)
	if gerr != nil {
		return CopilotExecutionIntent{}, historyError(gerr)
	}
	observed := false
	if generation != nil && (generation.RetryApprovalAdmission != "" || generation.RetryApprovalAdmissionDigest != "") {
		admission, ae := retryapproval.ParseAdmission(generation.RetryApprovalAdmission)
		g, ge := taskqueue.ParseJobGeneration(generation.GenerationJSON)
		if ae != nil || ge != nil {
			return CopilotExecutionIntent{}, ErrCopilotHistoryConflict
		}
		if !executionAdmissionMatches(row, generation, admission, g, review) {
			return CopilotExecutionIntent{}, ErrCopilotHistoryConflict
		}
		observed = true
	}
	out.RuntimeAdmissionObserved = &observed
	// Full frozen policy/input evidence is a separately authorized view. Receipt
	// inspection stays possible after approval-read is revoked, without retaining
	// its previously visible candidates, source paths or artifact metadata.
	if out.RetryReview != nil {
		local, project, _, scopeErr := s.liveScope(ctx, subject, row.ProjectID, row.RunID, s.historyPrimary)
		if scopeErr != nil {
			return CopilotExecutionIntent{}, scopeErr
		}
		allowed, authErr := local.authorizer.Check(ctx, local.sourceSubject, "approval:read", authz.ResourceRef{Type: "project", ID: project.ProjectID, ProjectID: project.ProjectID, OrgID: project.OrgID})
		if authErr != nil {
			return CopilotExecutionIntent{}, ErrCopilotDenied
		}
		if !allowed {
			out.RetryReview = nil
		}
	}
	if err := s.executionPayloadGuard(ctx, subject, row, &out); err != nil {
		return CopilotExecutionIntent{}, err
	}
	// Fail closed on scope revocation during observation. This read cannot submit
	// or repeat any operation, and keeps the immutable vote separate from runtime.
	current, err := s.repos.CopilotExecutionGet(ctx, subject.UserID, row.ProjectID, row.IntentID, historyAuthorize)
	if err != nil {
		return CopilotExecutionIntent{}, historyError(err)
	}
	if current.Version != row.Version || current.State != row.State {
		return CopilotExecutionIntent{}, ErrCopilotHistoryConflict
	}
	// Keep the first redaction check and refresh it after final authorization, so
	// rotation during assembly cannot make an earlier secret visible.
	if err := s.executionPayloadGuard(ctx, subject, row, &out); err != nil {
		return CopilotExecutionIntent{}, err
	}
	if out.RetryReview != nil {
		local, project, _, err := s.liveScope(ctx, subject, row.ProjectID, row.RunID, s.historyPrimary)
		if err != nil {
			return CopilotExecutionIntent{}, err
		}
		allowed, err := local.authorizer.Check(ctx, local.sourceSubject, "approval:read", authz.ResourceRef{Type: "project", ID: project.ProjectID, ProjectID: project.ProjectID, OrgID: project.OrgID})
		if err != nil {
			return CopilotExecutionIntent{}, ErrCopilotDenied
		}
		if !allowed {
			out.RetryReview = nil
		}
	}
	return out, nil
}

// A persisted admission is an observation only after every immutable input and
// execution identity agrees. A canonical receipt for a different task is not proof.
func executionAdmissionMatches(row *model.CopilotExecutionIntent, generation *model.JobRunGeneration, admission *retryapproval.Admission, g *taskqueue.JobGeneration, review *retryapproval.Review) bool {
	if row == nil || generation == nil || admission == nil || g == nil || review == nil || review.Evidence == nil {
		return false
	}
	e := review.Evidence
	evidenceDigest, err := e.Digest()
	if err != nil || evidenceDigest != row.EvidenceDigest || review.EvidenceHash != row.EvidenceHash || e.ApprovalID != row.ApprovalID {
		return false
	}
	digest, err := admission.Digest()
	identity := taskqueue.JobInputIdentity{PipelineID: g.PipelineID, PipelineRunID: g.PipelineRunID, JobRunID: g.JobRunID, AgentID: g.AgentID, ClaimID: g.OriginalClaimID, RuntimeBindingDigest: g.RuntimeBindingDigest, SourceBindingDigest: g.SourceBindingDigest}
	return err == nil && digest == generation.RetryApprovalAdmissionDigest && admission.ApprovalID == row.ApprovalID && admission.EvidenceHash == row.EvidenceHash &&
		admission.Request.Identity == e.Identity && identity == e.Identity && e.Identity.PipelineID == row.PipelineID && e.Identity.PipelineRunID == row.RunID && e.Identity.JobRunID == row.JobRunID &&
		admission.Request.RetryPlanDigest == e.RetryPlanDigest && g.RetryPlanDigest == e.RetryPlanDigest && admission.Request.InputManifestDigest == e.InputManifestDigest && (g.InputManifestDigest == e.InputManifestDigest || g.Number == 1 && g.InputManifestDigest == "") &&
		admission.ExpiresAtUnixMilli == e.ExpiresAtUnixMilli && admission.Request.GenerationID == generation.GenerationID && admission.Request.GenerationBindingDigest == g.BindingDigest && g.GenerationID == generation.GenerationID && g.Number == generation.Number
}

// Detached intent inspection does not depend on deleted conversation bodies.
// Rebuild current redaction sets from authoritative run/step scopes instead.
func (s *CopilotService) executionPayloadGuard(ctx context.Context, subject authz.Subject, row *model.CopilotExecutionIntent, out *CopilotExecutionIntent) error {
	local, project, run, err := s.liveScope(ctx, subject, row.ProjectID, row.RunID, s.historyPrimary)
	if err != nil {
		return err
	}
	if local.repos.StepRun == nil {
		return ErrCopilotSecrets
	}
	steps, total, err := local.repos.StepRun.List(ctx, repo.StepRunFilter{PipelineID: row.PipelineID, PipelineRunID: row.RunID, Page: 1, PageSize: 200})
	if err != nil || total != int64(len(steps)) || len(steps) > 200 {
		return ErrCopilotSecrets
	}
	pointers := make([]*model.StepRun, 0, len(steps)+1)
	stepIDs := map[string]bool{}
	stepRows := map[string]*model.StepRun{}
	for i := range steps {
		if steps[i].PipelineID != row.PipelineID || steps[i].PipelineRunID != row.RunID || !copilot.ValidID(steps[i].StepRunID) || stepIDs[steps[i].StepRunID] {
			return ErrCopilotSecrets
		}
		stepIDs[steps[i].StepRunID] = true
		stepRows[steps[i].StepRunID] = &steps[i]
		pointers = append(pointers, &steps[i])
	}
	job, err := local.repos.JobRun.GetByJobRunID(ctx, row.JobRunID)
	if err != nil || job == nil || job.JobRunID != row.JobRunID || job.PipelineRunID != run.RunID || job.PipelineID != run.PipelineID {
		return ErrCopilotSecrets
	}
	pointers = append(pointers, &model.StepRun{Env: job.Env})
	r, err := local.redactor(ctx, project, run, pointers)
	if err != nil {
		return ErrCopilotSecrets
	}
	redactors := []*copilot.Redactor{r}
	if out.RetryReview != nil {
		seen := map[string]bool{}
		for _, artifact := range out.RetryReview.Evidence.Artifacts {
			step := stepRows[artifact.ProducerStepRunID]
			if step == nil || step.JobRunID != artifact.ProducerJobRunID {
				return ErrCopilotSecrets
			}
			if seen[artifact.ProducerJobRunID] {
				continue
			}
			if len(seen) >= 200 {
				return ErrCopilotSecrets
			}
			seen[artifact.ProducerJobRunID] = true
			producer, err := local.repos.JobRun.GetByJobRunID(ctx, artifact.ProducerJobRunID)
			if err != nil || producer == nil || producer.JobRunID != artifact.ProducerJobRunID || producer.PipelineRunID != row.RunID || producer.PipelineID != row.PipelineID {
				return ErrCopilotSecrets
			}
			pointers = append(pointers, &model.StepRun{Env: producer.Env})
		}
		// One bounded snapshot covers all exact same-run producer environments.
		pr, err := local.redactor(ctx, project, run, pointers)
		if err != nil {
			return ErrCopilotSecrets
		}
		redactors = append(redactors, pr)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return ErrCopilotSecrets
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ErrCopilotSecrets
	}
	var safe func(any) bool
	safe = func(v any) bool {
		switch x := v.(type) {
		case string:
			for _, r := range redactors {
				if r.Redact(x) != x {
					return false
				}
			}
		case []any:
			for _, item := range x {
				if !safe(item) {
					return false
				}
			}
		case map[string]any:
			for key, item := range x {
				if !safe(key) || !safe(item) {
					return false
				}
			}
		}
		return true
	}
	if !safe(value) {
		return ErrCopilotSecrets
	}
	return nil
}
