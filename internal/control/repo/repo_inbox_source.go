// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/bytedance/sonic"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var inboxRunColumns = []string{"id", "run_id", "pipeline_id", "project_id", "inbox_actor_id", "inbox_origin", "trigger_type", "status", "start_time", "end_time", "created_at", "updated_at", "definition_commit_sha", "definition_path", "retry_approval_context", "retry_approval_context_digest", "deployment_actor_id", "deployment_plan_hash", "deployment_target_fingerprint", "allow_deployments"}
var inboxApprovalColumns = []string{"id", "approval_id", "pipeline_run_id", "status", "created_at", "updated_at", "version", "requested_by", "candidate_approvers", "mode", "required_approver_count", "expires_at", "retry_job_run_id", "binding_kind", "evidence_json", "evidence_hash", "deployment_release_id", "job_name", "step_name", "plugin", "policy_id", "environment", "callback_url", "inbox_origin"}

func inboxRun(tx *gorm.DB, id string) (*model.PipelineRun, error) {
	var rows []model.PipelineRun
	if err := tx.Select(boundedInboxColumns(inboxRunColumns)).Where("run_id = ?", id).Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != 1 || rows[0].RunID != id || rows[0].InboxOrigin != 1 || !validApprovalID(rows[0].InboxActorID) || rows[0].TriggerType != 1 {
		return nil, ErrInboxSource
	}
	return &rows[0], nil
}
func admittedInboxRun(r *model.PipelineRun) bool {
	return r != nil && validApprovalID(r.ProjectID) && r.StartTime != nil && !r.StartTime.IsZero()
}
func inboxRunStatus(s int) string {
	switch s {
	case model.PipelineStatusSuccess:
		return "success"
	case model.PipelineStatusFailed:
		return "failed"
	case model.PipelineStatusCancelled:
		return "cancelled"
	}
	return ""
}
func inboxApprovalStatus(s int) string {
	switch s {
	case model.ApprovalStatusPending:
		return "pending"
	case model.ApprovalStatusApproved:
		return "approved"
	case model.ApprovalStatusRejected:
		return "rejected"
	case model.ApprovalStatusExpired:
		return "expired"
	case model.ApprovalStatusCanceled:
		return "cancelled"
	}
	return ""
}

// This validator is historical and read-only. In particular it never calls
// lockApproval (which writes version), Expire, or active-execution admission.
func inboxApproval(ctx context.Context, tx *gorm.DB, id string) (*model.ApprovalRequest, *model.PipelineRun, []string, error) {
	var rows []model.ApprovalRequest
	if err := tx.Select(boundedInboxColumns(inboxApprovalColumns)).Where("approval_id = ?", id).Limit(2).Find(&rows).Error; err != nil {
		return nil, nil, nil, err
	}
	if len(rows) != 1 || rows[0].ApprovalID != id {
		return nil, nil, nil, ErrInboxSource
	}
	a := &rows[0]
	run, err := inboxRun(tx, a.PipelineRunID)
	if err != nil {
		return nil, nil, nil, err
	}
	if !admittedInboxRun(run) || a.RequestedBy != run.InboxActorID || a.Version < 1 || inboxApprovalStatus(a.Status) == "" {
		return nil, nil, nil, ErrInboxSource
	}
	candidates, err := model.ApprovalCandidates(a)
	if err != nil {
		return nil, nil, nil, ErrInboxSource
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		key := strings.ToLower(candidate)
		if seen[key] || strings.EqualFold(candidate, a.RequestedBy) {
			return nil, nil, nil, ErrInboxSource
		}
		seen[key] = true
	}
	switch {
	case a.RetryJobRunID != nil && a.DeploymentReleaseID == nil:
		err = validateInboxRetry(tx, a, run, candidates)
	case a.DeploymentReleaseID != nil && a.RetryJobRunID == nil:
		err = validateInboxDeployment(tx, a, run)
	default:
		err = ErrInboxSource
	}
	if err != nil {
		return nil, nil, nil, err
	}
	return a, run, candidates, ctx.Err()
}
func validateInboxRetry(tx *gorm.DB, a *model.ApprovalRequest, r *model.PipelineRun, candidates []string) error {
	if (a.BindingKind != retryapproval.Kind && a.BindingKind != retryapproval.FailedStepKind) || a.Plugin != retryapproval.Plugin || a.ExpiresAt == nil || a.StepName != "" || a.Environment != "" || a.CallbackURL != "" {
		return ErrInboxSource
	}
	c, err := retryapproval.ParseRunContext(r.RetryApprovalContext)
	if err != nil {
		return ErrInboxSource
	}
	digest, err := c.Digest()
	if err != nil || digest != r.RetryApprovalContextDigest || c.ActorID != r.InboxActorID || c.ProjectID != r.ProjectID || c.PipelineID != r.PipelineID || c.PipelineRunID != r.RunID || c.DefinitionCommitSHA != r.DefinitionCommitSha || c.DefinitionPath != r.DefinitionPath {
		return ErrInboxSource
	}
	e, err := retryapproval.ParseEvidence(a.EvidenceJSON)
	if err != nil {
		return ErrInboxSource
	}
	hash, err := e.Digest()
	if err != nil || hash != a.EvidenceHash || e.Kind != a.BindingKind || e.ApprovalID != a.ApprovalID || e.Identity.JobRunID != *a.RetryJobRunID || e.Identity.PipelineRunID != r.RunID || e.Identity.PipelineID != r.PipelineID || e.JobName != a.JobName || e.CreatedAtUnixMilli != a.CreatedAt.UnixMilli() || e.ExpiresAtUnixMilli != a.ExpiresAt.UnixMilli() {
		return ErrInboxSource
	}
	cc, _ := c.Canonical()
	ec, _ := e.Context.Canonical()
	if cc != ec || !slices.Equal(candidates, c.Policy.Candidates) || a.PolicyID != c.Policy.ID || a.Mode != c.Policy.Mode || a.RequiredApproverCount != c.Policy.RequiredApproverCount {
		return ErrInboxSource
	}
	var jobs []model.JobRun
	columns := []string{"job_run_id", "pipeline_id", "pipeline_run_id", "job_name", "agent_id", "claim_id", "runtime_binding_digest", "source_binding_digest", "retry_plan", "retry_plan_digest", "retry_approval_required", "retry_approval_id", "retry_approval_evidence_hash", "input_manifest", "input_manifest_digest", "input_manifest_state", "runtime_type", "runtime_image", "runtime_cpu", "runtime_memory", "source_type", "source_repository", "source_ref", "source_resolved_commit"}
	if err := tx.Select(boundedInboxColumns(columns)).Where("job_run_id = ?", *a.RetryJobRunID).Limit(2).Find(&jobs).Error; err != nil {
		return err
	}
	if len(jobs) != 1 {
		return ErrInboxSource
	}
	j := &jobs[0]
	plan, _ := e.RetryPlan.Canonical()
	inputs, _ := e.Inputs.Canonical()
	if j.JobRunID != *a.RetryJobRunID || j.PipelineRunID != r.RunID || j.PipelineID != r.PipelineID || j.JobName != a.JobName || !j.RetryApprovalRequired || j.RetryApprovalID != a.ApprovalID || j.RetryApprovalEvidenceHash != a.EvidenceHash || e.Identity != generationIdentity(j) || j.RetryPlan != plan || j.RetryPlanDigest != e.RetryPlanDigest || j.InputManifestState != "pinned" || j.InputManifest != inputs || j.InputManifestDigest != e.InputManifestDigest || j.RuntimeType != e.Runtime.Type || j.RuntimeImage != e.Runtime.Image || j.RuntimeCPU != e.Runtime.Resources.CPU || j.RuntimeMemory != e.Runtime.Resources.Memory {
		return ErrInboxSource
	}
	if e.Source == nil {
		if j.SourceType != "" || j.SourceRepository != "" || j.SourceRef != "" {
			return ErrInboxSource
		}
	} else if e.Source.Type != j.SourceType || e.Source.Repository != j.SourceRepository || e.Source.Ref != j.SourceRef {
		return ErrInboxSource
	}
	if j.SourceResolvedCommit != "" && j.SourceResolvedCommit != e.Inputs.SourceCommit {
		return ErrInboxSource
	}
	return nil
}

// InboxDeploymentEvidence is the same pure historical hash as deployment
// execution uses, shared here to avoid consulting mutable live target config.
func InboxDeploymentEvidence(entry *model.DeploymentRelease, req *model.ApprovalRequest) string {
	if entry == nil || req == nil || req.ExpiresAt == nil || entry.ExecutionStepRunID == nil {
		return ""
	}
	candidates, err := model.ApprovalCandidates(req)
	if err != nil {
		return ""
	}
	var target struct {
		ProjectID     string `json:"projectId"`
		EnvironmentID string `json:"environmentId"`
		TargetRef     string `json:"targetRef"`
		Namespace     string `json:"namespace"`
		Deployment    string `json:"deployment"`
		Container     string `json:"container"`
	}
	var fields map[string]any
	if sonic.Unmarshal(entry.ApprovalTarget, &target) != nil || sonic.Unmarshal(entry.ApprovalTarget, &fields) != nil || len(fields) != 6 {
		return ""
	}
	values := []any{"pipeline-deployment-approval-v1", entry.ReleaseID, entry.ProjectID, entry.RequestID, entry.EnvironmentID, entry.PipelineID, entry.PipelineRunID, entry.ExecutionJobRunID, *entry.ExecutionStepRunID, entry.ProducerJobRunID, entry.ProducerStepRunID, entry.DeploymentPlanHash, entry.TargetFingerprint, entry.ArtifactID, entry.ArtifactSHA256, entry.CommitSHA, entry.Image, entry.CreatedBy, target, req.ApprovalID, req.JobName, req.StepName, req.Plugin, candidates, req.RequiredApproverCount, req.Mode, req.RequestedBy, req.ExpiresAt.UTC().UnixMilli()}
	data, err := sonic.Marshal(values)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
func validateInboxDeployment(tx *gorm.DB, a *model.ApprovalRequest, r *model.PipelineRun) error {
	if a.BindingKind != "" || a.Plugin != "builtin:deployment" || a.Mode != "any" || a.ExpiresAt == nil || a.CallbackURL != "" || a.RequestedBy != r.DeploymentActorID || !r.AllowDeployments {
		return ErrInboxSource
	}
	var releases []model.DeploymentRelease
	columns := []string{"release_id", "project_id", "request_id", "environment_id", "pipeline_id", "pipeline_run_id", "execution_job_run_id", "execution_step_run_id", "producer_job_run_id", "producer_step_run_id", "deployment_plan_hash", "target_fingerprint", "artifact_id", "artifact_sha256", "commit_sha", "image", "created_by", "approval_id", "approval_evidence_hash", "approval_target", "origin"}
	if err := tx.Select(boundedInboxColumns(columns)).Where("release_id = ?", *a.DeploymentReleaseID).Limit(2).Find(&releases).Error; err != nil {
		return err
	}
	if len(releases) != 1 {
		return ErrInboxSource
	}
	v := &releases[0]
	if v.ReleaseID != *a.DeploymentReleaseID || v.ProjectID != r.ProjectID || v.PipelineID != r.PipelineID || v.PipelineRunID != r.RunID || v.Origin != "pipeline_job" || v.CreatedBy != r.InboxActorID || v.ApprovalID == nil || *v.ApprovalID != a.ApprovalID || v.ExecutionStepRunID == nil || v.EnvironmentID != a.Environment || v.DeploymentPlanHash != r.DeploymentPlanHash || v.TargetFingerprint != r.DeploymentTargetFingerprint || v.CommitSHA != r.DefinitionCommitSha || v.ApprovalEvidenceHash != a.EvidenceHash || a.EvidenceHash == "" || InboxDeploymentEvidence(v, a) != a.EvidenceHash {
		return ErrInboxSource
	}
	var jobs []model.JobRun
	if err := tx.Select("job_run_id", "pipeline_id", "pipeline_run_id", "job_name").Where("job_run_id = ?", v.ExecutionJobRunID).Limit(2).Find(&jobs).Error; err != nil {
		return err
	}
	if len(jobs) != 1 || jobs[0].JobRunID != v.ExecutionJobRunID || jobs[0].PipelineID != r.PipelineID || jobs[0].PipelineRunID != r.RunID || jobs[0].JobName != a.JobName {
		return ErrInboxSource
	}
	var steps []model.StepRun
	if err := tx.Select("step_run_id", "job_run_id", "pipeline_id", "pipeline_run_id", "name", "created_by", "deployment_release_id").Where("step_run_id = ?", *v.ExecutionStepRunID).Limit(2).Find(&steps).Error; err != nil {
		return err
	}
	if len(steps) != 1 || steps[0].StepRunID != *v.ExecutionStepRunID || steps[0].JobRunID != v.ExecutionJobRunID || steps[0].PipelineID != r.PipelineID || steps[0].PipelineRunID != r.RunID || steps[0].Name != a.StepName || steps[0].CreatedBy != r.InboxActorID || steps[0].DeploymentReleaseID != v.ReleaseID {
		return ErrInboxSource
	}
	return nil
}
func enrollInboxWork(tx *gorm.DB, kind, id string) error {
	if (kind != "run" && kind != "approval") || !validApprovalID(id) {
		return ErrInboxSource
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.InboxWork{SourceKind: kind, SourceID: id, NextCheckAt: time.Now().UTC()}).Error
}

// Enrollment is atomic with a supported source's creation. Legacy runs do not
// enroll; malformed new bindings fail source creation rather than guessing.
func enrollInboxApproval(ctx context.Context, tx *gorm.DB, a *model.ApprovalRequest) error {
	var rows []model.PipelineRun
	if err := tx.Select("run_id", "inbox_actor_id", "inbox_origin").Where("run_id = ?", a.PipelineRunID).Limit(2).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].RunID != a.PipelineRunID {
		return ErrInboxSource
	}
	if rows[0].InboxOrigin == 0 && rows[0].InboxActorID == "" {
		return nil
	}
	if _, _, _, err := inboxApproval(ctx, tx, a.ApprovalID); err != nil {
		return err
	}
	if err := tx.Model(&model.ApprovalRequest{}).Where("approval_id = ?", a.ApprovalID).UpdateColumn("inbox_origin", 1).Error; err != nil {
		return err
	}
	return enrollInboxWork(tx, "approval", a.ApprovalID)
}

// Bound proof material before materializing it. Raw logs/env/reasons are not
// selected at all; oversized/corrupt evidence becomes an invalid fixed source.
func boundedInboxColumns(columns []string) []string {
	out := append([]string(nil), columns...)
	for i, name := range out {
		switch name {
		case "retry_approval_context", "evidence_json", "retry_plan", "input_manifest":
			out[i] = fmt.Sprintf("CASE WHEN LENGTH(%s) <= %d THEN %s ELSE '' END AS %s", name, retryapproval.MaxBytes, name, name)
		case "approval_target":
			out[i] = "CASE WHEN LENGTH(approval_target) <= 8192 THEN approval_target ELSE '' END AS approval_target"
		}
	}
	return out
}
