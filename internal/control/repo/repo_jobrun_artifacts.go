// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
)

// PatchReportedStatusAndArtifacts 原子提交归属匹配的作业终态、回执与制品，失败保留待上报状态
func (r *JobRunRepo) PatchReportedStatusAndArtifacts(ctx context.Context, expected *model.JobRun, updates map[string]any, artifacts []*model.ProjectArtifact) error {
	var err error
	updates, err = normalizeJSONUpdates(updates, jobRunJSONDefaults)
	if err != nil {
		return err
	}
	if expected != nil {
		if len(artifacts) > 0 && expected.InputExpectation != "" && expected.InputManifestState != "pinned" {
			return fmt.Errorf("outputs require pinned container inputs")
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
	return r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockReportedJob(ctx, tx, expected); err != nil {
			return err
		}
		if next, _ := updates["status"].(int); next >= 4 {
			if err := ensureJobAttemptsTerminal(ctx, tx, expected.JobRunID); err != nil {
				return err
			}
		}
		var job model.JobRun
		if err := reportedJobQuery(tx.WithContext(ctx).Select("job_run_id", "pipeline_id", "pipeline_run_id", "agent_id", "artifact_storage_id", "artifact_storage_fingerprint", "artifact_storage_type"), expected).First(&job).Error; err != nil {
			return err
		}
		for _, item := range artifacts {
			if item == nil || item.JobRunID != job.JobRunID || item.PipelineID != job.PipelineID || item.PipelineRunID != job.PipelineRunID || item.StorageID != job.ArtifactStorageID || item.StorageFingerprint != job.ArtifactStorageFingerprint || item.StorageType != job.ArtifactStorageType {
				return fmt.Errorf("artifact ownership or storage binding mismatch")
			}
			var step model.StepRun
			if err := tx.WithContext(ctx).Select("step_run_id", "agent_id", "job_run_id", "pipeline_id", "pipeline_run_id").Where("step_run_id = ?", item.StepRunID).First(&step).Error; err != nil {
				return err
			}
			if step.AgentID != job.AgentID || step.JobRunID != job.JobRunID || step.PipelineID != job.PipelineID || step.PipelineRunID != job.PipelineRunID {
				return fmt.Errorf("artifact step ownership mismatch")
			}
			if err := UpsertArtifact(ctx, tx, item); err != nil {
				return err
			}
		}
		updates["updated_at"] = time.Now()
		result := reportedJobQuery(tx.WithContext(ctx).Model(&model.JobRun{}), expected).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("execution state changed")
		}
		return nil
	})
}

// ValidateCompletedArtifacts 校验重复终态回执完全一致，拒绝追加或改变已有制品
func (r *JobRunRepo) ValidateCompletedArtifacts(ctx context.Context, jobRunID string, artifacts []*model.ProjectArtifact) error {
	return r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.WithContext(ctx).Model(&model.ProjectArtifact{}).Where("job_run_id = ?", jobRunID).Count(&count).Error; err != nil {
			return err
		}
		if count != int64(len(artifacts)) {
			return ErrArtifactConflict
		}
		for _, item := range artifacts {
			if item == nil || item.JobRunID != jobRunID {
				return ErrArtifactConflict
			}
			var exists int64
			if err := tx.WithContext(ctx).Model(&model.ProjectArtifact{}).Where("job_run_id = ? AND step_run_id = ? AND name = ?", jobRunID, item.StepRunID, item.Name).Count(&exists).Error; err != nil {
				return err
			}
			if exists != 1 {
				return ErrArtifactConflict
			}
			if err := UpsertArtifact(ctx, tx, item); err != nil {
				return err
			}
		}
		return nil
	})
}
