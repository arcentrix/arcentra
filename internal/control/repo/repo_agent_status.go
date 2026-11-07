package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
)

// PatchStatusIfCurrent 防止迟到或并发状态报告覆盖步骤终态
func (r *StepRunRepo) PatchStatusIfCurrent(ctx context.Context, runID string, current int, updates map[string]any) error {
	if err := rejectStepGenerationUpdates(updates); err != nil {
		return err
	}
	var err error
	updates, err = normalizeJSONUpdates(updates, stepRunJSONDefaults)
	if err != nil {
		return err
	}
	updates["updated_at"] = time.Now()
	return ordinaryStepMutation(ctx, r.Database(), runID, updates, func(tx *gorm.DB) error {
		result := tx.Model(&model.StepRun{}).
			Where("step_run_id = ? AND status = ?", runID, current).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("execution state changed")
		}
		return nil
	})
}

// PatchStatusIfCurrent 防止迟到或并发状态报告覆盖作业终态
func (r *JobRunRepo) PatchStatusIfCurrent(ctx context.Context, runID string, current int, updates map[string]any) error {
	var err error
	updates, err = normalizeJSONUpdates(updates, jobRunJSONDefaults)
	if err != nil {
		return err
	}
	if err := rejectSourceUpdates(updates); err != nil {
		return err
	}
	if changesJobIdentity(updates) {
		return fmt.Errorf("generic status update cannot change job identity")
	}
	updates["updated_at"] = time.Now()
	result := r.Database().WithContext(ctx).Model(&model.JobRun{}).Where(ordinaryJobPredicate).
		Where("job_run_id = ? AND status = ? AND retry_plan = ? AND retry_plan_digest = ?", runID, current, "", "").Where("retry_approval_required = ? AND retry_approval_id = ? AND retry_approval_evidence_hash = ?", false, "", "").Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("execution state changed")
	}
	return nil
}

// PatchReportedStatus 原子校验步骤归属与原执行认领后提交状态和终态回执
func (r *StepRunRepo) PatchReportedStatus(ctx context.Context, expected *model.StepRun, updates map[string]any) error {
	if expected == nil {
		return fmt.Errorf("step snapshot is required")
	}
	if err := validateGenerationStepUpdates(ctx, expected, updates); err != nil {
		return err
	}
	var err error
	updates, err = normalizeJSONUpdates(updates, stepRunJSONDefaults)
	if err != nil {
		return err
	}
	if expected == nil || expected.Status < 1 || expected.Status > 3 {
		return fmt.Errorf("active step snapshot is required")
	}
	return executionAttachmentTransaction(ctx, r.Database(), executionStepJobID(expected), func(tx *gorm.DB) error {
		claim, _ := updates["claim_id"].(string)
		next, _ := updates["status"].(int)
		if err := validateRuntimeStepClaim(ctx, tx, expected, claim, next); err != nil {
			return err
		}
		if next, _ := updates["status"].(int); next == 3 {
			if err := validateRetryActivation(ctx, tx, expected); err != nil {
				return err
			}
		}
		updates["updated_at"] = time.Now()
		result := tx.Model(&model.StepRun{}).
			Where("step_run_id = ? AND status = ? AND agent_id = ? AND claim_id = ?", expected.StepRunID, expected.Status, expected.AgentID, expected.ClaimID).
			Where("pipeline_id = ? AND pipeline_run_id = ? AND job_run_id = ?", expected.PipelineID, expected.PipelineRunID, expected.JobRunID).
			Where("terminal_report_digest = ? AND generation_id = ? AND generation_binding_digest = ?", expected.TerminalReportDigest, expected.GenerationID, expected.GenerationBindingDigest).
			Where("attempt_admission_request = ? AND attempt_admission_request_digest = ? AND attempt_admission = ?", expected.AttemptAdmissionRequest, expected.AttemptAdmissionRequestDigest, expected.AttemptAdmission).Updates(updates)
		if err := reportedStatusResult(result); err != nil {
			return err
		}
		if err := activateClaimedGeneration(ctx, tx, next); err != nil {
			return err
		}
		if next == model.JobRunStatusRunning && expected.AttemptAdmission != "" {
			admission, err := taskqueue.ParseStepAttemptAdmission(expected.AttemptAdmission)
			if err != nil {
				return err
			}
			var job model.JobRun
			if err := tx.WithContext(ctx).Select(jobSnapshotColumns).Where("job_run_id = ?", expected.JobRunID).First(&job).Error; err != nil {
				return err
			}
			return validateStepAttemptCommit(ctx, tx, &job, admission, nil)
		}
		return nil
	})
}

// PatchReportedStatus 原子校验作业归属与原执行认领后提交状态和终态回执
func (r *JobRunRepo) PatchReportedStatus(ctx context.Context, expected *model.JobRun, updates map[string]any) error {
	var err error
	updates, err = normalizeJSONUpdates(updates, jobRunJSONDefaults)
	if err != nil {
		return err
	}
	if expected != nil {
		if err := rejectGenerationArtifactShortcut(expected, updates); err != nil {
			return err
		}
		if err := validateJobReportIdentityUpdates(expected, updates); err != nil {
			return err
		}
		if err := validateSourceReportUpdates(expected, updates, false); err != nil {
			return err
		}
	}
	if expected == nil || expected.Status < 1 || expected.Status > 3 {
		return fmt.Errorf("active job snapshot is required")
	}
	return executionTransaction(ctx, r.Database(), expected.JobRunID, func(tx *gorm.DB) error {
		if _, protected := rerunProtectedLocator(tx); protected {
			if err := lockCopilotRerunReportedJob(ctx, tx, expected, updates); err != nil {
				return err
			}
		} else if _, err := lockExecutionJob(ctx, tx, expected.JobRunID, executionLockLegacy); err != nil {
			return err
		}
		if err := lockReportedJob(ctx, tx, expected); err != nil {
			return err
		}
		if next, _ := updates["status"].(int); next != 0 {
			if err := validateGenerationJobReport(ctx, tx, expected, next); err != nil {
				return err
			}
		}
		if next, _ := updates["status"].(int); next >= 4 {
			if err := ensureJobAttemptsTerminal(ctx, tx, expected.JobRunID); err != nil {
				return err
			}
		}
		updates["updated_at"] = time.Now()
		if err := reportedStatusResult(reportedJobQuery(tx.Model(&model.JobRun{}), expected).Updates(updates)); err != nil {
			return err
		}
		next, _ := updates["status"].(int)
		return validateRetryApprovalCommit(ctx, tx, expected, next)
	})
}

func reportedJobQuery(tx *gorm.DB, expected *model.JobRun) *gorm.DB {
	return tx.Where("copilot_rerun_intent_id = ? AND copilot_rerun_gate_id = ? AND expected_source_commit = ?", expected.CopilotRerunIntentID, expected.CopilotRerunGateID, expected.ExpectedSourceCommit).Where("job_run_id = ? AND status = ? AND agent_id = ? AND claim_id = ?", expected.JobRunID, expected.Status, expected.AgentID, expected.ClaimID).
		Where("pipeline_id = ? AND pipeline_run_id = ? AND job_name = ?", expected.PipelineID, expected.PipelineRunID, expected.JobName).
		Where("terminal_report_digest = ? AND cancellation_claim_id = ?", expected.TerminalReportDigest, expected.CancellationClaimID).
		Where("source_expectation_captured = ? AND source_type = ? AND source_repository = ? AND source_ref = ?", expected.SourceExpectationCaptured, expected.SourceType, expected.SourceRepository, expected.SourceRef).
		Where("source_binding_digest = ? AND source_resolved_commit = ? AND source_provenance_state = ?", expected.SourceBindingDigest, expected.SourceResolvedCommit, expected.SourceProvenanceState).
		Where("runtime_type = ? AND runtime_image = ? AND runtime_cpu = ? AND runtime_memory = ? AND runtime_binding_digest = ?", expected.RuntimeType, expected.RuntimeImage, expected.RuntimeCPU, expected.RuntimeMemory, expected.RuntimeBindingDigest).
		Where("input_expectation = ? AND input_manifest_state = ? AND input_manifest = ? AND input_manifest_digest = ?", expected.InputExpectation, expected.InputManifestState, expected.InputManifest, expected.InputManifestDigest).
		Where("retry_plan = ? AND retry_plan_digest = ? AND current_generation_id = ? AND current_generation = ?", expected.RetryPlan, expected.RetryPlanDigest, expected.CurrentGenerationID, expected.CurrentGeneration).
		Where("retry_approval_required = ? AND retry_approval_id = ? AND retry_approval_evidence_hash = ?", expected.RetryApprovalRequired, expected.RetryApprovalID, expected.RetryApprovalEvidenceHash)
}

func reportedStatusResult(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("execution identity or state changed")
	}
	return nil
}
