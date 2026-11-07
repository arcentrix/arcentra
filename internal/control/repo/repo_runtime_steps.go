// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package repo

import (
	"context"
	"errors"
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ExcludeRuntimeSteps 禁止旧单步派发接口读取需要完整容器作业的命令及环境
func ExcludeRuntimeSteps(query *gorm.DB) *gorm.DB {
	runtimeJobs := query.Session(&gorm.Session{NewDB: true}).Model(&model.JobRun{}).Select("job_run_id").Where("runtime_type <> ? OR runtime_image <> ? OR runtime_cpu <> ? OR runtime_memory <> ? OR runtime_binding_digest <> ? OR copilot_rerun_intent_id <> ? OR copilot_rerun_gate_id <> ? OR expected_source_commit <> ?", "", "", "", "", "", "", "", "")
	runs := query.Session(&gorm.Session{NewDB: true}).Model(&model.PipelineRun{}).Select("run_id").Where("copilot_rerun_intent_id <> ? OR copilot_rerun_gate_id <> ?", "", "")
	return query.Where("job_run_id NOT IN (?)", runtimeJobs).Where("(pipeline_run_id IS NULL OR pipeline_run_id NOT IN (?))", runs)
}

// ValidateRuntimeStepClaim 原子确认运行时步骤属于正在执行的原作业认领
func (r *StepRunRepo) ValidateRuntimeStepClaim(ctx context.Context, expected *model.StepRun, claim string, next int) error {
	return executionAttachmentTransaction(ctx, r.Database(), executionStepJobID(expected), func(tx *gorm.DB) error { return validateRuntimeStepClaim(ctx, tx, expected, claim, next) })
}

func validateRuntimeStepClaim(ctx context.Context, tx *gorm.DB, expected *model.StepRun, claim string, next int) error {
	if expected == nil {
		return fmt.Errorf("step snapshot is required")
	}
	if expected.JobRunID == "" {
		return lockOrdinaryRerunParent(ctx, tx, expected.PipelineRunID)
	}
	mode := executionLockLegacy
	if model.IsJobRunTerminal(expected.Status) && next != model.JobRunStatusRunning {
		mode = executionLockInspect
	}
	// Missing-parent legacy fallback must lock the expected Run before the Job
	// absence probe; it must never introduce a Job -> Run locking suffix.
	if value, ok := tx.Statement.Settings.Load(executionScopeLocatorKey); ok {
		if locator, valid := value.(executionScopeLocator); valid && errors.Is(locator.jobErr, errExecutionParentMissing) {
			if err := lockOrdinaryRerunParent(ctx, tx, expected.PipelineRunID); err != nil {
				return err
			}
		}
	}
	var locked *model.JobRun
	var lockErr error
	if _, protected := rerunProtectedLocator(tx); protected {
		locked, lockErr = lockCopilotRerunStepOperation(ctx, tx, expected, claim, next)
	} else {
		locked, lockErr = lockExecutionJob(ctx, tx, expected.JobRunID, mode)
	}
	if err := lockErr; err != nil {
		if errors.Is(err, errExecutionParentMissing) {
			return requireRerunAttachmentTransaction(tx) // Both absent parent keys remain protected until commit.
		} // 历史独立步骤可能没有父作业
		return err
	}
	parent := *locked
	if emptyRuntime(&parent) {
		return nil
	}
	if parent.JobRunID != expected.JobRunID || parent.PipelineID != expected.PipelineID || parent.PipelineRunID != expected.PipelineRunID || parent.AgentID != expected.AgentID || parent.ClaimID == "" || claim != agentidentity.StepClaimID(parent.ClaimID, expected.StepRunID) || !parent.SourceExpectationCaptured {
		return fmt.Errorf("runtime step requires the exact original parent execution claim")
	}
	runtime := &taskqueue.RuntimeRequest{Type: parent.RuntimeType, Image: parent.RuntimeImage, Resources: taskqueue.RuntimeResources{CPU: parent.RuntimeCPU, Memory: parent.RuntimeMemory}}
	digest, err := taskqueue.RuntimeBindingDigest(parent.PipelineID, parent.PipelineRunID, parent.JobRunID, parent.AgentID, runtime)
	if err != nil || digest != parent.RuntimeBindingDigest {
		return fmt.Errorf("runtime parent binding is invalid")
	}
	if next == model.JobRunStatusRunning && parent.Status != model.JobRunStatusRunning {
		return fmt.Errorf("runtime step activation requires a running parent job")
	}
	if err := validateGenerationStep(ctx, tx, &parent, expected, next); err != nil {
		return err
	}
	var current model.StepRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("step_run_id", "job_run_id", "pipeline_id", "pipeline_run_id", "agent_id", "claim_id", "status", "generation_id", "generation_binding_digest", "attempt_admission_request", "attempt_admission_request_digest", "attempt_admission").Where("step_run_id = ?", expected.StepRunID).First(&current).Error; err != nil {
		return err
	}
	if !sameStepAdmission(&current, expected) || current.StepRunID != expected.StepRunID || current.JobRunID != expected.JobRunID || current.PipelineID != expected.PipelineID || current.PipelineRunID != expected.PipelineRunID || current.AgentID != expected.AgentID || current.ClaimID != expected.ClaimID || current.Status != expected.Status || current.GenerationID != expected.GenerationID || current.GenerationBindingDigest != expected.GenerationBindingDigest {
		return fmt.Errorf("runtime step ownership changed")
	}
	return nil
}
