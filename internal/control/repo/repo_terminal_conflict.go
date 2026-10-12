package repo

import (
	"context"
	"fmt"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ValidateTerminalConflict 只验证不可变作业快照和原认领，不写入或确认任何结果
func (r *JobRunRepo) ValidateTerminalConflict(ctx context.Context, expected *model.JobRun, claim string) error {
	if expected == nil || !model.IsJobRunTerminal(expected.Status) || expected.AgentID == "" || expected.ClaimID == "" || expected.ClaimID != claim {
		return fmt.Errorf("original terminal job claim required")
	}
	return r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockReportedJob(ctx, tx, expected); err != nil {
			return err
		}
		var current model.JobRun
		if err := reportedJobQuery(tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("job_run_id", "pipeline_id", "pipeline_run_id", "agent_id", "claim_id", "status", "cancellation_claim_id", "terminal_report_digest"), expected).
			Where("cancellation_claim_id = ?", expected.CancellationClaimID).First(&current).Error; err != nil {
			return err
		}
		if current.JobRunID != expected.JobRunID || current.PipelineID != expected.PipelineID || current.PipelineRunID != expected.PipelineRunID || current.AgentID != expected.AgentID || current.ClaimID != expected.ClaimID || current.Status != expected.Status || current.CancellationClaimID != expected.CancellationClaimID || current.TerminalReportDigest != expected.TerminalReportDigest {
			return fmt.Errorf("terminal job snapshot changed")
		}
		return nil
	})
}

// ValidateTerminalConflict 验证原步骤认领，未认领终态仅凭原终态父作业及尝试快照证明拒绝来源
func (r *StepRunRepo) ValidateTerminalConflict(ctx context.Context, expected *model.StepRun, claim string) error {
	if expected == nil || !model.IsJobRunTerminal(expected.Status) || expected.AgentID == "" || expected.Attempt < 1 {
		return fmt.Errorf("terminal step attempt required")
	}
	derived := expected.ClaimID == ""
	if !derived && expected.ClaimID != claim {
		return fmt.Errorf("original terminal step claim required")
	}
	if derived && expected.Status == model.JobRunStatusCancelled && (expected.CancellationClaimID == "" || claim != agentidentity.StepClaimID(expected.CancellationClaimID, expected.StepRunID)) {
		return fmt.Errorf("verifiable cancelled parent claim required")
	}
	return r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if derived {
			var parent model.JobRun
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("job_run_id", "pipeline_id", "pipeline_run_id", "agent_id", "claim_id", "cancellation_claim_id", "status").
				Where("job_run_id = ? AND pipeline_id = ? AND pipeline_run_id = ? AND agent_id = ?", expected.JobRunID, expected.PipelineID, expected.PipelineRunID, expected.AgentID).First(&parent).Error; err != nil {
				return err
			}
			if parent.JobRunID != expected.JobRunID || parent.PipelineID != expected.PipelineID || parent.PipelineRunID != expected.PipelineRunID || parent.AgentID != expected.AgentID || !model.IsJobRunTerminal(parent.Status) || parent.ClaimID == "" || claim != agentidentity.StepClaimID(parent.ClaimID, expected.StepRunID) {
				return fmt.Errorf("matching terminal parent original claim required")
			}
			if expected.Status == model.JobRunStatusCancelled && (parent.Status != model.JobRunStatusCancelled || parent.ClaimID != expected.CancellationClaimID || parent.CancellationClaimID != expected.CancellationClaimID) {
				return fmt.Errorf("original parent cancellation provenance required")
			}
		}
		var current model.StepRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("step_run_id", "job_run_id", "pipeline_id", "pipeline_run_id", "agent_id", "status", "claim_id", "cancellation_claim_id", "terminal_report_digest", "attempt", "step_index", "previous_step_run_id").
			Where("step_run_id = ? AND job_run_id = ? AND pipeline_id = ? AND pipeline_run_id = ? AND agent_id = ?", expected.StepRunID, expected.JobRunID, expected.PipelineID, expected.PipelineRunID, expected.AgentID).
			Where("status = ? AND claim_id = ? AND cancellation_claim_id = ? AND terminal_report_digest = ?", expected.Status, expected.ClaimID, expected.CancellationClaimID, expected.TerminalReportDigest).
			Where("attempt = ? AND step_index = ? AND previous_step_run_id = ?", expected.Attempt, expected.StepIndex, expected.PreviousStepRunID).First(&current).Error; err != nil {
			return err
		}
		// 数据库排序规则可能忽略大小写，锁内仍逐字节比较所有执行身份字段
		if current.StepRunID != expected.StepRunID || current.JobRunID != expected.JobRunID || current.PipelineID != expected.PipelineID || current.PipelineRunID != expected.PipelineRunID || current.AgentID != expected.AgentID || current.Status != expected.Status || current.ClaimID != expected.ClaimID || current.CancellationClaimID != expected.CancellationClaimID || current.TerminalReportDigest != expected.TerminalReportDigest || current.Attempt != expected.Attempt || current.StepIndex != expected.StepIndex || current.PreviousStepRunID != expected.PreviousStepRunID {
			return fmt.Errorf("terminal step snapshot changed")
		}
		return nil
	})
}
