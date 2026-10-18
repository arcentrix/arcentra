// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package repo

import (
	"context"
	"errors"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrDeploymentConflict 表示发布身份或环境执行权冲突
var ErrDeploymentConflict = errors.New("deployment identity or active release conflict")

// ErrDeploymentApplyState hides database details when authoritative apply state
// cannot be read. Replica reads must never stand in for this safety check.
var ErrDeploymentApplyState = errors.New("current deployment apply state is unavailable")

// IDeploymentRepository 定义按项目隔离且具有条件状态更新的发布仓储
type IDeploymentRepository interface {
	CreateEnvironment(context.Context, *model.DeploymentEnvironment) error
	GetEnvironment(context.Context, string, string) (*model.DeploymentEnvironment, error)
	ListEnvironments(context.Context, string, int, int) ([]model.DeploymentEnvironment, int64, error)
	CreateRelease(context.Context, *model.DeploymentRelease) (*model.DeploymentRelease, error)
	GetRelease(context.Context, string, string) (*model.DeploymentRelease, error)
	GetReleaseByRequest(context.Context, string, string) (*model.DeploymentRelease, error)
	ListReleases(context.Context, string, int, int) ([]model.DeploymentRelease, int64, error)
	Transition(context.Context, *model.DeploymentRelease, string, map[string]any) (bool, error)
}

// IDeploymentApplyStateReader is required before any external apply. Both rows
// must come from the primary database in one fresh read transaction.
type IDeploymentApplyStateReader interface {
	GetApplyState(context.Context, string, string) (*model.DeploymentRelease, *model.DeploymentEnvironment, error)
}

// DeploymentRepo 使用事务确保同一目标只有一个活动发布
type DeploymentRepo struct{ database.IDatabase }

// NewDeploymentRepo 创建发布仓储，不自动迁移表
func NewDeploymentRepo(db database.IDatabase) IDeploymentRepository { return &DeploymentRepo{db} }

var environmentColumns = []string{"environment_id", "project_id", "name", "target_ref", "namespace", "deployment", "container", "active_release_id", "created_at", "updated_at"}
var releaseColumns = []string{"approval_id", "approval_evidence_hash", "approval_target", "origin", "execution_job_run_id", "execution_step_run_id", "producer_job_run_id", "producer_step_run_id", "deployment_plan_hash", "last_recovery_by", "last_recovery_at", "version", "release_id", "project_id", "request_id", "environment_id", "pipeline_id", "pipeline_run_id", "artifact_id", "artifact_sha256", "target_fingerprint", "commit_sha", "image", "created_by", "status", "reason", "workload_uid", "baseline_generation", "applied_generation", "observed_generation", "desired_replicas", "updated_replicas", "available_replicas", "last_observed_at", "finished_at", "created_at", "updated_at"}

// CreateEnvironment 创建不可变的目标绑定
func (r *DeploymentRepo) CreateEnvironment(ctx context.Context, value *model.DeploymentEnvironment) error {
	err := r.Database().WithContext(ctx).Create(value).Error
	if err == nil {
		return nil
	}
	var existing model.DeploymentEnvironment
	lookup := r.Database().WithContext(ctx).Select("environment_id").Where("project_id = ? AND target_ref = ? AND namespace = ? AND deployment = ?", value.ProjectID, value.TargetRef, value.Namespace, value.Deployment).First(&existing).Error
	if lookup == nil {
		return ErrDeploymentConflict
	}
	return err
}

// GetEnvironment 读取当前项目的环境
func (r *DeploymentRepo) GetEnvironment(ctx context.Context, project, id string) (*model.DeploymentEnvironment, error) {
	var value model.DeploymentEnvironment
	err := r.Database().WithContext(ctx).Select(environmentColumns).Where("project_id = ? AND environment_id = ?", project, id).First(&value).Error
	return &value, err
}

// ListEnvironments 按稳定顺序分页环境
func (r *DeploymentRepo) ListEnvironments(ctx context.Context, project string, offset, limit int) ([]model.DeploymentEnvironment, int64, error) {
	var values []model.DeploymentEnvironment
	var count int64
	q := r.Database().WithContext(ctx).Model(&model.DeploymentEnvironment{}).Where("project_id = ?", project)
	if err := q.Count(&count).Error; err != nil {
		return nil, 0, err
	}
	err := q.Select(environmentColumns).Order("created_at DESC, environment_id DESC").Offset(offset).Limit(limit).Find(&values).Error
	return values, count, err
}

// GetRelease 读取当前项目的发布
func (r *DeploymentRepo) GetRelease(ctx context.Context, project, id string) (*model.DeploymentRelease, error) {
	var value model.DeploymentRelease
	err := r.Database().WithContext(ctx).Select(releaseColumns).Where("project_id = ? AND release_id = ?", project, id).First(&value).Error
	return &value, err
}

// GetApplyState bypasses read replicas without changing history/observation
// routing. A fresh session prevents inherited query conditions or read routing
// from contaminating the final external-write check.
func (r *DeploymentRepo) GetApplyState(ctx context.Context, project, id string) (*model.DeploymentRelease, *model.DeploymentEnvironment, error) {
	if r == nil || r.IDatabase == nil {
		return nil, nil, ErrDeploymentApplyState
	}
	base := r.Database()
	if base == nil {
		return nil, nil, ErrDeploymentApplyState
	}
	var release model.DeploymentRelease
	var environment model.DeploymentEnvironment
	err := base.Session(&gorm.Session{NewDB: true}).Clauses(database.Write()).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Select(releaseColumns).Where("project_id = ? AND release_id = ?", project, id).First(&release).Error; err != nil {
			return err
		}
		return tx.Select(environmentColumns).Where("project_id = ? AND environment_id = ?", project, release.EnvironmentID).First(&environment).Error
	})
	if err != nil {
		return nil, nil, ErrDeploymentApplyState
	}
	return &release, &environment, nil
}

// GetReleaseByRequest 读取已经持久化的幂等请求
func (r *DeploymentRepo) GetReleaseByRequest(ctx context.Context, project, request string) (*model.DeploymentRelease, error) {
	var value model.DeploymentRelease
	err := r.Database().WithContext(ctx).Select(releaseColumns).Where("project_id = ? AND request_id = ?", project, request).First(&value).Error
	return &value, err
}

// ListReleases 返回发布历史，调用方必须在权限过滤后分页
func (r *DeploymentRepo) ListReleases(ctx context.Context, project string, offset, limit int) ([]model.DeploymentRelease, int64, error) {
	var values []model.DeploymentRelease
	var count int64
	q := r.Database().WithContext(ctx).Model(&model.DeploymentRelease{}).Where("project_id = ?", project)
	if err := q.Count(&count).Error; err != nil {
		return nil, 0, err
	}
	err := q.Select(releaseColumns).Order("created_at DESC, release_id DESC").Offset(offset).Limit(limit).Find(&values).Error
	return values, count, err
}

// CreateRelease 幂等登记发布并在同一事务中预留环境
func (r *DeploymentRepo) CreateRelease(ctx context.Context, value *model.DeploymentRelease) (*model.DeploymentRelease, error) {
	err := r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if value.Origin == "pipeline_job" {
			if value.ExecutionStepRunID == nil || value.ExecutionJobRunID == "" || value.DeploymentPlanHash == "" || value.CreatedBy == "" {
				return ErrDeploymentConflict
			}
			var run model.PipelineRun
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("run_id").Where("run_id = ? AND pipeline_id = ? AND status = ? AND allow_deployments = ? AND deployment_actor_id = ? AND deployment_plan_hash = ? AND definition_commit_sha = ? AND deployment_target_fingerprint = ?", value.PipelineRunID, value.PipelineID, model.PipelineStatusRunning, true, value.CreatedBy, value.DeploymentPlanHash, value.CommitSHA, value.TargetFingerprint).First(&run).Error; err != nil {
				return ErrDeploymentConflict
			}
			var job model.JobRun
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("job_run_id").Where("job_run_id = ? AND pipeline_run_id = ? AND pipeline_id = ? AND status = ? AND (agent_id = ? OR agent_id IS NULL)", value.ExecutionJobRunID, value.PipelineRunID, value.PipelineID, model.JobRunStatusRunning, "").First(&job).Error; err != nil {
				return ErrDeploymentConflict
			}
			link := tx.Model(&model.StepRun{}).Where("step_run_id = ? AND job_run_id = ? AND pipeline_run_id = ? AND pipeline_id = ? AND status = ? AND created_by = ?", *value.ExecutionStepRunID, value.ExecutionJobRunID, value.PipelineRunID, value.PipelineID, 3, value.CreatedBy).Where("deployment_release_id IS NULL OR deployment_release_id = ?", "").Update("deployment_release_id", value.ReleaseID)
			if link.Error != nil {
				return link.Error
			}
			if link.RowsAffected != 1 {
				return ErrDeploymentConflict
			}
		}
		claim := tx.Model(&model.DeploymentEnvironment{}).Where("project_id = ? AND environment_id = ? AND active_release_id = ?", value.ProjectID, value.EnvironmentID, "").Update("active_release_id", value.ReleaseID)
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected != 1 {
			return ErrDeploymentConflict
		}
		if err := tx.Create(value).Error; err != nil {
			return err
		}
		if value.ApprovalID != nil {
			approval := value.ApprovalRequest
			if approval == nil || approval.ApprovalID != *value.ApprovalID || approval.DeploymentReleaseID == nil || *approval.DeploymentReleaseID != value.ReleaseID || approval.PipelineRunID != value.PipelineRunID || approval.EvidenceHash == "" || approval.EvidenceHash != value.ApprovalEvidenceHash || approval.Status != model.ApprovalStatusPending {
				return ErrDeploymentConflict
			}
			if _, err := model.ApprovalCandidates(approval); err != nil {
				return err
			}
			if approval.InboxOrigin != 0 {
				return ErrDeploymentConflict
			}
			if err := tx.Create(approval).Error; err != nil {
				return err
			}
			if value.Origin == "pipeline_job" {
				return enrollInboxApproval(ctx, tx, approval)
			}
			return nil
		}
		if value.ApprovalRequest != nil || value.ApprovalEvidenceHash != "" {
			return ErrDeploymentConflict
		}
		return nil
	})
	if err == nil {
		return value, nil
	}
	old, readErr := r.GetReleaseByRequest(ctx, value.ProjectID, value.RequestID)
	if readErr == nil {
		if old.EnvironmentID == value.EnvironmentID && old.PipelineID == value.PipelineID && old.PipelineRunID == value.PipelineRunID && old.ArtifactID == value.ArtifactID && old.ArtifactSHA256 == value.ArtifactSHA256 && old.Image == value.Image && old.CommitSHA == value.CommitSHA && old.TargetFingerprint == value.TargetFingerprint && old.CreatedBy == value.CreatedBy && old.Origin == value.Origin && old.ExecutionJobRunID == value.ExecutionJobRunID && sameDeploymentStep(old.ExecutionStepRunID, value.ExecutionStepRunID) && old.ProducerJobRunID == value.ProducerJobRunID && old.ProducerStepRunID == value.ProducerStepRunID && old.DeploymentPlanHash == value.DeploymentPlanHash && sameDeploymentStep(old.ApprovalID, value.ApprovalID) {
			return old, nil
		}
		return nil, ErrDeploymentConflict
	}
	return nil, err
}

// Transition 原子更新状态并在终态释放环境，防止取消与提交并发交错
func (r *DeploymentRepo) Transition(ctx context.Context, value *model.DeploymentRelease, next string, updates map[string]any) (bool, error) {
	changed := false
	err := r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		patch := make(map[string]any, len(updates)+2)
		for k, v := range updates {
			switch k {
			case "last_recovery_by", "last_recovery_at", "reason", "workload_uid", "baseline_generation", "applied_generation", "observed_generation", "desired_replicas", "updated_replicas", "available_replicas", "last_observed_at":
				patch[k] = v
			default:
				return gorm.ErrInvalidData
			}
		}
		patch["status"] = next
		patch["version"] = value.Version + 1
		if model.IsDeploymentTerminal(next) {
			patch["finished_at"] = time.Now().UTC()
		}
		update := tx.Model(&model.DeploymentRelease{}).Where("project_id = ? AND release_id = ? AND status = ? AND version = ?", value.ProjectID, value.ReleaseID, value.Status, value.Version).Updates(patch)
		if update.Error != nil {
			return update.Error
		}
		changed = update.RowsAffected == 1
		if changed && model.IsDeploymentTerminal(next) {
			if value.Status == "queued" && value.ApprovalID != nil {
				if err := tx.Model(&model.ApprovalRequest{}).Where("approval_id = ? AND deployment_release_id = ? AND status = ?", *value.ApprovalID, value.ReleaseID, model.ApprovalStatusPending).Updates(map[string]any{"status": model.ApprovalStatusCanceled, "reason": "Deployment cancelled before apply", "version": gorm.Expr("version + ?", 1)}).Error; err != nil {
					return err
				}
			}
			return tx.Model(&model.DeploymentEnvironment{}).Where("project_id = ? AND environment_id = ? AND active_release_id = ?", value.ProjectID, value.EnvironmentID, value.ReleaseID).Update("active_release_id", "").Error
		}
		return nil
	})
	return changed, err
}

func sameDeploymentStep(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
