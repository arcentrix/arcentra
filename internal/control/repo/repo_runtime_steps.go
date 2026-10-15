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
	runtimeJobs := query.Session(&gorm.Session{NewDB: true}).Model(&model.JobRun{}).Select("job_run_id").Where("runtime_type <> ? OR runtime_image <> ? OR runtime_cpu <> ? OR runtime_memory <> ? OR runtime_binding_digest <> ?", "", "", "", "", "")
	return query.Where("job_run_id NOT IN (?)", runtimeJobs)
}

// ValidateRuntimeStepClaim 原子确认运行时步骤属于正在执行的原作业认领
func (r *StepRunRepo) ValidateRuntimeStepClaim(ctx context.Context, expected *model.StepRun, claim string, next int) error {
	return r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error { return validateRuntimeStepClaim(ctx, tx, expected, claim, next) })
}

func validateRuntimeStepClaim(ctx context.Context, tx *gorm.DB, expected *model.StepRun, claim string, next int) error {
	if expected == nil {
		return fmt.Errorf("step snapshot is required")
	}
	if expected.JobRunID == "" {
		return nil
	}
	var parent model.JobRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(jobSnapshotColumns).Where("job_run_id = ?", expected.JobRunID).First(&parent).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		} // 历史独立步骤可能没有父作业
		return err
	}
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
