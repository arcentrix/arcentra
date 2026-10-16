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
	"github.com/bytedance/sonic"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IsAutomaticPipelineRun 检查自动入口生成的无审批运行身份，不授予执行或状态修改权限
func IsAutomaticPipelineRun(run *model.PipelineRun) bool {
	if run == nil || run.RunID == "" || run.PipelineID == "" || run.AllowDeployments || run.DeploymentActorID != "" || run.DeploymentPlan != "" || run.DeploymentPlanHash != "" || run.DeploymentTargetFingerprint != "" || run.RetryApprovalContext != "" || run.RetryApprovalContextDigest != "" {
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
	err := r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 正向认领与拒绝采用同一锁顺序；精确比较禁止 MySQL 不区分大小写排序规则扩张身份
		var stored model.PipelineRun
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("run_id", "pipeline_id", "request_id", "trigger_type", "triggered_by", "definition_commit_sha", "definition_path", "branch", "commit_sha", "env", "status", "start_time", "end_time", "allow_deployments", "deployment_actor_id", "deployment_plan", "deployment_plan_hash", "deployment_target_fingerprint", "retry_approval_context", "retry_approval_context_digest").
			Where("run_id = ?", run.RunID).First(&stored).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !sameAutomaticRunIdentity(&stored, run) || stored.Status != model.PipelineStatusPending || stored.StartTime != nil || stored.EndTime != nil {
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
			updates = map[string]any{"status": model.PipelineStatusRunning, "start_time": time.Now()}
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
