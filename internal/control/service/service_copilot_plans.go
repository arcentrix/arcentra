// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	cluster "github.com/arcentrix/arcentra/internal/shared/deployment"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/google/uuid"
)

// The plan service stores only copied static target values. It retains no
// DeploymentService or cluster/SCM execution adapter, so proposal/review cannot
// reserve an environment, approve a retry, create a run or apply a patch.
type copilotPlanTarget struct {
	ProjectID, Fingerprint, Tier string
	Target                       cluster.Target
	ApprovalRequired             bool
	PolicyDigest                 string
}

func (s *CopilotService) ConfigureHistoryPlans(deployments *DeploymentService) {
	s.historyPlanTargets = map[string]copilotPlanTarget{}
	if deployments == nil {
		return
	}
	for ref, b := range deployments.targets {
		if b.Adapter == nil {
			continue
		}
		policy, _ := json.Marshal(b.PipelineApproval)
		s.historyPlanTargets[ref] = copilotPlanTarget{ProjectID: b.ProjectID, Fingerprint: b.Fingerprint, Tier: b.Tier, Target: b.Target, ApprovalRequired: b.PipelineApproval != nil, PolicyDigest: historyHash(string(policy))}
	}
}

type copilotPlanEnvelope struct {
	Schema  int                `json:"schema"`
	Payload CopilotPlanPayload `json:"payload"`
	Binding string             `json:"binding"`
}

func validateCopilotPlanRequest(in CopilotPlanRequest) error {
	if !copilot.ValidID(in.RequestID) || len(in.RequestID) < 16 || !copilot.ValidID(in.TurnID) {
		return ErrCopilotInput
	}
	switch in.Kind {
	case "code_patch":
		if in.CodePatch == nil || in.RetryApproval != nil || in.Deployment != nil || (in.CodePatch.ArtifactID == "") == (in.CodePatch.SourceFile == nil) || len(in.CodePatch.Patch) > 16384 || in.CodePatch.Patch == "" {
			return ErrCopilotInput
		}
	case "new_run_review":
		if in.CodePatch != nil || in.RetryApproval != nil || in.Deployment != nil {
			return ErrCopilotInput
		}
	case "retry_approval_review":
		if in.RetryApproval == nil || in.CodePatch != nil || in.Deployment != nil || !copilot.ValidID(in.RetryApproval.ApprovalID) {
			return ErrCopilotInput
		}
	case "deployment_review":
		if in.Deployment == nil || in.CodePatch != nil || in.RetryApproval != nil || !copilot.ValidID(in.Deployment.EnvironmentID) || !copilot.ValidID(in.Deployment.ArtifactID) || (in.Deployment.ReleaseID != "" && !copilot.ValidID(in.Deployment.ReleaseID)) {
			return ErrCopilotInput
		}
	default:
		return ErrCopilotInput
	}
	return nil
}
func (s *CopilotService) planParent(ctx context.Context, subject authz.Subject, project, id, turnID string) (*model.CopilotConversation, *model.CopilotTurn, *copilotHistoryEnvelope, *copilotHistoryState, error) {
	c, err := s.historyConversation(ctx, subject, project, id)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	t, err := s.repos.CopilotHistoryGetTurn(ctx, subject.UserID, project, id, turnID, historyAuthorize)
	if err != nil {
		return nil, nil, nil, nil, historyError(err)
	}
	view, payload, err := s.historyTurnDTO(ctx, subject, c, t, map[string]bool{})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if !view.ContentAvailable || payload == nil {
		return nil, nil, nil, nil, ErrCopilotHistoryContent
	}
	state, err := s.historyState(ctx, subject, c, payload.Selection)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	state.contentUntil = view.ContentAvailableUntil
	state.planRedactors, state.planRedactionBinding, err = s.copilotPlanRedactionState(ctx, subject, c, payload, state)
	return c, t, payload, state, err
}
func (s *CopilotService) CreateConversationPlan(ctx context.Context, subject authz.Subject, project, id string, in CopilotPlanRequest) (CopilotPlan, error) {
	if err := validateCopilotPlanRequest(in); err != nil {
		return CopilotPlan{}, err
	}
	c, t, parent, state, err := s.planParent(ctx, subject, project, id, in.TurnID)
	if err != nil {
		return CopilotPlan{}, err
	}
	p, binding, err := s.buildCopilotPlan(ctx, subject, c, parent, state, in)
	if err != nil {
		return CopilotPlan{}, err
	}
	// Any redaction of immutable target/diff bytes makes a verified plan unsafe.
	if guardErr := s.copilotPlanPayloadGuard(ctx, subject, c, parent, state, p); guardErr != nil {
		return CopilotPlan{}, ErrCopilotPlanUnavailable
	}
	content := copilotPlanEnvelope{Schema: 1, Payload: p, Binding: binding}
	encoded, _ := json.Marshal(content)
	digest := historyMAC(s.historyCipher.bindingKey, "plan-v1", subject.UserID, project, id, t.TurnID, string(encoded))
	planID := uuid.NewString()
	cipher, err := s.historyCipher.seal(subject.UserID, project, id, planID, "plan", content)
	if err != nil {
		return CopilotPlan{}, err
	}
	// Bind the second current metadata snapshot; no model-provided approval or
	// URL can populate the typed proposal or bypass these authoritative reads.
	refreshed, err := s.historyState(ctx, subject, c, parent.Selection)
	if err != nil {
		return CopilotPlan{}, err
	}
	if refreshed.seal != state.seal {
		return CopilotPlan{}, ErrCopilotPreview
	}
	if err = s.copilotPlanPayloadGuard(ctx, subject, c, parent, state, p); err != nil {
		return CopilotPlan{}, err
	}
	row, _, err := s.repos.CopilotHistoryCreatePlan(ctx, repo.CopilotHistoryPlanInput{OwnerUserID: subject.UserID, ProjectID: project, ConversationID: id, TurnID: t.TurnID, PlanID: planID, RequestID: in.RequestID, Kind: in.Kind, PlanDigest: digest, ConversationVersion: c.Version, TurnVersion: t.Version, SchemaVersion: 1, CipherVersion: 1, PayloadCiphertext: cipher}, historyAuthorize)
	if err != nil {
		return CopilotPlan{}, historyError(err)
	}
	return s.copilotPlanDTO(ctx, subject, c, row)
}
func (s *CopilotService) GetConversationPlan(ctx context.Context, subject authz.Subject, project, id, planID string) (CopilotPlan, error) {
	c, err := s.historyConversation(ctx, subject, project, id)
	if err != nil {
		return CopilotPlan{}, err
	}
	p, err := s.repos.CopilotHistoryGetPlan(ctx, subject.UserID, project, id, planID, historyAuthorize)
	if err != nil {
		return CopilotPlan{}, historyError(err)
	}
	return s.copilotPlanDTO(ctx, subject, c, p)
}
func (s *CopilotService) ListConversationPlans(ctx context.Context, subject authz.Subject, project, id string, after uint64, limit int) (CopilotPlanList, error) {
	out := CopilotPlanList{Plans: []CopilotPlan{}}
	c, err := s.historyConversation(ctx, subject, project, id)
	if err != nil {
		return out, err
	}
	if limit == 0 {
		limit = 20
	}
	page, err := s.repos.CopilotHistoryPlans(ctx, subject.UserID, project, id, after, limit, historyAuthorize)
	if err != nil {
		return out, historyError(err)
	}
	size, lastID := 1024, uint64(0)
	out.NextAfter = page.NextAfter
	for _, p := range page.List {
		v, e := s.copilotPlanDTO(ctx, subject, c, p)
		if e != nil {
			return CopilotPlanList{}, e
		}
		encoded, e := json.Marshal(v)
		if e != nil {
			return CopilotPlanList{}, ErrCopilotPlanUnavailable
		}
		if size+len(encoded) > 256*1024 {
			if lastID == 0 {
				return CopilotPlanList{}, ErrCopilotPlanUnavailable
			}
			out.NextAfter = lastID
			break
		}
		size += len(encoded)
		lastID = p.ID
		out.Plans = append(out.Plans, v)
	}
	if _, err = s.historyConversation(ctx, subject, project, id); err != nil {
		return CopilotPlanList{}, err
	}
	return out, nil
}
func (s *CopilotService) ReviewConversationPlan(ctx context.Context, subject authz.Subject, project, id, planID string, in CopilotPlanReviewRequest) (CopilotPlan, error) {
	if !in.AcknowledgeNotExecuted || in.ExpectedVersion == 0 || len(in.PlanDigest) != 64 {
		return CopilotPlan{}, ErrCopilotInput
	}
	v, err := s.GetConversationPlan(ctx, subject, project, id, planID)
	if err != nil {
		return CopilotPlan{}, err
	}
	if !v.Eligible || v.PlanDigest != in.PlanDigest || v.Version != in.ExpectedVersion {
		return CopilotPlan{}, ErrCopilotHistoryConflict
	}
	_, err = s.repos.CopilotHistoryTransitionPlan(ctx, repo.CopilotHistoryPlanTransitionInput{OwnerUserID: subject.UserID, ProjectID: project, ConversationID: id, PlanID: planID, PlanDigest: in.PlanDigest, Version: in.ExpectedVersion, State: "reviewed"}, historyAuthorize)
	if err != nil {
		return CopilotPlan{}, historyError(err)
	}
	return s.GetConversationPlan(ctx, subject, project, id, planID)
}
func (s *CopilotService) CancelConversationPlan(ctx context.Context, subject authz.Subject, project, id, planID string, in CopilotPlanCancelRequest) (CopilotPlan, error) {
	v, err := s.GetConversationPlan(ctx, subject, project, id, planID)
	if err != nil {
		return CopilotPlan{}, err
	}
	_, err = s.repos.CopilotHistoryTransitionPlan(ctx, repo.CopilotHistoryPlanTransitionInput{OwnerUserID: subject.UserID, ProjectID: project, ConversationID: id, PlanID: planID, PlanDigest: v.PlanDigest, Version: in.ExpectedVersion, State: "cancelled", ReasonCode: "cancelled"}, historyAuthorize)
	if err != nil {
		return CopilotPlan{}, historyError(err)
	}
	return s.GetConversationPlan(ctx, subject, project, id, planID)
}
func (s *CopilotService) copilotPlanDTO(ctx context.Context, subject authz.Subject, c *model.CopilotConversation, row *model.CopilotActionPlan) (CopilotPlan, error) {
	out := CopilotPlan{PlanID: row.PlanID, RequestID: row.RequestID, TurnID: row.TurnID, Kind: row.Kind, State: row.State, Version: row.Version, PlanDigest: row.PlanDigest, ExecutionEnabled: false, ExecutionStatus: "Not executed", CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt}
	unavailable := func() (CopilotPlan, error) {
		out.UnavailableReason = "plan_unavailable"
		if out.State == "proposed" || out.State == "reviewed" {
			out.State = "invalidated"
		}
		return out, nil
	}
	if row.OwnerUserID != subject.UserID || row.ProjectID != c.ProjectID || row.ConversationID != c.ConversationID {
		return CopilotPlan{}, ErrCopilotNotFound
	}
	if !row.ExpiresAt.After(time.Now()) {
		out.State = "expired"
		return unavailable()
	}
	if row.State == "cancelled" || row.State == "invalidated" || row.State == "expired" {
		return unavailable()
	}
	var saved copilotPlanEnvelope
	if row.CipherVersion != 1 || s.historyCipher.open(subject.UserID, c.ProjectID, c.ConversationID, row.PlanID, "plan", row.PayloadCiphertext, &saved) != nil || saved.Schema != 1 || saved.Payload.SchemaVersion != 1 || saved.Payload.Kind != row.Kind {
		return unavailable()
	}
	_, _, parent, state, err := s.planParent(ctx, subject, c.ProjectID, c.ConversationID, row.TurnID)
	if err != nil {
		if err == ErrCopilotDenied || err == ErrCopilotNotFound {
			return CopilotPlan{}, err
		}
		return unavailable()
	}
	if guardErr := s.copilotPlanPayloadGuard(ctx, subject, c, parent, state, saved.Payload); guardErr != nil {
		return unavailable()
	}
	if err = s.validateSavedCopilotPlan(ctx, subject, c, parent, state, saved); err != nil {
		return unavailable()
	}
	latestState, stateErr := s.historyState(ctx, subject, c, parent.Selection)
	if stateErr != nil || latestState.seal != state.seal {
		return unavailable()
	}
	if err = s.copilotPlanPayloadGuard(ctx, subject, c, parent, state, saved.Payload); err != nil {
		return unavailable()
	}
	if _, err = s.historyConversation(ctx, subject, c.ProjectID, c.ConversationID); err != nil {
		return CopilotPlan{}, err
	}
	latest, refreshErr := s.repos.CopilotHistoryGetPlan(ctx, subject.UserID, c.ProjectID, c.ConversationID, row.PlanID, historyAuthorize)
	if refreshErr != nil {
		return CopilotPlan{}, historyError(refreshErr)
	}
	if latest.Version != row.Version || latest.State != row.State || latest.PlanDigest != row.PlanDigest {
		return CopilotPlan{}, ErrCopilotHistoryConflict
	}
	if !row.ExpiresAt.After(time.Now()) {
		out.State = "expired"
		return unavailable()
	}
	out.ContentAvailableUntil = historyEarlier(&row.ExpiresAt, state.contentUntil)
	if saved.Payload.RetryApproval != nil {
		out.ContentAvailableUntil = historyEarlier(out.ContentAvailableUntil, saved.Payload.RetryApproval.Deadline)
	}
	if saved.Payload.Deployment != nil {
		out.ContentAvailableUntil = historyEarlier(out.ContentAvailableUntil, saved.Payload.Deployment.ArtifactExpiresAt)
	}
	if out.ContentAvailableUntil != nil && !out.ContentAvailableUntil.After(time.Now()) {
		return unavailable()
	}
	out.Payload = &saved.Payload
	out.Eligible = true
	return out, nil
}
func (s *CopilotService) buildCopilotPlan(ctx context.Context, subject authz.Subject, c *model.CopilotConversation, parent *copilotHistoryEnvelope, state *copilotHistoryState, in CopilotPlanRequest) (CopilotPlanPayload, string, error) {
	out := CopilotPlanPayload{SchemaVersion: 1, Kind: in.Kind}
	switch in.Kind {
	case "code_patch":
		p, err := s.buildCopilotPatch(ctx, c, parent, state, *in.CodePatch)
		if err != nil {
			return out, "", err
		}
		out.Label = "Review a code proposal. Not executed"
		if p.SnapshotOnly {
			out.Label = "Snapshot patch; repository destination unverified. Not executed"
		}
		out.CodePatch = p
		binding, guardErr := s.copilotPlanTextGuard(ctx, subject, c, parent, state, p.Patch)
		return out, binding, guardErr
	case "new_run_review":
		p, binding, err := s.buildCopilotNewRun(ctx, c, state)
		out.Label = "Review a new run; verify the current definition and inputs in pipeline review. Not executed"
		out.NewRun = p
		return out, binding, err
	case "retry_approval_review":
		p, binding, err := s.buildCopilotRetry(ctx, c, state, in.RetryApproval.ApprovalID)
		out.Label = "Review the existing retry approval. Not executed"
		out.RetryApproval = p
		return out, binding, err
	case "deployment_review":
		p, binding, err := s.buildCopilotDeployment(ctx, c, state, *in.Deployment, true)
		out.Label = "Review a deployment proposal. Not executed"
		if p != nil && p.ApprovalRequired {
			out.Label = "Review through the existing pipeline approval pathway. Not executed"
		}
		out.Deployment = p
		return out, binding, err
	}
	return out, "", ErrCopilotInput
}
func (s *CopilotService) buildCopilotNewRun(ctx context.Context, c *model.CopilotConversation, state *copilotHistoryState) (*CopilotNewRunPlan, string, error) {
	if state.run.Status != model.PipelineStatusFailed && state.run.Status != model.PipelineStatusCancelled {
		return nil, "", ErrCopilotPlanUnavailable
	}
	p, err := state.local.repos.Pipeline.Get(ctx, c.PipelineID)
	if err != nil || p == nil || p.PipelineID != c.PipelineID || p.ProjectID != c.ProjectID || !commitPattern.MatchString(p.LastCommitSha) {
		return nil, "", ErrCopilotPlanUnavailable
	}
	binding := historyMAC(s.historyCipher.bindingKey, "new-run", p.PipelineID, p.ProjectID, p.RepoURL, p.DefaultBranch, p.PipelineFilePath, p.LastCommitSha)
	return &CopilotNewRunPlan{PipelineID: c.PipelineID, DiagnosticRunID: c.RunID, DefinitionRevision: p.LastCommitSha, DefinitionSource: "last_saved_revision", DefinitionVerified: false}, binding, nil
}
func (s *CopilotService) buildCopilotRetry(ctx context.Context, c *model.CopilotConversation, state *copilotHistoryState, id string) (*CopilotRetryApprovalPlan, string, error) {
	if state.local.repos.Approval == nil || state.local.repos.JobRun == nil {
		return nil, "", ErrCopilotPlanUnavailable
	}
	a, err := state.local.repos.Approval.GetByApprovalID(ctx, id)
	if err != nil || a == nil || a.ApprovalID != id || a.PipelineRunID != c.RunID || a.Status != model.ApprovalStatusPending || a.RetryJobRunID == nil || a.ExpiresAt == nil || !a.ExpiresAt.After(time.Now()) || (a.BindingKind != retryapproval.Kind && a.BindingKind != retryapproval.FailedStepKind) {
		return nil, "", ErrCopilotPlanUnavailable
	}
	reader, ok := state.local.repos.Approval.(interface {
		ReadRetryApproval(context.Context, string) (*retryapproval.Review, error)
	})
	if !ok {
		return nil, "", ErrCopilotPlanUnavailable
	}
	review, readErr := reader.ReadRetryApproval(ctx, id)
	if readErr != nil || review == nil || review.ApprovalID != id || review.Status != model.ApprovalStatusPending || review.EvidenceHash != a.EvidenceHash {
		return nil, "", ErrCopilotPlanUnavailable
	}
	evidence, err := retryapproval.ParseEvidence(a.EvidenceJSON)
	if err != nil {
		return nil, "", ErrCopilotPlanUnavailable
	}
	digest, err := evidence.Digest()
	if err != nil || digest != a.EvidenceHash || evidence.ApprovalID != id || evidence.Kind != a.BindingKind || evidence.Context.ProjectID != c.ProjectID || evidence.Identity.PipelineID != c.PipelineID || evidence.Identity.PipelineRunID != c.RunID || evidence.Identity.JobRunID != *a.RetryJobRunID || evidence.Context.Policy.ID != a.PolicyID || evidence.ExpiresAtUnixMilli != a.ExpiresAt.UnixMilli() {
		return nil, "", ErrCopilotPlanUnavailable
	}
	job, err := state.local.repos.JobRun.GetByJobRunID(ctx, *a.RetryJobRunID)
	if err != nil || job == nil || job.JobRunID != *a.RetryJobRunID || job.PipelineID != c.PipelineID || job.PipelineRunID != c.RunID || job.Status != model.JobRunStatusRunning || job.RetryApprovalID != id || job.RetryApprovalEvidenceHash != digest || job.RetryPlanDigest != evidence.RetryPlanDigest || job.InputManifestDigest != evidence.InputManifestDigest {
		return nil, "", ErrCopilotPlanUnavailable
	}
	candidates, err := model.ApprovalCandidates(a)
	if err != nil || !reflect.DeepEqual(candidates, evidence.Context.Policy.Candidates) || a.Mode != evidence.Context.Policy.Mode || a.RequiredApproverCount != evidence.Context.Policy.RequiredApproverCount {
		return nil, "", ErrCopilotPlanUnavailable
	}
	b := historyMAC(s.historyCipher.bindingKey, "retry-review", digest, a.CandidateApprovers, job.CurrentGenerationID)
	return &CopilotRetryApprovalPlan{ApprovalID: id, JobRunID: job.JobRunID, BindingKind: a.BindingKind, EvidenceDigest: digest, PolicyID: a.PolicyID, Deadline: a.ExpiresAt, SourceBindingDigest: evidence.Identity.SourceBindingDigest, InputManifestDigest: evidence.InputManifestDigest, RetryPlanDigest: evidence.RetryPlanDigest, PolicyFingerprint: evidence.Context.Policy.Fingerprint}, b, nil
}
func (s *CopilotService) buildCopilotDeployment(ctx context.Context, c *model.CopilotConversation, state *copilotHistoryState, in CopilotDeploymentReviewInput, readBody bool) (*CopilotDeploymentPlan, string, error) {
	r := state.local.repos
	if r.Deployment == nil || r.Artifact == nil || state.run.Status != model.PipelineStatusSuccess || deploymentDefinitionRevision(state.run) == "" {
		return nil, "", ErrCopilotPlanUnavailable
	}
	if r.Approval == nil {
		return nil, "", ErrCopilotPlanUnavailable
	}
	approvals, err := r.Approval.ListByPipelineRunID(ctx, c.RunID)
	if err != nil || len(approvals) > 200 {
		return nil, "", ErrCopilotPlanUnavailable
	}
	for _, a := range approvals {
		if a == nil || a.PipelineRunID != c.RunID || a.Status != model.ApprovalStatusApproved {
			return nil, "", ErrCopilotPlanUnavailable
		}
	}
	env, err := r.Deployment.GetEnvironment(ctx, c.ProjectID, in.EnvironmentID)
	if err != nil || env == nil || env.EnvironmentID != in.EnvironmentID || env.ProjectID != c.ProjectID {
		return nil, "", ErrCopilotPlanUnavailable
	}
	target, ok := s.historyPlanTargets[env.TargetRef]
	if !ok || target.ProjectID != c.ProjectID || target.Target != environmentTarget(env) || target.Fingerprint == "" {
		return nil, "", ErrCopilotPlanUnavailable
	}
	a, err := r.Artifact.Get(ctx, c.ProjectID, in.ArtifactID)
	if err != nil || a == nil || a.ArtifactID != in.ArtifactID || a.ProjectID != c.ProjectID || a.PipelineID != c.PipelineID || a.PipelineRunID != c.RunID || a.Size <= 0 || a.Size > 4096 || len(a.SHA256) != 64 || (a.ExpiresAt != nil && !a.ExpiresAt.After(time.Now())) {
		return nil, "", ErrCopilotPlanUnavailable
	}
	if err := state.local.historyArtifactAvailable(ctx, a); err != nil {
		return nil, "", ErrCopilotPlanUnavailable
	}
	image := ""
	if readBody {
		if state.local.artifacts == nil {
			return nil, "", ErrCopilotPlanUnavailable
		}
		stream, err := state.local.artifacts.Open(ctx, c.ProjectID, a.ArtifactID)
		if err != nil {
			return nil, "", ErrCopilotPlanUnavailable
		}
		raw, e := io.ReadAll(io.LimitReader(stream.Reader, 4097))
		ce := stream.Reader.Close()
		if e != nil || ce != nil || int64(len(raw)) != a.Size || historyHash(string(raw)) != a.SHA256 {
			return nil, "", ErrCopilotPlanUnavailable
		}
		var manifest map[string]string
		if json.Unmarshal(raw, &manifest) != nil || len(manifest) != 1 || !cluster.ValidImage(manifest["image"]) {
			return nil, "", ErrCopilotPlanUnavailable
		}
		image = manifest["image"]
	}
	out := &CopilotDeploymentPlan{EnvironmentID: env.EnvironmentID, ArtifactID: a.ArtifactID, ArtifactDigest: a.SHA256, TargetFingerprint: target.Fingerprint, Namespace: env.Namespace, Workload: env.Deployment, Container: env.Container, ImageDigest: image, ApprovalRequired: target.ApprovalRequired, ArtifactExpiresAt: a.ExpiresAt, PipelineID: c.PipelineID, SourceRunID: c.RunID, DefinitionRevision: deploymentDefinitionRevision(state.run), TargetRef: env.TargetRef, Tier: target.Tier, PolicyDigest: target.PolicyDigest}
	binding := historyMAC(s.historyCipher.bindingKey, "deployment-review", env.EnvironmentID, env.TargetRef, target.Fingerprint, target.Tier, target.PolicyDigest, env.Namespace, env.Deployment, env.Container, c.RunID, deploymentDefinitionRevision(state.run), a.ArtifactID, a.SHA256)
	if in.ReleaseID != "" {
		release, err := r.Deployment.GetRelease(ctx, c.ProjectID, in.ReleaseID)
		if err != nil || release == nil || release.ReleaseID != in.ReleaseID || release.ProjectID != c.ProjectID || release.PipelineID != c.PipelineID || release.PipelineRunID != c.RunID || release.EnvironmentID != env.EnvironmentID || release.ArtifactID != a.ArtifactID || release.ArtifactSHA256 != a.SHA256 || release.TargetFingerprint != target.Fingerprint || release.CommitSHA != deploymentDefinitionRevision(state.run) {
			return nil, "", ErrCopilotPlanUnavailable
		}
		if readBody && release.Image != image {
			return nil, "", ErrCopilotPlanUnavailable
		}
		out.ReleaseID = release.ReleaseID
		out.ReleaseVersion = release.Version
		out.ReleaseOrigin = release.Origin
		out.ReleaseState = release.Status
		binding = historyMAC(s.historyCipher.bindingKey, binding, release.ReleaseID, release.Origin, release.Status, strconv.FormatInt(release.Version, 10))
	}
	return out, binding, nil
}
func (s *CopilotService) buildCopilotPatch(ctx context.Context, c *model.CopilotConversation, parent *copilotHistoryEnvelope, state *copilotHistoryState, in CopilotCodePatchInput) (*CopilotCodePatchPlan, error) {
	if state.redactor.Redact(in.Patch) != in.Patch {
		return nil, ErrCopilotPlanUnavailable
	}
	out := &CopilotCodePatchPlan{Patch: in.Patch}
	var base []byte
	if in.ArtifactID != "" {
		found := false
		for _, id := range parent.Selection.ArtifactIDs {
			if id == in.ArtifactID {
				found = true
			}
		}
		if !found || state.local.artifacts == nil {
			return nil, ErrCopilotPlanUnavailable
		}
		a, err := state.local.artifacts.Get(ctx, c.ProjectID, in.ArtifactID)
		if err != nil || a == nil || a.SHA256 != parent.ArtifactDigests[in.ArtifactID] || a.PipelineID != c.PipelineID || a.PipelineRunID != c.RunID || a.Size < 0 || a.Size > 8192 || !copilot.CodeArtifactName(a.Name) {
			return nil, ErrCopilotPlanUnavailable
		}
		stream, err := state.local.artifacts.Open(ctx, c.ProjectID, a.ArtifactID)
		if err != nil {
			return nil, ErrCopilotPlanUnavailable
		}
		raw, e := io.ReadAll(io.LimitReader(stream.Reader, 8193))
		ce := stream.Reader.Close()
		if e != nil || ce != nil || len(raw) > 8192 || int64(len(raw)) != a.Size || historyHash(string(raw)) != a.SHA256 {
			return nil, ErrCopilotPlanUnavailable
		}
		base = raw
		out.ArtifactID = a.ArtifactID
		out.Path = a.Name
		out.SnapshotOnly = true
	} else {
		if in.SourceFile == nil || state.sources == nil {
			return nil, ErrCopilotPlanUnavailable
		}
		index := -1
		for i, f := range state.sources.selection {
			if f == *in.SourceFile {
				index = i
			}
		}
		if index < 0 {
			return nil, ErrCopilotPlanUnavailable
		}
		file, err := state.sources.read(ctx, index)
		if err != nil {
			return nil, err
		}
		found := false
		for _, e := range parent.Evidence {
			if e.Kind == "source-file" && e.JobRunID == in.SourceFile.JobRunID && e.Path == file.Path && e.Repository == file.Repository && e.ResolvedCommit == file.Commit && e.BlobSHA == file.BlobSHA {
				found = true
			}
		}
		if !found {
			return nil, ErrCopilotPlanUnavailable
		}
		base = file.Content
		out.JobRunID = in.SourceFile.JobRunID
		out.Repository = file.Repository
		out.Commit = file.Commit
		out.Path = file.Path
		out.BlobSHA = file.BlobSHA
	}
	if !utf8.Valid(base) {
		return nil, ErrCopilotPlanUnavailable
	}
	if _, guardErr := s.copilotPlanTextGuard(ctx, authz.Subject{UserID: c.OwnerUserID}, c, parent, state, string(base)); guardErr != nil {
		return nil, ErrCopilotPlanUnavailable
	}
	result, err := verifyCopilotPatch(base, out.Path, in.Patch)
	if err != nil {
		return nil, err
	}
	out.BaseDigest = historyHash(string(base))
	out.PatchDigest = historyHash(in.Patch)
	out.ResultDigest = historyHash(string(result))
	return out, nil
}
func (s *CopilotService) validateSavedCopilotPlan(ctx context.Context, subject authz.Subject, c *model.CopilotConversation, parent *copilotHistoryEnvelope, state *copilotHistoryState, saved copilotPlanEnvelope) error {
	p := saved.Payload
	switch p.Kind {
	case "new_run_review":
		v, b, e := s.buildCopilotNewRun(ctx, c, state)
		if e != nil || b != saved.Binding || !reflect.DeepEqual(v, p.NewRun) {
			return ErrCopilotPlanUnavailable
		}
	case "retry_approval_review":
		if p.RetryApproval == nil {
			return ErrCopilotPlanUnavailable
		}
		v, b, e := s.buildCopilotRetry(ctx, c, state, p.RetryApproval.ApprovalID)
		if e != nil || b != saved.Binding || !reflect.DeepEqual(v, p.RetryApproval) {
			return ErrCopilotPlanUnavailable
		}
	case "deployment_review":
		if p.Deployment == nil {
			return ErrCopilotPlanUnavailable
		}
		v, b, e := s.buildCopilotDeployment(ctx, c, state, CopilotDeploymentReviewInput{EnvironmentID: p.Deployment.EnvironmentID, ArtifactID: p.Deployment.ArtifactID, ReleaseID: p.Deployment.ReleaseID}, false)
		if e != nil || b != saved.Binding {
			return ErrCopilotPlanUnavailable
		}
		v.ImageDigest = p.Deployment.ImageDigest
		if !reflect.DeepEqual(v, p.Deployment) {
			return ErrCopilotPlanUnavailable
		}
	case "code_patch":
		currentBinding, guardErr := s.copilotPlanTextGuard(ctx, subject, c, parent, state, "")
		if guardErr != nil || currentBinding != saved.Binding {
			return ErrCopilotPlanUnavailable
		}
		if p.CodePatch == nil || historyHash(p.CodePatch.Patch) != p.CodePatch.PatchDigest {
			return ErrCopilotPlanUnavailable
		}
		if p.CodePatch.ArtifactID != "" {
			if state.artifactDigests[p.CodePatch.ArtifactID] != p.CodePatch.BaseDigest {
				return ErrCopilotPlanUnavailable
			}
		} else {
			found := false
			for _, e := range parent.Evidence {
				if e.Kind == "source-file" && e.JobRunID == p.CodePatch.JobRunID && e.Repository == p.CodePatch.Repository && e.Path == p.CodePatch.Path && e.ResolvedCommit == p.CodePatch.Commit && e.BlobSHA == p.CodePatch.BlobSHA {
					found = true
				}
			}
			if !found {
				return ErrCopilotPlanUnavailable
			}
		}
	default:
		return ErrCopilotPlanUnavailable
	}
	return nil
}

// The whole typed payload and verified base are checked against current own and
// inherited secret sets. Only opaque state bindings survive this request.
func (s *CopilotService) copilotPlanRedactionState(ctx context.Context, subject authz.Subject, c *model.CopilotConversation, parent *copilotHistoryEnvelope, state *copilotHistoryState) ([]*copilot.Redactor, string, error) {
	redactors := append([]*copilot.Redactor{state.redactor}, state.planRedactors...)
	checked, err := s.redactHistoryDependencies(ctx, subject, c, copilotHistoryEnvelope{History: parent.History}, map[string]bool{}, &redactors)
	if err != nil {
		return nil, "", ErrCopilotPlanUnavailable
	}
	binding := historyMAC(s.historyCipher.bindingKey, append([]string{"plan-redaction-v1", state.stableSeal}, checked.dependencySeals...)...)
	if state.planRedactionBinding != "" && state.planRedactionBinding != binding {
		return nil, "", ErrCopilotPlanUnavailable
	}
	return redactors, binding, nil
}
func (s *CopilotService) copilotPlanTextGuard(ctx context.Context, subject authz.Subject, c *model.CopilotConversation, parent *copilotHistoryEnvelope, state *copilotHistoryState, text string) (string, error) {
	redactors, binding, err := s.copilotPlanRedactionState(ctx, subject, c, parent, state)
	if err != nil {
		return "", err
	}
	for _, r := range redactors {
		if r.Redact(text) != text {
			return "", ErrCopilotPlanUnavailable
		}
	}
	return binding, nil
}

// Inspect decoded typed string values, not their JSON escaping. Secrets containing
// quotes, slashes, HTML characters or Unicode separators must remain detectable.
func (s *CopilotService) copilotPlanPayloadGuard(ctx context.Context, subject authz.Subject, c *model.CopilotConversation, parent *copilotHistoryEnvelope, state *copilotHistoryState, payload CopilotPlanPayload) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return ErrCopilotPlanUnavailable
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return ErrCopilotPlanUnavailable
	}
	redactors, _, err := s.copilotPlanRedactionState(ctx, subject, c, parent, state)
	if err != nil {
		return err
	}
	var safe func(any) bool
	safe = func(value any) bool {
		switch v := value.(type) {
		case string:
			for _, r := range redactors {
				if r.Redact(v) != v {
					return false
				}
			}
		case []any:
			for _, item := range v {
				if !safe(item) {
					return false
				}
			}
		case map[string]any:
			for key, item := range v {
				if !safe(key) || !safe(item) {
					return false
				}
			}
		}
		return true
	}
	if !safe(decoded) {
		return ErrCopilotPlanUnavailable
	}
	return nil
}
