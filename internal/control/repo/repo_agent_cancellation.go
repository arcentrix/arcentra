package repo

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CancelAgentJobIfCurrent 原子取消远程作业及活动步骤，保留原认领并明确记录取消来源
func (r *JobRunRepo) CancelAgentJobIfCurrent(ctx context.Context, runID string, current int, updates map[string]any) error {
	if err := rejectSourceUpdates(updates); err != nil {
		return err
	}
	if changesJobIdentity(updates) {
		return fmt.Errorf("cancellation cannot replace execution identity")
	}
	if next, _ := updates["status"].(int); next != model.JobRunStatusCancelled || current < 1 || current > 3 {
		return fmt.Errorf("active agent cancellation required")
	}
	return r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.JobRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select(jobSnapshotColumns).
			Where("job_run_id = ? AND status = ?", runID, current).First(&job).Error; err != nil {
			return err
		}
		if job.JobRunID != runID {
			return fmt.Errorf("exact job identity is required")
		}
		if job.AgentID == "" && job.ClaimID != "" {
			return fmt.Errorf("claimed job requires agent ownership")
		}
		if err := cancelPendingRetryApproval(ctx, tx, &job, "job execution canceled"); err != nil {
			return err
		}
		patch := maps.Clone(updates)
		patch["cancellation_claim_id"] = job.ClaimID
		patch["updated_at"] = time.Now()
		if err := reportedStatusResult(reportedJobQuery(tx.Model(&model.JobRun{}), &job).Updates(patch)); err != nil {
			return err
		}
		// 仅取消同一预分配作业的未完成尝试，不覆盖同时提交的成功或失败
		return tx.Model(&model.StepRun{}).
			Where("job_run_id = ? AND pipeline_id = ? AND pipeline_run_id = ? AND agent_id = ?", runID, job.PipelineID, job.PipelineRunID, job.AgentID).
			Where("status IN ?", []int{1, 2, 3}).
			Updates(map[string]any{"status": model.JobRunStatusCancelled, "end_time": time.Now(), "updated_at": time.Now(), "cancellation_claim_id": job.ClaimID}).Error
	})
}

// CommitCancellationReceipt 只确认原作业认领的取消回执，不更改取消结果或添加制品
func (r *JobRunRepo) CommitCancellationReceipt(ctx context.Context, expected *model.JobRun, claim, digest string) error {
	return r.CommitCancellationReceiptWithSource(ctx, expected, claim, digest, nil)
}

// CommitCancellationReceiptWithSource 原子记录原认领取消回执和初始检出，不覆盖取消结果或添加制品
func (r *JobRunRepo) CommitCancellationReceiptWithSource(ctx context.Context, expected *model.JobRun, claim, digest string, sourceUpdates map[string]any) error {
	if expected == nil || expected.Status != model.JobRunStatusCancelled || expected.CancellationClaimID == "" || expected.ClaimID != claim || expected.CancellationClaimID != claim || expected.TerminalReportDigest != "" || len(digest) != 64 {
		return fmt.Errorf("matching controller cancellation required")
	}
	updates := maps.Clone(sourceUpdates)
	if updates == nil {
		updates = map[string]any{}
	}
	for key := range updates {
		if key != "source_resolved_commit" && key != "source_provenance_state" {
			return fmt.Errorf("only source result updates are permitted")
		}
	}
	updates["terminal_report_digest"] = digest
	if err := validateSourceReportUpdates(expected, updates, true); err != nil {
		return err
	}
	return r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockReportedJob(ctx, tx, expected); err != nil {
			return err
		}
		if err := validateGenerationCancellationReceipt(ctx, tx, expected, nil); err != nil {
			return err
		}
		if err := ensureJobAttemptsTerminal(ctx, tx, expected.JobRunID); err != nil {
			return err
		}
		return reportedStatusResult(reportedJobQuery(tx.Model(&model.JobRun{}), expected).
			Where("cancellation_claim_id = ?", expected.CancellationClaimID).
			Updates(updates))
	})
}

// CommitCancellationReceipt 只确认原步骤或可验证的未开始尝试取消回执，不制造执行认领
func (r *StepRunRepo) CommitCancellationReceipt(ctx context.Context, expected *model.StepRun, claim, digest string) error {
	if expected == nil || expected.Status != model.JobRunStatusCancelled || expected.CancellationClaimID == "" || expected.TerminalReportDigest != "" || len(digest) != 64 {
		return fmt.Errorf("controller step cancellation required")
	}
	want := expected.ClaimID
	if want == "" {
		want = agentidentity.StepClaimID(expected.CancellationClaimID, expected.StepRunID)
	}
	if claim != want {
		return fmt.Errorf("original step or parent claim required")
	}
	return r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var parent model.JobRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(jobSnapshotColumns).
			Where("job_run_id = ? AND pipeline_id = ? AND pipeline_run_id = ? AND agent_id = ?", expected.JobRunID, expected.PipelineID, expected.PipelineRunID, expected.AgentID).
			Where("status = ? AND claim_id = ? AND cancellation_claim_id = ?", model.JobRunStatusCancelled, expected.CancellationClaimID, expected.CancellationClaimID).First(&parent).Error; err != nil {
			return err
		}
		if err := validateGenerationCancellationReceipt(ctx, tx, &parent, expected); err != nil {
			return err
		}
		return reportedStatusResult(tx.Model(&model.StepRun{}).
			Where("step_run_id = ? AND job_run_id = ? AND pipeline_id = ? AND pipeline_run_id = ? AND agent_id = ?", expected.StepRunID, expected.JobRunID, expected.PipelineID, expected.PipelineRunID, expected.AgentID).
			Where("status = ? AND claim_id = ? AND cancellation_claim_id = ? AND terminal_report_digest = ?", model.JobRunStatusCancelled, expected.ClaimID, expected.CancellationClaimID, "").
			Where("attempt = ? AND step_index = ? AND previous_step_run_id = ?", expected.Attempt, expected.StepIndex, expected.PreviousStepRunID).
			Where("generation_id = ? AND generation_binding_digest = ?", expected.GenerationID, expected.GenerationBindingDigest).
			Where("attempt_admission_request = ? AND attempt_admission_request_digest = ? AND attempt_admission = ?", expected.AttemptAdmissionRequest, expected.AttemptAdmissionRequestDigest, expected.AttemptAdmission).
			UpdateColumn("terminal_report_digest", digest))
	})
}
