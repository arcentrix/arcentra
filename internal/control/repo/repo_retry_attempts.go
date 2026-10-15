package repo

import (
	"context"
	"fmt"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/retry"
	"github.com/bytedance/sonic"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func lockReportedJob(ctx context.Context, tx *gorm.DB, expected *model.JobRun) error {
	var current model.JobRun
	if err := reportedJobQuery(tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(jobSnapshotColumns), expected).First(&current).Error; err != nil {
		return err
	}
	if !jobSnapshotEqual(&current, expected) {
		return fmt.Errorf("execution identity or source changed")
	}
	return nil
}

func ensureJobAttemptsTerminal(ctx context.Context, tx *gorm.DB, jobRunID string) error {
	var pending int64
	if err := tx.WithContext(ctx).Model(&model.StepRun{}).Where("job_run_id = ? AND status < ?", jobRunID, 4).Count(&pending).Error; err != nil {
		return err
	}
	if pending > 0 {
		return fmt.Errorf("job has %d unfinished step attempts", pending)
	}
	return nil
}

// ValidateRetryActivation 校验同认领重复确认仍有活动作业和合法的已提交前序尝试
func (r *StepRunRepo) ValidateRetryActivation(ctx context.Context, expected *model.StepRun) error {
	return r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error { return validateRetryActivation(ctx, tx, expected) })
}

func validateRetryActivation(ctx context.Context, tx *gorm.DB, expected *model.StepRun) error {
	if expected == nil {
		return fmt.Errorf("step snapshot is required")
	}
	if expected.RetryMode == "" && expected.Attempt <= 1 {
		return nil
	} // Legacy non-retry standalone claims remain independent.
	var policy pipelinev1.Retry
	if err := sonic.UnmarshalString(expected.RetryPolicy, &policy); err != nil {
		return fmt.Errorf("retry policy snapshot is invalid")
	}
	if err := retry.Validate(&policy); err != nil {
		return err
	}
	if policy.Mode != expected.RetryMode || expected.Attempt < 1 || expected.Attempt > int(policy.MaxAttempts) {
		return fmt.Errorf("attempt is outside its immutable retry policy")
	}
	var parent model.JobRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(jobSnapshotColumns).Where("job_run_id = ?", expected.JobRunID).First(&parent).Error; err != nil {
		return err
	}
	if parent.Status != 3 || parent.AgentID != expected.AgentID || parent.PipelineID != expected.PipelineID || parent.PipelineRunID != expected.PipelineRunID {
		return fmt.Errorf("retry parent job is not active or its ownership changed")
	}
	if parent.RetryPlan != "" && expected.RetryMode == "failed_step" {
		plan, err := retryPlanForJob(&parent)
		if err != nil {
			return err
		}
		return validateFailedStepReport(ctx, tx, &parent, expected, plan, model.JobRunStatusRunning)
	}
	var current model.StepRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("step_run_id", "retry_policy").
		Where("step_run_id = ? AND status = ? AND claim_id = ? AND terminal_report_digest = ?", expected.StepRunID, expected.Status, expected.ClaimID, expected.TerminalReportDigest).
		Where("job_run_id = ? AND pipeline_id = ? AND pipeline_run_id = ? AND agent_id = ?", expected.JobRunID, expected.PipelineID, expected.PipelineRunID, expected.AgentID).
		Where("name = ? AND step_index = ? AND attempt = ? AND retry_mode = ? AND previous_step_run_id = ?", expected.Name, expected.StepIndex, expected.Attempt, expected.RetryMode, expected.PreviousStepRunID).First(&current).Error; err != nil {
		return fmt.Errorf("retry step identity changed: %w", err)
	}
	if !sameRetryPolicy(current.RetryPolicy, expected.RetryPolicy) {
		return fmt.Errorf("retry policy changed")
	}
	if expected.Attempt == 1 {
		return nil
	}
	var previous model.StepRun
	if err := tx.WithContext(ctx).Select("step_run_id", "status", "agent_id", "pipeline_id", "pipeline_run_id", "job_run_id", "name", "step_index", "attempt", "retry_mode", "retry_policy", "terminal_report_digest", "exit_code", "uses", "continue_on_error").Where("step_run_id = ?", expected.PreviousStepRunID).First(&previous).Error; err != nil {
		return err
	}
	if previous.JobRunID != expected.JobRunID || previous.PipelineID != expected.PipelineID || previous.PipelineRunID != expected.PipelineRunID || previous.AgentID != expected.AgentID || previous.Name != expected.Name || previous.StepIndex != expected.StepIndex || previous.Attempt != expected.Attempt-1 || previous.RetryMode != expected.RetryMode || !sameRetryPolicy(previous.RetryPolicy, expected.RetryPolicy) || previous.TerminalReportDigest == "" {
		return fmt.Errorf("retry predecessor identity or committed receipt is invalid")
	}
	if policy.Mode == "failed_step" {
		if !allowedReportedFailure(previous, &policy) {
			return fmt.Errorf("failed-step retry requires an eligible failed predecessor")
		}
		return nil
	}
	if previous.Status != 4 && previous.Status != 5 && previous.Status != 8 {
		return fmt.Errorf("whole-job predecessor is not a completed retryable round")
	}
	var round []model.StepRun
	if err := tx.WithContext(ctx).Select("status", "uses", "exit_code", "continue_on_error", "terminal_report_digest", "retry_policy", "agent_id", "pipeline_id", "pipeline_run_id").Where("job_run_id = ? AND attempt = ?", expected.JobRunID, expected.Attempt-1).Find(&round).Error; err != nil {
		return err
	}
	failed := false
	for _, item := range round {
		if item.Status != 4 && item.Status != 5 && item.Status != 8 || item.TerminalReportDigest == "" || !sameRetryPolicy(item.RetryPolicy, expected.RetryPolicy) || item.AgentID != expected.AgentID || item.PipelineID != expected.PipelineID || item.PipelineRunID != expected.PipelineRunID {
			return fmt.Errorf("previous whole-job round is incomplete or not retryable")
		}
		failed = failed || allowedReportedFailure(item, &policy)
	}
	if !failed {
		return fmt.Errorf("whole-job retry requires an eligible failure in the previous round")
	}
	return nil
}

func allowedReportedFailure(step model.StepRun, policy *pipelinev1.Retry) bool {
	if step.Status != 5 || step.ContinueOnError != 0 || step.ExitCode == nil || (step.Uses != "shell" && step.Uses != "builtin:shell" && step.Uses != "") {
		return false
	}
	for _, code := range policy.AllowedExitCodes {
		if int(code) == *step.ExitCode {
			return true
		}
	}
	return false
}

func sameRetryPolicy(left, right string) bool {
	var a, b pipelinev1.Retry
	if sonic.UnmarshalString(left, &a) != nil || sonic.UnmarshalString(right, &b) != nil {
		return false
	}
	return proto.Equal(&a, &b)
}
