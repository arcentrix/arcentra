// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package repo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func validateGenerationStep(ctx context.Context, tx *gorm.DB, job *model.JobRun, step *model.StepRun, next int) error {
	proof := agentidentity.GenerationReportFromContext(ctx)
	if job.RetryPlan == "" {
		if job.RetryApprovalRequired || job.RetryApprovalID != "" || job.RetryApprovalEvidenceHash != "" {
			return fmt.Errorf("required approval retry plan is missing")
		}
		if proof.ID != "" || proof.BindingDigest != "" || agentidentity.StepAttemptReportFromContext(ctx) != "" {
			return fmt.Errorf("non-generation step cannot acquire generation authority")
		}
		return nil
	}
	// 历史准确终态由完整回执摘要校验，不能被误认为新的 RUNNING 权限
	if model.IsJobRunTerminal(step.Status) && next != model.JobRunStatusRunning {
		return nil
	}
	plan, err := retryPlanForJob(job)
	if err != nil {
		return err
	}
	if plan.Policy.Mode == "failed_step" {
		return validateFailedStepReport(ctx, tx, job, step, plan, next)
	}
	if agentidentity.StepAttemptReportFromContext(ctx) != "" {
		return fmt.Errorf("whole_job reports cannot carry step attempt admission")
	}
	var expected *taskqueue.RetryStepPlan
	for index := range plan.Steps {
		p := &plan.Steps[index]
		if step.Attempt >= 1 && step.Attempt <= len(p.AttemptRunIDs) && p.AttemptRunIDs[step.Attempt-1] == step.StepRunID {
			expected = p
			break
		}
	}
	if expected == nil || expected.Name != step.Name || int(expected.StepIndex) != step.StepIndex || expected.Uses != step.Uses || expected.Action != step.Action || (step.ContinueOnError != 0) != expected.ContinueOnError {
		return fmt.Errorf("step is outside immutable generation plan")
	}
	if err := validateInputExecution(ctx, tx, job, generationIdentity(job)); err != nil {
		return err
	}
	if job.CurrentGenerationID == "" {
		if unusedGenerationClosure(next) && step.Status < 3 && step.ClaimID == "" && proof.ID == "" && proof.BindingDigest == "" {
			return nil
		}
		return fmt.Errorf("generation admission is required before execution")
	}
	if proof.ID != job.CurrentGenerationID || proof.BindingDigest == "" {
		return fmt.Errorf("step report requires the current generation")
	}
	record, g, err := loadGeneration(ctx, tx, job.CurrentGenerationID)
	if err != nil {
		return err
	}
	if g.BindingDigest != proof.BindingDigest || !generationMatchesJob(g, job) || g.Number != job.CurrentGeneration || (record.State != "active" && record.State != "preparing") {
		return fmt.Errorf("step generation identity changed")
	}
	if step.Attempt != g.Number {
		if !unusedGenerationClosure(next) || step.Status >= 3 || step.ClaimID != "" || step.Attempt < g.Number {
			return fmt.Errorf("only current round commands can execute")
		}
	}
	if step.GenerationID != "" && (step.GenerationID != g.GenerationID || step.GenerationBindingDigest != g.BindingDigest) {
		return fmt.Errorf("step already belongs to another generation")
	}
	if next == model.JobRunStatusRunning {
		if err := validateGenerationLive(ctx, tx, job, generationIdentity(job)); err != nil {
			return err
		}
		digest := g.InputManifestDigest
		if g.Number == 1 && digest == "" {
			digest = job.InputManifestDigest
		}
		if err := validateGenerationInputs(job, digest, false); err != nil {
			return err
		}
		if err := validateRetryApprovalExecution(ctx, tx, job, true); err != nil {
			return err
		}
		if time.Now().UnixMilli() < g.NotBeforeUnixMilli {
			return fmt.Errorf("generation delay has not elapsed")
		}
	}
	return nil
}

func validateGenerationStepUpdates(ctx context.Context, expected *model.StepRun, updates map[string]any) error {
	proof := agentidentity.GenerationReportFromContext(ctx)
	for key := range updates {
		switch key {
		case "status", "error_message", "start_time", "end_time", "duration", "claim_id", "terminal_report_digest", "exit_code", "workspace", "generation_id", "generation_binding_digest", "retryable_shell_exit", "updated_at":
		default:
			return fmt.Errorf("step report cannot alter immutable execution plan")
		}
		normalized := strings.ReplaceAll(strings.ToLower(key), "_", "")
		if generationField(key) && key != "generation_id" && key != "generation_binding_digest" {
			return fmt.Errorf("generation fields require dedicated validation")
		}
		if normalized == "retryableshellexit" && key != "retryable_shell_exit" {
			return fmt.Errorf("retry evidence must use canonical fields")
		}
	}
	id, _ := updates["generation_id"].(string)
	binding, _ := updates["generation_binding_digest"].(string)
	if id != proof.ID || binding != proof.BindingDigest {
		return fmt.Errorf("step generation update does not match report")
	}
	if expected.GenerationID != "" && (id != expected.GenerationID || binding != expected.GenerationBindingDigest) {
		return fmt.Errorf("step generation is immutable")
	}
	eligible, _ := updates["retryable_shell_exit"].(bool)
	if eligible {
		next, _ := updates["status"].(int)
		code, ok := updates["exit_code"].(int32)
		if !ok {
			if n, yes := updates["exit_code"].(int); yes {
				code, ok = int32(n), true
			}
		}
		if expected.Status != model.JobRunStatusRunning || expected.ClaimID == "" || next != model.JobRunStatusFailed || !ok || code < 1 || code > 125 || expected.ContinueOnError != 0 || (expected.Uses != "shell" && expected.Uses != "builtin:shell") || id == "" {
			return fmt.Errorf("pure shell failure evidence requires an active failed generation step")
		}
	}
	return nil
}

func activateClaimedGeneration(ctx context.Context, tx *gorm.DB, next int) error {
	proof := agentidentity.GenerationReportFromContext(ctx)
	if next != model.JobRunStatusRunning || proof.ID == "" {
		return nil
	}
	var record model.JobRunGeneration
	if err := tx.WithContext(ctx).Select("job_run_id").Where("generation_id = ?", proof.ID).First(&record).Error; err != nil {
		return err
	}
	var job model.JobRun
	if err := tx.WithContext(ctx).Select(jobSnapshotColumns).Where("job_run_id = ?", record.JobRunID).First(&job).Error; err != nil {
		return err
	}
	if err := validateRetryApprovalCommit(ctx, tx, &job, next); err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Model(&model.JobRunGeneration{}).Where("generation_id = ? AND state = ?", proof.ID, "preparing").Updates(map[string]any{"state": "active", "updated_at": time.Now()}).Error; err != nil {
		return err
	}
	return validateRetryApprovalCommit(ctx, tx, &job, next)
}

func validateGenerationJobReport(ctx context.Context, tx *gorm.DB, job *model.JobRun, next int) error {
	proof := agentidentity.GenerationReportFromContext(ctx)
	if job.RetryPlan == "" {
		if job.RetryApprovalRequired || job.RetryApprovalID != "" || job.RetryApprovalEvidenceHash != "" {
			return fmt.Errorf("required approval retry plan is missing")
		}
		if proof.ID != "" || proof.BindingDigest != "" {
			return fmt.Errorf("non-generation job cannot acquire generation authority")
		}
		return nil
	}
	if _, err := retryPlanForJob(job); err != nil {
		return err
	}
	if model.IsJobRunTerminal(next) && next != model.JobRunStatusSuccess {
		if err := cancelPendingRetryApproval(ctx, tx, job, "job execution ended"); err != nil {
			return err
		}
	}
	if job.CurrentGenerationID == "" {
		if proof.ID != "" || proof.BindingDigest != "" || next == model.JobRunStatusSuccess {
			return fmt.Errorf("job result requires an admitted generation")
		}
		return nil
	}
	if proof.ID != job.CurrentGenerationID || proof.BindingDigest == "" {
		return fmt.Errorf("job result requires active generation")
	}
	record, g, err := loadGeneration(ctx, tx, job.CurrentGenerationID)
	if err != nil {
		return err
	}
	if g.BindingDigest != proof.BindingDigest || (record.State != "active" && record.State != "preparing") || g.Number != job.CurrentGeneration || !generationMatchesJob(g, job) {
		return fmt.Errorf("job generation binding mismatch")
	}
	if err := validateInputExecution(ctx, tx, job, generationIdentity(job)); err != nil {
		return err
	}
	if next == model.JobRunStatusSuccess {
		if err := validateRetryApprovalExecution(ctx, tx, job, true); err != nil {
			return err
		}
		digest := g.InputManifestDigest
		if g.Number == 1 && digest == "" {
			digest = job.InputManifestDigest
		}
		if err := validateGenerationInputs(job, digest, false); err != nil {
			return err
		}
		plan, err := retryPlanForJob(job)
		if err != nil {
			return err
		}
		if plan.Policy.Mode == "failed_step" {
			return validateFailedStepSuccess(ctx, tx, job, g, plan)
		}
		if record.State == "preparing" {
			if err := validateSkippedPreparingGeneration(ctx, tx, job, g); err != nil {
				return err
			}
		}
	}
	return nil
}

func rejectStepGenerationUpdates(updates map[string]any) error {
	for key := range updates {
		normalized := strings.ReplaceAll(strings.ToLower(key), "_", "")
		if normalized == "jobrunid" || normalized == "steprunid" {
			return fmt.Errorf("generic step mutation cannot change allocation identity")
		}
		if stepAttemptField(key) || generationField(key) || strings.ReplaceAll(strings.ToLower(key), "_", "") == "retryableshellexit" {
			return fmt.Errorf("generation evidence requires atomic authenticated report")
		}
	}
	return nil
}

func validateGenerationArtifact(ctx context.Context, tx *gorm.DB, job *model.JobRun, stepID string) error {
	if job.RetryPlan == "" {
		if job.RetryApprovalRequired || job.RetryApprovalID != "" || job.RetryApprovalEvidenceHash != "" {
			return fmt.Errorf("required approval retry plan is missing")
		}
		return nil
	}
	proof := agentidentity.GenerationReportFromContext(ctx)
	if proof.ID != job.CurrentGenerationID || proof.ID == "" {
		return fmt.Errorf("generation output requires authenticated current-generation publication")
	}
	if err := validateGenerationJobReport(ctx, tx, job, model.JobRunStatusSuccess); err != nil {
		return err
	}
	plan, err := retryPlanForJob(job)
	if err != nil {
		return err
	}
	if plan.Policy.Mode == "failed_step" {
		return validateFailedStepArtifact(ctx, tx, job, stepID)
	}
	var step model.StepRun
	if err := tx.WithContext(ctx).Select("step_run_id", "job_run_id", "pipeline_id", "pipeline_run_id", "agent_id", "attempt", "generation_id", "generation_binding_digest", "status", "terminal_report_digest").Where("step_run_id = ?", stepID).First(&step).Error; err != nil {
		return err
	}
	if step.StepRunID != stepID || step.JobRunID != job.JobRunID || step.PipelineID != job.PipelineID || step.PipelineRunID != job.PipelineRunID || step.AgentID != job.AgentID || step.Attempt != job.CurrentGeneration || step.GenerationID != proof.ID || step.GenerationBindingDigest != proof.BindingDigest || step.Status != model.JobRunStatusSuccess || step.TerminalReportDigest == "" {
		return fmt.Errorf("output step is not a successful current-generation attempt")
	}
	return nil
}

func unusedGenerationClosure(next int) bool {
	return next == model.JobRunStatusSkipped || next == model.JobRunStatusCancelled || next == model.JobRunStatusTimeout
}

func excludeRetryPlanSteps(tx *gorm.DB) *gorm.DB {
	jobs := tx.Session(&gorm.Session{NewDB: true}).Model(&model.JobRun{}).Select("job_run_id").Where("retry_plan <> ? OR retry_plan_digest <> ? OR copilot_rerun_intent_id <> ? OR copilot_rerun_gate_id <> ? OR expected_source_commit <> ?", "", "", "", "", "")
	runs := tx.Session(&gorm.Session{NewDB: true}).Model(&model.PipelineRun{}).Select("run_id").Where("copilot_rerun_intent_id <> ? OR copilot_rerun_gate_id <> ?", "", "")
	return tx.Where("job_run_id NOT IN (?)", jobs).Where("(pipeline_run_id IS NULL OR pipeline_run_id NOT IN (?))", runs)
}

type generationArtifactPublicationKey struct{}

func withGenerationArtifactPublication(ctx context.Context) context.Context {
	return context.WithValue(ctx, generationArtifactPublicationKey{}, true)
}
func generationArtifactPublication(ctx context.Context) bool {
	allowed, _ := ctx.Value(generationArtifactPublicationKey{}).(bool)
	return allowed
}

func rejectGenerationArtifactShortcut(expected *model.JobRun, updates map[string]any) error {
	if expected == nil || (expected.RetryPlan == "" && emptyRetryApproval(expected)) {
		return nil
	}
	for key := range updates {
		normalized := strings.ReplaceAll(strings.ToLower(key), "_", "")
		if normalized == "artifacturis" || normalized == "artifactreferences" {
			return fmt.Errorf("generation artifact results require atomic final publication")
		}
	}
	return nil
}

// preparing 只可能在未提交任何 RUNNING 准入时存在，SKIPPED 的 StartTime 不是命令证据
func validateSkippedPreparingGeneration(ctx context.Context, tx *gorm.DB, job *model.JobRun, g *taskqueue.JobGeneration) error {
	if g.Number != 1 {
		return fmt.Errorf("only the initial generation may finish without execution")
	}
	plan, err := retryPlanForJob(job)
	if err != nil {
		return err
	}
	var rows []model.StepRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("step_run_id", "job_run_id", "pipeline_id", "pipeline_run_id", "agent_id", "claim_id", "attempt", "name", "step_index", "status", "generation_id", "generation_binding_digest", "terminal_report_digest", "retryable_shell_exit").Where("job_run_id = ? AND attempt = ?", job.JobRunID, g.Number).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != len(plan.Steps) {
		return fmt.Errorf("unexecuted generation requires the complete skipped round")
	}
	byID := make(map[string]model.StepRun, len(rows))
	for _, step := range rows {
		byID[step.StepRunID] = step
	}
	for _, planned := range plan.Steps {
		id := planned.AttemptRunIDs[g.Number-1]
		step, exists := byID[id]
		if !exists || step.StepRunID != id || step.JobRunID != job.JobRunID || step.PipelineID != job.PipelineID || step.PipelineRunID != job.PipelineRunID || step.AgentID != job.AgentID || step.ClaimID != agentidentity.StepClaimID(job.ClaimID, id) || step.Attempt != g.Number || step.Name != planned.Name || step.StepIndex != int(planned.StepIndex) || step.Status != model.JobRunStatusSkipped || step.GenerationID != g.GenerationID || step.GenerationBindingDigest != g.BindingDigest || artifactutil.Digest(step.TerminalReportDigest) != nil || step.RetryableShellExit {
			return fmt.Errorf("preparing generation success requires exact committed skipped attempts")
		}
	}
	return nil
}
