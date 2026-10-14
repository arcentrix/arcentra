// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package repo

import (
	"context"
	"fmt"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"gorm.io/gorm"
)

// 新取消回执必须绑定当前或已终止的准确代次；它不是活动执行准入
// 已有准确回执由服务先校验并重放，不经过此首次写入守卫
func validateGenerationCancellationReceipt(ctx context.Context, tx *gorm.DB, job *model.JobRun, step *model.StepRun) error {
	proof := agentidentity.GenerationReportFromContext(ctx)
	if job.RetryPlan == "" && job.RetryPlanDigest == "" {
		if proof.ID != "" || proof.BindingDigest != "" {
			return fmt.Errorf("non-generation cancellation cannot acquire generation evidence")
		}
		return nil
	}
	plan, err := retryPlanForJob(job)
	if err != nil {
		return err
	}
	if job.Status != model.JobRunStatusCancelled || job.ClaimID == "" || job.CancellationClaimID != job.ClaimID {
		return fmt.Errorf("original controller cancellation is required")
	}
	if step != nil {
		if step.JobRunID != job.JobRunID || step.PipelineID != job.PipelineID || step.PipelineRunID != job.PipelineRunID || step.AgentID != job.AgentID || step.CancellationClaimID != job.ClaimID {
			return fmt.Errorf("cancellation step parent identity changed")
		}
		found := false
		for _, planned := range plan.Steps {
			if step.Attempt >= 1 && step.Attempt <= len(planned.AttemptRunIDs) && planned.AttemptRunIDs[step.Attempt-1] == step.StepRunID && planned.Name == step.Name && int(planned.StepIndex) == step.StepIndex {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("cancellation step is outside the approved retry plan")
		}
	}
	if job.CurrentGenerationID == "" {
		if job.CurrentGeneration != 0 || proof.ID != "" || proof.BindingDigest != "" {
			return fmt.Errorf("pre-admission cancellation requires empty generation evidence")
		}
		if step != nil && (step.GenerationID != "" || step.GenerationBindingDigest != "" || step.ClaimID != "") {
			return fmt.Errorf("pre-admission step cannot claim executed generation")
		}
		return nil
	}
	record, g, err := loadGeneration(ctx, tx, job.CurrentGenerationID)
	if err != nil {
		return err
	}
	if !generationMatchesJob(g, job) || g.Number != job.CurrentGeneration || proof.ID != g.GenerationID || proof.BindingDigest != g.BindingDigest || (record.State != "preparing" && record.State != "active" && record.State != "aborted") {
		return fmt.Errorf("fresh cancellation requires current generation evidence")
	}
	if step == nil {
		return nil
	}
	if step.GenerationID == "" && step.GenerationBindingDigest == "" {
		if step.ClaimID != "" || step.Attempt < g.Number {
			return fmt.Errorf("only unused current or future attempts may close under current generation")
		}
		return nil
	}
	if step.GenerationID != g.GenerationID || step.GenerationBindingDigest != g.BindingDigest || step.Attempt != g.Number || step.ClaimID != agentidentity.StepClaimID(job.ClaimID, step.StepRunID) {
		return fmt.Errorf("started cancellation step requires its stored current-generation claim")
	}
	return nil
}
