// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/bytedance/sonic"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IsAutomaticPipelineRun 检查自动入口生成的无审批运行身份，不授予执行或状态修改权限
func IsAutomaticPipelineRun(run *model.PipelineRun) bool {
	if run == nil || !emptyRunInboxBinding(run) || run.PollingContext != "" || run.PollingContextDigest != "" || run.RunID == "" || run.PipelineID == "" || run.AllowDeployments || run.DeploymentActorID != "" || run.DeploymentPlan != "" || run.DeploymentPlanHash != "" || run.DeploymentTargetFingerprint != "" || run.RetryApprovalContext != "" || run.RetryApprovalContextDigest != "" {
		return false
	}
	switch run.TriggerType {
	case int(pipelinev1.TriggerType_TRIGGER_TYPE_CRON):
		return run.TriggeredBy == "cron" && strings.HasPrefix(run.RequestID, "cron:"+run.PipelineID+":")
	case int(pipelinev1.TriggerType_TRIGGER_TYPE_EVENT):
		return run.TriggeredBy == "webhook" && strings.HasPrefix(run.RequestID, "event:"+run.PipelineID+":")
	default:
		return false
	}
}

// RejectPendingAutomaticRun 仅将身份匹配且没有执行证据的自动运行原子标记为失败
// 返回 false 表示状态或身份已不匹配，禁止退回到无条件更新
func (r *PipelineRepo) RejectPendingAutomaticRun(ctx context.Context, run *model.PipelineRun) (bool, error) {
	return r.transitionPendingAutomaticRun(ctx, run, false)
}

// StartAutomaticRunIfPending 原子认领新的自动运行，认领结果不明时禁止创建协调器
func (r *PipelineRepo) StartAutomaticRunIfPending(ctx context.Context, run *model.PipelineRun) (bool, error) {
	return r.transitionPendingAutomaticRun(ctx, run, true)
}

func (r *PipelineRepo) transitionPendingAutomaticRun(ctx context.Context, run *model.PipelineRun, start bool) (bool, error) {
	if !IsAutomaticPipelineRun(run) {
		return false, nil
	}
	changed := false
	err := r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		projectID := ""
		if start {
			var err error
			projectID, err = lockPipelineAdmissionProject(ctx, tx, run.PipelineID)
			if err != nil || projectID == "" {
				return err
			}
		}
		// Both transitions share the run -> job suffix. Rejection intentionally
		// needs no active scope; it can safely terminalize an archived pending run.
		// Exact comparison prevents case-insensitive MySQL aliases expanding identity.
		var stored model.PipelineRun
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("run_id", "pipeline_id", "project_id", "inbox_actor_id", "inbox_origin", "request_id", "trigger_type", "triggered_by", "definition_commit_sha", "definition_path", "branch", "commit_sha", "env", "status", "start_time", "end_time", "allow_deployments", "deployment_actor_id", "deployment_plan", "deployment_plan_hash", "deployment_target_fingerprint", "retry_approval_context", "retry_approval_context_digest", "polling_context", "polling_context_digest").
			Where("run_id = ?", run.RunID).First(&stored).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !sameAutomaticRunIdentity(&stored, run) || stored.ProjectID != "" || run.ProjectID != "" || stored.Status != model.PipelineStatusPending || stored.StartTime != nil || stored.EndTime != nil {
			return nil
		}
		var jobs []model.JobRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("job_run_id").Where("pipeline_run_id = ?", stored.RunID).Limit(1).Find(&jobs).Error; err != nil {
			return err
		}
		if len(jobs) != 0 {
			return nil
		}
		updates := map[string]any{"status": model.PipelineStatusFailed, "end_time": time.Now()}
		if start {
			updates = map[string]any{"status": model.PipelineStatusRunning, "start_time": time.Now(), "project_id": projectID}
		}
		result := tx.Model(&model.PipelineRun{}).Where("run_id = ? AND status = ? AND start_time IS NULL AND end_time IS NULL", stored.RunID, model.PipelineStatusPending).Updates(updates)
		changed = result.RowsAffected == 1
		return result.Error
	})
	// A commit error can mean the server committed; callers must not schedule or
	// try rejection cleanup without positive confirmation of this transaction.
	return changed && err == nil, err
}

func sameAutomaticRunIdentity(stored, submitted *model.PipelineRun) bool {
	return IsAutomaticPipelineRun(stored) && IsAutomaticPipelineRun(submitted) &&
		stored.RunID == submitted.RunID && stored.PipelineID == submitted.PipelineID && stored.RequestID == submitted.RequestID &&
		stored.TriggerType == submitted.TriggerType && stored.TriggeredBy == submitted.TriggeredBy &&
		stored.DefinitionCommitSha == submitted.DefinitionCommitSha && stored.DefinitionPath == submitted.DefinitionPath &&
		stored.Branch == submitted.Branch && stored.CommitSha == submitted.CommitSha && sameAutomaticEnvironment(stored.Env, submitted.Env)
}

// MySQL 规范化 JSON 空白与键顺序；仅比较字符串环境映射，不接受数字、数组或 null
func sameAutomaticEnvironment(stored, submitted string) bool {
	storedEnv, storedOK := automaticEnvironment(stored)
	submittedEnv, submittedOK := automaticEnvironment(submitted)
	return storedOK && submittedOK && maps.Equal(storedEnv, submittedEnv)
}

func automaticEnvironment(value string) (map[string]string, bool) {
	// 指针值区分 JSON null 与空字符串，避免解码后丢失身份差异
	var decoded map[string]*string
	if sonic.UnmarshalString(value, &decoded) != nil || decoded == nil {
		return nil, false
	}
	result := make(map[string]string, len(decoded))
	for key, value := range decoded {
		if value == nil {
			return nil, false
		}
		result[key] = *value
	}
	return result, true
}

// lockPipelineAdmissionProject serializes new run admission against project
// archive/disable. The order is pipeline -> project -> run -> job, matching
// polling admission. Never acquire a project lock after a run/job lock here.
// Missing or ambiguous scope is unavailable; tests must provide real scope rows.
func lockPipelineAdmissionProject(ctx context.Context, tx *gorm.DB, pipelineID string) (string, error) {
	var pipelines []model.Pipeline
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("pipeline_id", "project_id", "is_enabled", "is_deleted").Where("pipeline_id = ?", pipelineID).Limit(2).Find(&pipelines).Error; err != nil {
		return "", err
	}
	if len(pipelines) != 1 || pipelines[0].PipelineID != pipelineID || pipelines[0].ProjectID == "" || pipelines[0].IsEnabled != 1 || pipelines[0].IsDeleted != 0 {
		return "", nil
	}
	var projects []model.Project
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("project_id", "is_enabled", "status").Where("project_id = ?", pipelines[0].ProjectID).Limit(2).Find(&projects).Error; err != nil {
		return "", err
	}
	if len(projects) != 1 || projects[0].ProjectID != pipelines[0].ProjectID || projects[0].IsEnabled != 1 || projects[0].Status != model.ProjectStatusActive {
		return "", nil
	}
	return pipelines[0].ProjectID, nil
}

// StartManualRunIfPending admits an unchanged manual/gated run exactly once.
// A false result or uncertain commit never grants scheduling permission. Already
// admitted jobs keep their authority; archive does not cancel or rewrite them.
func (r *PipelineRepo) StartManualRunIfPending(ctx context.Context, run *model.PipelineRun) (bool, error) {
	if !manualAdmissionIdentity(run) {
		return false, nil
	}
	changed := false
	err := r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		projectID, err := lockPipelineAdmissionProject(ctx, tx, run.PipelineID)
		if err != nil || projectID == "" {
			return err
		}
		var rows []model.PipelineRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(pipelineRunReadColumns).
			Where("run_id = ?", run.RunID).Limit(2).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != 1 {
			return nil
		}
		stored := &rows[0]
		if !sameManualAdmissionIdentity(stored, run) || stored.ProjectID != "" || run.ProjectID != "" || stored.Status != model.PipelineStatusPending || stored.StartTime != nil || stored.EndTime != nil {
			return nil
		}
		if stored.RetryApprovalContext != "" {
			// A pipeline move cannot expand an already captured approval scope.
			captured, err := retryapproval.ParseRunContext(stored.RetryApprovalContext)
			if err != nil || captured.ProjectID != projectID {
				return nil
			}
		}
		var jobs []model.JobRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("job_run_id").Where("pipeline_run_id = ?", stored.RunID).Limit(1).Find(&jobs).Error; err != nil {
			return err
		}
		if len(jobs) != 0 {
			return nil
		}
		result := tx.Model(&model.PipelineRun{}).Where("id = ? AND status = ? AND start_time IS NULL AND end_time IS NULL", stored.ID, model.PipelineStatusPending).
			Updates(map[string]any{"status": model.PipelineStatusRunning, "start_time": time.Now(), "project_id": projectID})
		changed = result.RowsAffected == 1
		return result.Error
	})
	return changed && err == nil, err
}
func manualAdmissionIdentity(run *model.PipelineRun) bool {
	// Zero is retained for legacy internal manual runs; automatic trigger types
	// and every polling marker remain excluded, never relabelled as manual.
	return run != nil && validRunInboxBinding(run) && run.RunID != "" && run.PipelineID != "" &&
		(run.TriggerType == 0 || run.TriggerType == int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL)) &&
		run.PollingContext == "" && run.PollingContextDigest == "" && run.TriggeredBy != "polling"
}
func sameManualAdmissionIdentity(a, b *model.PipelineRun) bool {
	return manualAdmissionIdentity(a) && manualAdmissionIdentity(b) && a.RunID == b.RunID && a.PipelineID == b.PipelineID && a.RequestID == b.RequestID &&
		a.TriggerType == b.TriggerType && a.TriggeredBy == b.TriggeredBy && a.InboxActorID == b.InboxActorID && a.InboxOrigin == b.InboxOrigin && a.DefinitionCommitSha == b.DefinitionCommitSha && a.DefinitionPath == b.DefinitionPath &&
		a.Branch == b.Branch && a.CommitSha == b.CommitSha && sameAutomaticEnvironment(a.Env, b.Env) &&
		a.AllowDeployments == b.AllowDeployments && a.DeploymentActorID == b.DeploymentActorID && a.DeploymentPlan == b.DeploymentPlan && a.DeploymentPlanHash == b.DeploymentPlanHash && a.DeploymentTargetFingerprint == b.DeploymentTargetFingerprint &&
		a.RetryApprovalContext == b.RetryApprovalContext && a.RetryApprovalContextDigest == b.RetryApprovalContextDigest
}
