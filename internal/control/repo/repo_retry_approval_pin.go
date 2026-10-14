// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// pinRetryApproval 在固定输入的同一事务创建唯一审批，绝不延长已有截止时间
func pinRetryApproval(ctx context.Context, tx *gorm.DB, job *model.JobRun) error {
	c, err := retryApprovalContextForCapture(ctx, tx, job)
	if err != nil {
		return err
	}
	required := c != nil && c.RequiresJob(job.JobName)
	if required != job.RetryApprovalRequired {
		return fmt.Errorf("retry approval required marker mismatch")
	}
	if !required {
		if !emptyRetryApproval(job) {
			return fmt.Errorf("unexpected retry approval binding")
		}
		return nil
	}
	if job.InputManifestState != "pinned" {
		return fmt.Errorf("approval requires pinned inputs")
	}
	if job.RetryApprovalID != "" || job.RetryApprovalEvidenceHash != "" {
		_, _, err := validateRetryApprovalBinding(ctx, tx, job)
		return err
	}
	plan, err := retryPlanForJob(job)
	if err != nil {
		return err
	}
	inputs, err := taskqueue.ParseJobInputManifest(job.InputManifest)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	expiry := now.Add(time.Duration(c.Policy.TimeoutSeconds) * time.Second)
	if job.Timeout != "" {
		d, e := time.ParseDuration(job.Timeout)
		if e != nil || d <= 0 || job.StartTime == nil {
			return fmt.Errorf("invalid original job deadline")
		}
		if deadline := job.StartTime.Add(d).UTC().Truncate(time.Millisecond); deadline.Before(expiry) {
			expiry = deadline
		}
	}
	// 制品有效期也是不可延长的执行上界，避免后续锁等待穿过早先输入的截止时间
	for _, artifact := range inputs.Artifacts {
		if artifact.Reference.ExpiresAt > 0 {
			deadline := time.Unix(artifact.Reference.ExpiresAt, 0).UTC()
			if deadline.Before(expiry) {
				expiry = deadline
			}
		}
	}
	if !expiry.After(now) {
		return fmt.Errorf("original job deadline expired")
	}
	id := uuid.NewString()
	ev := &retryapproval.Evidence{Version: 1, Kind: retryapproval.Kind, ApprovalID: id, Context: *c, Identity: generationIdentity(job), JobName: job.JobName, RetryPlan: *plan, RetryPlanDigest: job.RetryPlanDigest, Runtime: taskqueue.RuntimeRequest{Type: job.RuntimeType, Image: job.RuntimeImage, Resources: taskqueue.RuntimeResources{CPU: job.RuntimeCPU, Memory: job.RuntimeMemory}, BindingDigest: job.RuntimeBindingDigest}, Inputs: *inputs, InputManifestDigest: job.InputManifestDigest, Artifacts: make([]retryapproval.Artifact, 0, len(inputs.Artifacts)), CreatedAtUnixMilli: now.UnixMilli(), ExpiresAtUnixMilli: expiry.UnixMilli()}
	if job.SourceBindingDigest != "" {
		ev.Source, err = sourceprovenance.NewBinding(job.PipelineID, job.PipelineRunID, job.JobRunID, job.SourceType, job.SourceRepository, job.SourceRef)
		if err != nil {
			return err
		}
	}
	for _, f := range inputs.Artifacts {
		var rows []model.ProjectArtifact
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("project_id", "pipeline_id", "pipeline_run_id", "job_run_id", "step_run_id", "name", "storage_id", "storage_fingerprint", "storage_path", "sha256", "size", "expires_at").Where("pipeline_run_id = ? AND storage_path = ?", job.PipelineRunID, f.Key).Limit(2).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != 1 {
			return fmt.Errorf("input producer is ambiguous")
		}
		a := rows[0]
		ev.Artifacts = append(ev.Artifacts, retryapproval.Artifact{Name: f.Name, ProducerJobRunID: a.JobRunID, ProducerStepRunID: a.StepRunID, StorageKey: f.Key, Reference: f.Reference})
	}
	encoded, err := ev.Canonical()
	if err != nil {
		return err
	}
	hash, err := ev.Digest()
	if err != nil {
		return err
	}
	candidates, err := sonic.MarshalString(c.Policy.Candidates)
	if err != nil {
		return err
	}
	req := &model.ApprovalRequest{ApprovalID: id, RetryJobRunID: &job.JobRunID, BindingKind: retryapproval.Kind, EvidenceJSON: encoded, EvidenceHash: hash, Version: 1, PipelineRunID: job.PipelineRunID, JobName: job.JobName, Plugin: retryapproval.Plugin, Status: model.ApprovalStatusPending, PolicyID: c.Policy.ID, Mode: c.Policy.Mode, RequiredApproverCount: c.Policy.RequiredApproverCount, CandidateApprovers: candidates, RequestedBy: c.ActorID, ExpiresAt: &expiry}
	req.CreatedAt = now
	req.UpdatedAt = now
	if _, err := model.ApprovalCandidates(req); err != nil {
		return err
	}
	if !time.Now().Before(expiry) {
		return fmt.Errorf("approval deadline expired during input pin")
	}
	if err := tx.WithContext(ctx).Create(req).Error; err != nil {
		return err
	}
	result := reportedJobQuery(tx.Model(&model.JobRun{}), job).Updates(map[string]any{"retry_approval_id": id, "retry_approval_evidence_hash": hash, "updated_at": now})
	if err := reportedStatusResult(result); err != nil {
		return err
	}
	job.RetryApprovalID = id
	job.RetryApprovalEvidenceHash = hash
	if !time.Now().Before(expiry) {
		return fmt.Errorf("approval deadline expired during input pin")
	}
	return nil
}
