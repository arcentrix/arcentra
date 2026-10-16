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
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var jobGenerationColumns = []string{"retry_plan", "retry_plan_digest", "current_generation_id", "current_generation"}
var generationColumns = []string{"generation_id", "job_run_id", "number", "state", "generation_json", "admission_request", "admission_digest", "closure_request", "closure_digest", "next_generation_id", "retry_approval_admission", "retry_approval_admission_digest"}

func generationField(key string) bool {
	key = strings.ReplaceAll(strings.ToLower(key), "_", "")
	return strings.HasPrefix(key, "retryplan") || strings.HasPrefix(key, "currentgeneration") || strings.HasPrefix(key, "generation")
}
func emptyJobGeneration(j *model.JobRun) bool {
	return j.RetryPlan == "" && j.RetryPlanDigest == "" && j.CurrentGenerationID == "" && j.CurrentGeneration == 0
}
func jobGenerationEqual(a, b *model.JobRun) bool {
	return a.RetryPlan == b.RetryPlan && a.RetryPlanDigest == b.RetryPlanDigest && a.CurrentGenerationID == b.CurrentGenerationID && a.CurrentGeneration == b.CurrentGeneration
}

// CaptureRuntimeRetryExpectation 原子冻结原运行时、输入、完整重试策略和全部预分配尝试
func (r *JobRunRepo) CaptureRuntimeRetryExpectation(ctx context.Context, pipelineID, runID, jobID, agentID string, source *sourceprovenance.Binding, runtime *taskqueue.RuntimeRequest, uris map[string]string, refs map[string]taskqueue.ArtifactReference, plan *taskqueue.JobRetryPlan) error {
	if runtime == nil || plan == nil {
		return fmt.Errorf("runtime and complete retry plan are required")
	}
	copy := *runtime
	digest, err := taskqueue.RuntimeBindingDigest(pipelineID, runID, jobID, agentID, &copy)
	if err != nil || digest != copy.BindingDigest {
		return fmt.Errorf("invalid runtime dispatch binding")
	}
	inputs, err := taskqueue.NewJobInputManifest("", uris, refs)
	if err != nil {
		return err
	}
	return r.captureDispatchExpectation(ctx, pipelineID, runID, jobID, agentID, source, &copy, inputs, plan)
}

func validateCapturedRetrySteps(ctx context.Context, tx *gorm.DB, job *model.JobRun, agentID string, plan *taskqueue.JobRetryPlan) error {
	var rows []model.StepRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("step_run_id", "name", "step_index", "job_run_id", "pipeline_id", "pipeline_run_id", "agent_id", "attempt", "retry_mode", "retry_policy", "previous_step_run_id", "uses", "action", "continue_on_error", "attempt_admission_request", "attempt_admission_request_digest", "attempt_admission").Where("job_run_id = ?", job.JobRunID).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != len(plan.Steps)*int(plan.Policy.MaxAttempts) {
		return fmt.Errorf("complete preallocated retry plan is required")
	}
	policy, err := sonic.MarshalString(plan.Policy)
	if err != nil {
		return err
	}
	byID := map[string]model.StepRun{}
	for _, row := range rows {
		byID[row.StepRunID] = row
	}
	for _, step := range plan.Steps {
		for index, id := range step.AttemptRunIDs {
			row, ok := byID[id]
			if !ok || !emptyStepAdmission(&row) || row.JobRunID != job.JobRunID || row.PipelineID != job.PipelineID || row.PipelineRunID != job.PipelineRunID || (row.AgentID != "" && row.AgentID != agentID) || row.Name != step.Name || row.StepIndex != int(step.StepIndex) || row.Attempt != index+1 || row.RetryMode != plan.Policy.Mode || !sameRetryPolicy(row.RetryPolicy, policy) || row.Uses != step.Uses || row.Action != step.Action || (row.ContinueOnError != 0) != step.ContinueOnError {
				return fmt.Errorf("retry plan does not match preallocated step identity")
			}
			previous := ""
			if index > 0 {
				previous = step.AttemptRunIDs[index-1]
			}
			if row.PreviousStepRunID != previous {
				return fmt.Errorf("retry predecessor plan mismatch")
			}
		}
	}
	return nil
}

func retryPlanForJob(job *model.JobRun) (*taskqueue.JobRetryPlan, error) {
	plan, err := taskqueue.ParseJobRetryPlan(job.RetryPlan)
	if err != nil {
		return nil, err
	}
	if plan.RetryApprovalRequired != job.RetryApprovalRequired || (!plan.RetryApprovalRequired && !emptyRetryApproval(job)) {
		return nil, fmt.Errorf("retry approval requirement differs from immutable plan")
	}
	digest, err := plan.Digest(job.PipelineID, job.PipelineRunID, job.JobRunID, job.AgentID, job.RuntimeBindingDigest)
	if err != nil || digest != job.RetryPlanDigest {
		return nil, fmt.Errorf("stored retry plan binding is invalid")
	}
	return plan, nil
}
func generationMatchesJob(g *taskqueue.JobGeneration, job *model.JobRun) bool {
	return g != nil && g.PipelineID == job.PipelineID && g.PipelineRunID == job.PipelineRunID && g.JobRunID == job.JobRunID && g.AgentID == job.AgentID && g.OriginalClaimID == job.ClaimID && g.RuntimeBindingDigest == job.RuntimeBindingDigest && g.SourceBindingDigest == job.SourceBindingDigest && g.RetryPlanDigest == job.RetryPlanDigest
}

func generationIdentity(job *model.JobRun) taskqueue.JobInputIdentity {
	return taskqueue.JobInputIdentity{PipelineID: job.PipelineID, PipelineRunID: job.PipelineRunID, JobRunID: job.JobRunID, AgentID: job.AgentID, ClaimID: job.ClaimID, RuntimeBindingDigest: job.RuntimeBindingDigest, SourceBindingDigest: job.SourceBindingDigest}
}

func validateGenerationInputs(job *model.JobRun, digest string, bootstrap bool) error {
	if bootstrap && digest == "" && job.InputManifestState == "unknown" && job.InputManifest == "" && job.InputManifestDigest == "" {
		return nil
	}
	if job.InputManifestState != "pinned" || digest == "" || digest != job.InputManifestDigest {
		return fmt.Errorf("generation requires unchanged pinned inputs")
	}
	manifest, err := taskqueue.ParseJobInputManifest(job.InputManifest)
	if err != nil {
		return err
	}
	want, err := manifest.Digest(generationIdentity(job))
	if err != nil || want != digest {
		return fmt.Errorf("pinned input binding changed")
	}
	return validateInputProposal(job, manifest)
}
func validateGenerationLive(ctx context.Context, tx *gorm.DB, job *model.JobRun, identity taskqueue.JobInputIdentity) error {
	if err := validateInputExecution(ctx, tx, job, identity); err != nil {
		return err
	}
	if job.Timeout != "" {
		duration, err := time.ParseDuration(job.Timeout)
		if err != nil || duration <= 0 {
			return fmt.Errorf("invalid job deadline")
		}
		if job.StartTime == nil || !time.Now().Before(job.StartTime.Add(duration)) {
			return fmt.Errorf("original job deadline expired")
		}
	}
	return nil
}
func loadGeneration(ctx context.Context, tx *gorm.DB, id string) (*model.JobRunGeneration, *taskqueue.JobGeneration, error) {
	return loadGenerationRead(ctx, tx, id, true)
}

// 非锁定读取仅用于已锁定终态父作业的不可执行历史证据
func loadGenerationRead(ctx context.Context, tx *gorm.DB, id string, lock bool) (*model.JobRunGeneration, *taskqueue.JobGeneration, error) {
	var record model.JobRunGeneration
	query := tx.WithContext(ctx)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Select(generationColumns).Where("generation_id = ?", id).First(&record).Error; err != nil {
		return nil, nil, err
	}
	descriptor, err := taskqueue.ParseJobGeneration(record.GenerationJSON)
	if err != nil || record.GenerationID != id || descriptor.GenerationID != id || descriptor.JobRunID != record.JobRunID || descriptor.Number != record.Number {
		return nil, nil, fmt.Errorf("stored generation identity is invalid")
	}
	return &record, descriptor, nil
}

// ReconcileJobGeneration 首次准入与关闭前序、创建后继、写入幂等响应同属一个事务
// 检查只读返回当前身份，绝不授予重启后执行权限
func (r *JobRunRepo) ReconcileJobGeneration(ctx context.Context, request taskqueue.JobGenerationTransition) (*taskqueue.JobGeneration, error) {
	encoded, err := request.Canonical()
	if err != nil {
		return nil, err
	}
	var response *taskqueue.JobGeneration
	err = r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.JobRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(jobSnapshotColumns).Where("job_run_id = ?", request.Identity.JobRunID).First(&job).Error; err != nil {
			return err
		}
		if generationIdentity(&job) != request.Identity || !job.SourceExpectationCaptured || job.RetryPlanDigest != request.RetryPlanDigest {
			return fmt.Errorf("original generation execution identity changed")
		}
		plan, err := retryPlanForJob(&job)
		if err != nil {
			return err
		}
		if plan.Policy.Mode == "failed_step" && request.Operation == "advance" {
			return fmt.Errorf("failed_step cannot advance a whole-job generation")
		}
		if request.Operation == "inspect" {
			if request.InputManifestDigest != "" && request.InputManifestDigest != job.InputManifestDigest {
				return fmt.Errorf("inspect input binding changed")
			}
			if job.CurrentGenerationID == "" {
				return fmt.Errorf("no generation has been admitted")
			}
			record, g, err := loadGeneration(ctx, tx, job.CurrentGenerationID)
			if err != nil {
				return err
			}
			if !generationMatchesJob(g, &job) || g.Number != job.CurrentGeneration {
				return fmt.Errorf("current generation pointer changed")
			}
			g.ExecutionAllowed = false
			g.PreparationOnly = record.State == "preparing"
			response = g
			return nil
		}
		terminal := model.IsJobRunTerminal(job.Status)
		if terminal && request.Operation != "advance" {
			return fmt.Errorf("terminal job cannot admit a generation")
		}
		// 终态 whole_job 仅可返回已提交的准确 advance 回执，不锁共享 run
		approvalContext, err := retryApprovalContextRead(ctx, tx, &job, !terminal)
		if err != nil {
			return err
		}
		required := approvalContext != nil
		// 已提交的清理/后继转换 ACK 可在失效后重放，但不能恢复执行权限
		if required && request.Operation == "advance" {
			prior, old, err := loadGeneration(ctx, tx, request.PreviousGenerationID)
			if err != nil {
				return err
			}
			if !generationMatchesJob(old, &job) || prior.JobRunID != job.JobRunID {
				return fmt.Errorf("foreign generation predecessor")
			}
			if prior.ClosureDigest != "" {
				if prior.ClosureDigest != request.RequestDigest || prior.ClosureRequest != encoded || prior.NextGenerationID == "" {
					return fmt.Errorf("generation transition payload changed")
				}
				next, g, err := loadGeneration(ctx, tx, prior.NextGenerationID)
				if err != nil {
					return err
				}
				if next.AdmissionRequest != encoded || next.AdmissionDigest != request.RequestDigest || !generationMatchesJob(g, &job) || g.PreviousGenerationID != prior.GenerationID || g.Number != prior.Number+1 {
					return fmt.Errorf("generation transition receipt changed")
				}
				g.ExecutionAllowed = false
				g.PreparationOnly = next.State == "preparing"
				if !terminal && job.CurrentGenerationID == g.GenerationID && validateRetryApprovalExecution(ctx, tx, &job, false) == nil {
					g.ExecutionAllowed = true
				}
				response = g
				return nil
			}
		}
		if err := validateGenerationLive(ctx, tx, &job, request.Identity); err != nil {
			return err
		}
		required, err = retryApprovalRequired(ctx, tx, &job)
		if err != nil {
			return err
		}
		if request.Operation == "advance" {
			if err := validateRetryApprovalExecution(ctx, tx, &job, false); err != nil {
				return err
			}
		}
		// 首次 ACK 丢失时固定请求仍可重放，不能把已固定 manifest 加入旧请求而改变转换身份
		if request.Operation == "admit" && job.CurrentGenerationID != "" {
			var first model.JobRunGeneration
			if err := tx.Select(generationColumns).Where("job_run_id = ? AND number = ?", job.JobRunID, 1).First(&first).Error; err != nil {
				return err
			}
			if first.JobRunID != job.JobRunID || first.AdmissionDigest != request.RequestDigest || first.AdmissionRequest != encoded {
				return fmt.Errorf("first generation admission payload changed")
			}
			_, g, err := loadGeneration(ctx, tx, first.GenerationID)
			if err != nil {
				return err
			}
			if !generationMatchesJob(g, &job) {
				return fmt.Errorf("first generation binding changed")
			}
			pinned := g.InputManifestDigest
			if pinned == "" && job.InputManifestState == "pinned" {
				pinned = job.InputManifestDigest
			}
			if err := validateGenerationInputs(&job, pinned, true); err != nil {
				return err
			}
			g.ExecutionAllowed = job.CurrentGenerationID == g.GenerationID
			g.PreparationOnly = first.State == "preparing" || required
			if required && first.RetryApprovalAdmission != "" {
				if err := validateRetryApprovalExecution(ctx, tx, &job, true); err != nil {
					return err
				}
			}
			response = g
			return nil
		}
		if err := validateGenerationInputs(&job, request.InputManifestDigest, request.Operation == "admit"); err != nil {
			return err
		}
		number, previous := 1, ""
		var prior *model.JobRunGeneration
		if request.Operation == "advance" {
			var old *taskqueue.JobGeneration
			prior, old, err = loadGeneration(ctx, tx, request.PreviousGenerationID)
			if err != nil {
				return err
			}
			if prior.JobRunID != job.JobRunID || !generationMatchesJob(old, &job) {
				return fmt.Errorf("foreign generation predecessor")
			}
			if prior.ClosureDigest != "" {
				if prior.ClosureDigest != request.RequestDigest || prior.ClosureRequest != encoded || prior.NextGenerationID == "" {
					return fmt.Errorf("generation transition payload changed")
				}
				next, g, err := loadGeneration(ctx, tx, prior.NextGenerationID)
				if err != nil {
					return err
				}
				if next.AdmissionRequest != encoded || next.AdmissionDigest != request.RequestDigest || !generationMatchesJob(g, &job) || g.PreviousGenerationID != prior.GenerationID || g.Number != prior.Number+1 {
					return fmt.Errorf("generation transition receipt changed")
				}
				g.ExecutionAllowed = job.CurrentGenerationID == g.GenerationID
				g.PreparationOnly = next.State == "preparing"
				response = g
				return nil
			}
			if job.CurrentGenerationID != prior.GenerationID || job.CurrentGeneration != prior.Number || prior.State != "active" || prior.Number >= int(plan.Policy.MaxAttempts) {
				return fmt.Errorf("generation is stale, inactive or exhausted")
			}
			if err := request.CleanupReceipt.ValidateGeneration(*old); err != nil {
				return err
			}
			if err := validateCompletedGeneration(ctx, tx, &job, plan, old, request); err != nil {
				return err
			}
			number, previous = prior.Number+1, prior.GenerationID
		} else if job.CurrentGenerationID != "" || job.CurrentGeneration != 0 {
			return fmt.Errorf("first generation already admitted")
		}
		g := &taskqueue.JobGeneration{Version: 1, PipelineID: job.PipelineID, PipelineRunID: job.PipelineRunID, JobRunID: job.JobRunID, AgentID: job.AgentID, OriginalClaimID: job.ClaimID, RuntimeBindingDigest: job.RuntimeBindingDigest, SourceBindingDigest: job.SourceBindingDigest, InputManifestDigest: request.InputManifestDigest, RetryPlanDigest: job.RetryPlanDigest, GenerationID: uuid.NewString(), Number: number, RuntimeClaimID: uuid.NewString(), PreviousGenerationID: previous, ExecutionAllowed: true, PreparationOnly: request.InputManifestDigest == "" || (required && number == 1)}
		if number > 1 {
			delay, _ := time.ParseDuration(plan.Policy.Delay)
			g.NotBeforeUnixMilli = time.Now().Add(delay).UnixMilli()
		}
		g.BindingDigest, err = g.Digest()
		if err != nil {
			return err
		}
		body, err := g.Canonical()
		if err != nil {
			return err
		}
		state := "active"
		if g.PreparationOnly {
			state = "preparing"
		}
		record := model.JobRunGeneration{GenerationID: g.GenerationID, JobRunID: g.JobRunID, Number: number, State: state, GenerationJSON: body, AdmissionRequest: encoded, AdmissionDigest: request.RequestDigest}
		if prior != nil {
			if err := reportedStatusResult(tx.Model(&model.JobRunGeneration{}).Where("generation_id = ? AND job_run_id = ? AND state = ? AND closure_digest = ? AND next_generation_id = ?", prior.GenerationID, job.JobRunID, "active", "", "").Updates(map[string]any{"state": "closed", "closure_request": encoded, "closure_digest": request.RequestDigest, "next_generation_id": g.GenerationID, "updated_at": time.Now()})); err != nil {
				return err
			}
		}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		if err := reportedStatusResult(reportedJobQuery(tx.Model(&model.JobRun{}), &job).Updates(map[string]any{"current_generation_id": g.GenerationID, "current_generation": number, "updated_at": time.Now()})); err != nil {
			return err
		}
		if request.Operation == "advance" {
			if err := validateRetryApprovalCommit(ctx, tx, &job, model.JobRunStatusRunning); err != nil {
				return err
			}
		}
		if err := validateGenerationLive(ctx, tx, &job, request.Identity); err != nil {
			return err
		}
		response = g
		return nil
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func validateCompletedGeneration(ctx context.Context, tx *gorm.DB, job *model.JobRun, plan *taskqueue.JobRetryPlan, g *taskqueue.JobGeneration, request taskqueue.JobGenerationTransition) error {
	policy, _ := sonic.MarshalString(plan.Policy)
	failed := false
	for _, item := range plan.Steps {
		id := item.AttemptRunIDs[g.Number-1]
		var step model.StepRun
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("step_run_id", "job_run_id", "pipeline_id", "pipeline_run_id", "agent_id", "claim_id", "name", "step_index", "attempt", "status", "uses", "action", "continue_on_error", "retry_policy", "retry_mode", "terminal_report_digest", "exit_code", "generation_id", "generation_binding_digest", "retryable_shell_exit").Where("step_run_id = ?", id).First(&step).Error; err != nil {
			return err
		}
		if step.StepRunID != id || step.JobRunID != job.JobRunID || step.PipelineID != job.PipelineID || step.PipelineRunID != job.PipelineRunID || step.AgentID != job.AgentID || step.ClaimID != agentidentity.StepClaimID(job.ClaimID, id) || step.Name != item.Name || step.StepIndex != int(item.StepIndex) || step.Attempt != g.Number || step.Uses != item.Uses || step.Action != item.Action || (step.ContinueOnError != 0) != item.ContinueOnError || step.GenerationID != g.GenerationID || step.GenerationBindingDigest != g.BindingDigest || step.TerminalReportDigest == "" || step.RetryMode != "whole_job" || !sameRetryPolicy(step.RetryPolicy, policy) {
			return fmt.Errorf("prior round identity or committed receipt mismatch")
		}
		if step.Status != model.JobRunStatusSuccess && step.Status != model.JobRunStatusFailed && step.Status != model.JobRunStatusSkipped {
			return fmt.Errorf("prior round is incomplete or not retryable")
		}
		if failed && step.Status != model.JobRunStatusSkipped {
			return fmt.Errorf("remaining prior-round attempts must be skipped")
		}
		if step.Status == model.JobRunStatusFailed && step.ContinueOnError == 0 && id != request.FailedStepRunID {
			return fmt.Errorf("prior round has a different non-continued failure")
		}
		if id == request.FailedStepRunID {
			if !step.RetryableShellExit || step.TerminalReportDigest != request.FailedStepReceiptDigest || !allowedReportedFailure(step, plan.Policy) || (step.Uses != "shell" && step.Uses != "builtin:shell") {
				return fmt.Errorf("original committed pure shell failure is required")
			}
			failed = true
		}
	}
	if !failed {
		return fmt.Errorf("failed step is outside prior round")
	}
	return nil
}
