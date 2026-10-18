// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package repo

import (
	"context"
	"fmt"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func retryApprovalField(k string) bool {
	return strings.HasPrefix(strings.ReplaceAll(strings.ToLower(k), "_", ""), "retryapproval")
}
func emptyRetryApproval(j *model.JobRun) bool {
	return !j.RetryApprovalRequired && j.RetryApprovalID == "" && j.RetryApprovalEvidenceHash == ""
}
func retryApprovalEqual(a, b *model.JobRun) bool {
	return a.RetryApprovalRequired == b.RetryApprovalRequired && a.RetryApprovalID == b.RetryApprovalID && a.RetryApprovalEvidenceHash == b.RetryApprovalEvidenceHash
}
func rejectRetryApprovalRunUpdates(updates map[string]any) error {
	for k := range updates {
		if retryApprovalField(k) {
			return fmt.Errorf("retry approval run context is immutable")
		}
	}
	return nil
}

// CreateRetryApprovalRun 通过专用入口保存已验证的可信手动运行上下文
func (r *PipelineRepo) CreateRetryApprovalRun(ctx context.Context, run *model.PipelineRun) error {
	if run == nil || !emptyRunInboxBinding(run) || run.ProjectID != "" || run.PollingContext != "" || run.PollingContextDigest != "" || run.TriggeredBy == "polling" {
		return gorm.ErrInvalidData
	}
	c, err := retryapproval.ParseRunContext(run.RetryApprovalContext)
	if err != nil {
		return err
	}
	d, err := c.Digest()
	if err != nil || d != run.RetryApprovalContextDigest || c.PipelineID != run.PipelineID || c.PipelineRunID != run.RunID || c.ActorID != run.TriggeredBy || c.DefinitionCommitSHA != run.DefinitionCommitSha || c.DefinitionPath != run.DefinitionPath || run.TriggerType != 1 || run.Status != model.PipelineStatusPending {
		return fmt.Errorf("trusted retry approval context does not match pending run")
	}
	if err := normalizeJSONFields(map[string]*string{"env": &run.Env}, pipelineRunJSONDefaults); err != nil {
		return err
	}
	return r.Database().WithContext(ctx).Create(run).Error
}

func retryApprovalContextForCapture(ctx context.Context, tx *gorm.DB, job *model.JobRun) (*retryapproval.RunContext, error) {
	var run model.PipelineRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("run_id", "pipeline_id", "triggered_by", "definition_commit_sha", "definition_path", "retry_approval_context", "retry_approval_context_digest").Where("run_id = ?", job.PipelineRunID).Find(&run).Error; err != nil {
		return nil, err
	}
	if run.RetryApprovalContext == "" && run.RetryApprovalContextDigest == "" {
		return nil, nil
	}
	c, err := retryapproval.ParseRunContext(run.RetryApprovalContext)
	if err != nil {
		return nil, err
	}
	d, err := c.Digest()
	if err != nil || d != run.RetryApprovalContextDigest || c.PipelineRunID != job.PipelineRunID || c.PipelineID != job.PipelineID || run.PipelineID != job.PipelineID || c.ActorID != run.TriggeredBy || c.DefinitionCommitSHA != run.DefinitionCommitSha || c.DefinitionPath != run.DefinitionPath {
		return nil, fmt.Errorf("stored trusted retry approval context is invalid")
	}
	return c, nil
}

// CaptureRuntimeRetryApprovalExpectation 固定完整重试派发，门禁由可信运行上下文决定
func (r *JobRunRepo) CaptureRuntimeRetryApprovalExpectation(ctx context.Context, pipelineID, runID, jobID, agentID string, source *sourceprovenance.Binding, runtime *taskqueue.RuntimeRequest, uris map[string]string, refs map[string]taskqueue.ArtifactReference, plan *taskqueue.JobRetryPlan) error {
	return r.CaptureRuntimeRetryExpectation(ctx, pipelineID, runID, jobID, agentID, source, runtime, uris, refs, plan)
}
