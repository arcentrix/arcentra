// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package repo

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
)

// FinalizeRetryJobIfCurrent 只为控制面失败、取消或超时结束准确的有限重试作业快照
// 保留原认领和已提交结果，不制造 Agent 回执、清理证明、成功或制品
func (r *JobRunRepo) FinalizeRetryJobIfCurrent(ctx context.Context, expected *model.JobRun, updates map[string]any) error {
	if expected == nil || expected.Status < 1 || expected.Status > 3 || expected.RetryPlan == "" || expected.RetryPlanDigest == "" {
		return fmt.Errorf("active immutable retry job snapshot is required")
	}
	if _, err := retryPlanForJob(expected); err != nil {
		return err
	}
	next, ok := updates["status"].(int)
	if !ok || (next != model.JobRunStatusFailed && next != model.JobRunStatusCancelled && next != model.JobRunStatusTimeout) {
		return fmt.Errorf("controller finalization cannot publish success")
	}
	for key := range updates {
		switch key {
		case "status", "end_time", "duration", "error_message":
		default:
			return fmt.Errorf("controller finalization cannot change execution authority or evidence")
		}
	}
	ended, ok := updates["end_time"].(time.Time)
	if !ok || ended.IsZero() {
		return fmt.Errorf("explicit terminal time is required")
	}
	if duration, exists := updates["duration"]; exists {
		value, ok := duration.(int64)
		if !ok || value < 0 {
			return fmt.Errorf("invalid terminal duration")
		}
	}
	patch := maps.Clone(updates)
	patch["updated_at"] = time.Now()
	if next == model.JobRunStatusCancelled {
		patch["cancellation_claim_id"] = expected.ClaimID
	}
	return r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockReportedJob(ctx, tx, expected); err != nil {
			return err
		}
		if err := cancelPendingRetryApproval(ctx, tx, expected, "job execution ended"); err != nil {
			return err
		}
		if expected.CurrentGenerationID != "" {
			record, g, err := loadGeneration(ctx, tx, expected.CurrentGenerationID)
			if err != nil {
				return err
			}
			if record.JobRunID != expected.JobRunID || record.Number != expected.CurrentGeneration || !generationMatchesJob(g, expected) || (record.State != "active" && record.State != "preparing") {
				return fmt.Errorf("controller finalization generation changed")
			}
			if err := reportedStatusResult(tx.Model(&model.JobRunGeneration{}).Where("generation_id = ? AND state = ? AND closure_digest = ?", record.GenerationID, record.State, "").Updates(map[string]any{"state": "aborted", "updated_at": time.Now()})); err != nil {
				return err
			}
		}
		if err := reportedStatusResult(reportedJobQuery(tx.Model(&model.JobRun{}), expected).Updates(patch)); err != nil {
			return err
		}
		stepStatus := model.JobRunStatusSkipped
		if next == model.JobRunStatusCancelled || next == model.JobRunStatusTimeout {
			stepStatus = next
		}
		stepPatch := map[string]any{"status": stepStatus, "end_time": ended, "updated_at": time.Now()}
		if reason, exists := patch["error_message"]; exists {
			stepPatch["error_message"] = reason
		}
		if next == model.JobRunStatusCancelled {
			stepPatch["cancellation_claim_id"] = expected.ClaimID
		}
		return tx.Model(&model.StepRun{}).Where("job_run_id = ? AND pipeline_id = ? AND pipeline_run_id = ? AND agent_id = ?", expected.JobRunID, expected.PipelineID, expected.PipelineRunID, expected.AgentID).Where("status IN ?", []int{1, 2, 3}).Updates(stepPatch).Error
	})
}
