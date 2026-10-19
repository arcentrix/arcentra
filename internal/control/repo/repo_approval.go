// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrApprovalNotFound 表示审批请求或投票不存在
	ErrApprovalNotFound = gorm.ErrRecordNotFound
	// ErrApprovalInvalid 表示审批输入或候选快照无效
	ErrApprovalInvalid = errors.New("invalid approval request")
	// ErrApprovalForbidden 表示操作者没有候选资格
	ErrApprovalForbidden = errors.New("actor is not an eligible approval candidate")
	// ErrApprovalConflict 表示投票或终态冲突
	ErrApprovalConflict = errors.New("approval decision conflicts with existing state")
	// ErrApprovalExpired 表示审批已超过截止时间
	ErrApprovalExpired = errors.New("approval request has expired")
)

// IApprovalRepository 定义审批请求及原子决策操作
// UpdateByApprovalID 只兼容过期与取消，决策必须通过 Decide 提交
// 所有终态不可逆，失败事务不留下投票或计数
type IApprovalRepository interface {
	Create(context.Context, *model.ApprovalRequest) error
	GetByApprovalID(context.Context, string) (*model.ApprovalRequest, error)
	UpdateByApprovalID(context.Context, string, map[string]any) error
	ListByPipelineRunID(context.Context, string) ([]*model.ApprovalRequest, error)
	Decide(ctx context.Context, approvalID, actorID, decision, reason string) (*model.ApprovalRequest, error)
	Expire(ctx context.Context, approvalID string) (*model.ApprovalRequest, error)
	Cancel(ctx context.Context, approvalID, reason string) (*model.ApprovalRequest, error)
}

// ApprovalRepo 使用数据库事务保存审批请求与投票
type ApprovalRepo struct{ database.IDatabase }

// NewApprovalRepo 创建审批仓储，不执行数据库迁移
func NewApprovalRepo(db database.IDatabase) IApprovalRepository { return &ApprovalRepo{db} }

var approvalColumns = []string{"approval_id", "pipeline_run_id", "job_name", "step_name", "plugin", "status", "policy_id", "mode", "required_approver_count", "approved_count", "rejected_count", "environment", "requested_by", "approved_by", "reason", "callback_url", "notify_channels", "candidate_approvers", "expires_at", "deployment_release_id", "evidence_hash", "version", "created_at", "updated_at", "retry_job_run_id", "binding_kind", "evidence_json"}
var approvalDecisionColumns = []string{"decision_id", "approval_id", "user_id", "decision", "comment", "created_at", "updated_at"}

// Create 保存经过校验的待审批请求
func (r *ApprovalRepo) Create(ctx context.Context, req *model.ApprovalRequest) error {
	if req == nil || req.InboxOrigin != 0 || req.RetryJobRunID != nil || req.Plugin == retryapproval.Plugin || req.BindingKind != "" || req.EvidenceJSON != "" || !validApprovalID(req.ApprovalID) || !validApprovalID(req.PipelineRunID) || req.Status != model.ApprovalStatusPending || req.ApprovedCount != 0 || req.RejectedCount != 0 {
		return ErrApprovalInvalid
	}
	if _, err := model.ApprovalCandidates(req); err != nil {
		return ErrApprovalInvalid
	}
	req.Version = 1
	return r.Database().WithContext(ctx).Create(req).Error
}

// GetByApprovalID 按业务标识读取审批请求
func (r *ApprovalRepo) GetByApprovalID(ctx context.Context, id string) (*model.ApprovalRequest, error) {
	var value model.ApprovalRequest
	err := r.Database().WithContext(ctx).Select(approvalColumns).Where("approval_id = ?", id).First(&value).Error
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// UpdateByApprovalID 仅兼容安全的取消或过期操作，不允许改写身份和决策计数
func (r *ApprovalRepo) UpdateByApprovalID(ctx context.Context, id string, updates map[string]any) error {
	status, ok := updates["status"].(int)
	if !ok || len(updates) > 2 {
		return ErrApprovalInvalid
	}
	reason := ""
	for k, v := range updates {
		if k == "status" {
			continue
		}
		if k != "reason" {
			return ErrApprovalInvalid
		}
		var valid bool
		reason, valid = v.(string)
		if !valid {
			return ErrApprovalInvalid
		}
	}
	var err error
	switch status {
	case model.ApprovalStatusExpired:
		_, err = r.Expire(ctx, id)
	case model.ApprovalStatusCanceled:
		_, err = r.Cancel(ctx, id, reason)
	default:
		return ErrApprovalInvalid
	}
	return err
}

// ListByPipelineRunID 返回指定流水线执行的审批请求
func (r *ApprovalRepo) ListByPipelineRunID(ctx context.Context, id string) ([]*model.ApprovalRequest, error) {
	var values []*model.ApprovalRequest
	err := r.Database().WithContext(ctx).Select(approvalColumns).Where("pipeline_run_id = ?", id).Order("created_at ASC, approval_id ASC").Find(&values).Error
	return values, err
}

func validApprovalID(value string) bool {
	return value != "" && len(value) <= 64 && strings.TrimSpace(value) == value
}

// lockApproval 先获取写锁再读取，兼容 MySQL 行锁与 SQLite 多连接写事务
func lockApproval(tx *gorm.DB, id string) (*model.ApprovalRequest, error) {
	if err := tx.Model(&model.ApprovalRequest{}).Where("approval_id = ?", id).UpdateColumn("version", gorm.Expr("version")).Error; err != nil {
		return nil, err
	}
	var value model.ApprovalRequest
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(approvalColumns).Where("approval_id = ?", id).First(&value).Error; err != nil {
		return nil, err
	}
	return &value, nil
}

func patchPendingApproval(tx *gorm.DB, value *model.ApprovalRequest, patch map[string]any) error {
	patch["version"] = value.Version + 1
	result := tx.Model(&model.ApprovalRequest{}).Where("approval_id = ? AND status = ? AND version = ?", value.ApprovalID, model.ApprovalStatusPending, value.Version).Updates(patch)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrApprovalConflict
	}
	value.Version++
	return nil
}

// Decide 在一个事务中校验候选人、记录唯一投票并推进审批状态
func (r *ApprovalRepo) Decide(ctx context.Context, id, actor, decision, reason string) (*model.ApprovalRequest, error) {
	if !validApprovalID(id) || !validApprovalID(actor) || len(reason) > 2048 || (decision != model.ApprovalDecisionApprove && decision != model.ApprovalDecisionReject) {
		return nil, ErrApprovalInvalid
	}
	initial, err := r.GetByApprovalID(ctx, id)
	if err != nil {
		return nil, err
	}
	if initial.RetryJobRunID != nil || (initial.BindingKind == retryapproval.Kind || initial.BindingKind == retryapproval.FailedStepKind) || initial.Plugin == retryapproval.Plugin {
		return r.decideRetryApproval(ctx, initial, actor, decision, reason)
	}
	if initial.BindingKind != "" || initial.EvidenceJSON != "" {
		return nil, ErrApprovalInvalid
	}
	var result *model.ApprovalRequest
	var expired bool
	err = r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 与发布取消保持 release -> approval 的固定顺序
		var release *model.DeploymentRelease
		if initial.DeploymentReleaseID != nil {
			release = &model.DeploymentRelease{}
			if err := tx.Model(release).Where("release_id = ?", *initial.DeploymentReleaseID).UpdateColumn("version", gorm.Expr("version")).Error; err != nil {
				return err
			}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(releaseColumns).Where("release_id = ?", *initial.DeploymentReleaseID).First(release).Error; err != nil {
				return ErrApprovalConflict
			}
		}
		value, err := lockApproval(tx, id)
		if err != nil {
			return err
		}
		result = value
		if !sameDeploymentStep(initial.DeploymentReleaseID, value.DeploymentReleaseID) || !model.IsApprovalCandidate(value, actor) {
			return ErrApprovalForbidden
		}
		var old model.ApprovalDecision
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(approvalDecisionColumns).Where("approval_id = ? AND user_id = ?", id, actor).First(&old).Error
		if err == nil {
			if old.Decision == decision {
				if value.Status == model.ApprovalStatusPending && value.ExpiresAt != nil && !time.Now().Before(*value.ExpiresAt) {
					if err := patchPendingApproval(tx, value, map[string]any{"status": model.ApprovalStatusExpired}); err != nil {
						return err
					}
					value.Status = model.ApprovalStatusExpired
				}
				return nil
			}
			return ErrApprovalConflict
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if value.Status == model.ApprovalStatusExpired {
			return ErrApprovalExpired
		}
		if value.Status != model.ApprovalStatusPending {
			return ErrApprovalConflict
		}
		if value.ExpiresAt != nil && !time.Now().Before(*value.ExpiresAt) {
			if err := patchPendingApproval(tx, value, map[string]any{"status": model.ApprovalStatusExpired}); err != nil {
				return err
			}
			value.Status, expired = model.ApprovalStatusExpired, true
			return nil
		}
		if release != nil {
			if err := validateApprovalDeployment(tx, value, release); err != nil {
				return err
			}
		}
		// 关联行可能经历锁等待，必须在获得全部执行锁后再次判断截止时间
		if approvalDeadlinePassed(value) {
			if err := patchPendingApproval(tx, value, map[string]any{"status": model.ApprovalStatusExpired}); err != nil {
				return err
			}
			value.Status, expired = model.ApprovalStatusExpired, true
			return nil
		}
		vote := &model.ApprovalDecision{DecisionID: uuid.NewString(), ApprovalID: id, UserID: actor, Decision: decision, Comment: reason}
		if err := tx.Create(vote).Error; err != nil {
			return err
		}
		var votes []model.ApprovalDecision
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(approvalDecisionColumns).Where("approval_id = ?", id).Find(&votes).Error; err != nil {
			return err
		}
		approved, rejected := 0, 0
		voters := make(map[string]bool, len(votes))
		for _, recorded := range votes {
			if !model.IsApprovalCandidate(value, recorded.UserID) || voters[recorded.UserID] {
				return ErrApprovalInvalid
			}
			voters[recorded.UserID] = true
			switch recorded.Decision {
			case model.ApprovalDecisionApprove:
				approved++
			case model.ApprovalDecisionReject:
				rejected++
			default:
				return ErrApprovalInvalid
			}
		}
		next := model.ApprovalStatusPending
		if rejected > 0 {
			next = model.ApprovalStatusRejected
		} else if approved >= value.RequiredApproverCount {
			next = model.ApprovalStatusApproved
		}
		// 投票写入或聚合期间也可能跨过截止时间，事务回滚必须移除新投票
		if approvalDeadlinePassed(value) {
			return ErrApprovalExpired
		}
		if err := patchPendingApproval(tx, value, map[string]any{"status": next, "approved_count": approved, "rejected_count": rejected, "approved_by": actor, "reason": reason}); err != nil {
			return err
		}
		if approvalDeadlinePassed(value) {
			return ErrApprovalExpired
		}
		value.Status, value.ApprovedCount, value.RejectedCount, value.ApprovedBy, value.Reason = next, approved, rejected, actor, reason
		return nil
	})
	if errors.Is(err, ErrApprovalExpired) {
		// 先回滚晚到的投票，再尝试持久化过期；读取路径也会重试此单调终态转换
		current, expireErr := r.Expire(ctx, id)
		if expireErr != nil {
			return nil, expireErr
		}
		return current, ErrApprovalExpired
	}
	if err != nil {
		return nil, err
	}
	if expired {
		return result, ErrApprovalExpired
	}
	return result, nil
}

func approvalDeadlinePassed(value *model.ApprovalRequest) bool {
	return value.ExpiresAt != nil && !time.Now().Before(*value.ExpiresAt)
}

func validateApprovalDeployment(tx *gorm.DB, approval *model.ApprovalRequest, release *model.DeploymentRelease) error {
	if release.Origin != "pipeline_job" || release.Status != "queued" || release.PipelineRunID != approval.PipelineRunID || release.EnvironmentID != approval.Environment || release.ApprovalID == nil || *release.ApprovalID != approval.ApprovalID || release.ApprovalEvidenceHash == "" || release.ApprovalEvidenceHash != approval.EvidenceHash || release.ExecutionStepRunID == nil || release.CreatedBy != approval.RequestedBy {
		return ErrApprovalConflict
	}
	var run model.PipelineRun
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("run_id").Where("run_id = ? AND pipeline_id = ? AND status = ? AND allow_deployments = ? AND deployment_actor_id = ? AND deployment_plan_hash = ? AND deployment_target_fingerprint = ? AND definition_commit_sha = ?", release.PipelineRunID, release.PipelineID, model.PipelineStatusRunning, true, release.CreatedBy, release.DeploymentPlanHash, release.TargetFingerprint, release.CommitSHA).First(&run).Error; err != nil {
		return ErrApprovalConflict
	}
	var job model.JobRun
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("job_run_id").Where("job_run_id = ? AND pipeline_run_id = ? AND pipeline_id = ? AND status = ?", release.ExecutionJobRunID, release.PipelineRunID, release.PipelineID, model.JobRunStatusRunning).First(&job).Error; err != nil {
		return ErrApprovalConflict
	}
	var step model.StepRun
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("step_run_id").Where("step_run_id = ? AND job_run_id = ? AND pipeline_run_id = ? AND pipeline_id = ? AND status = ? AND deployment_release_id = ?", *release.ExecutionStepRunID, release.ExecutionJobRunID, release.PipelineRunID, release.PipelineID, 3, release.ReleaseID).First(&step).Error; err != nil {
		return ErrApprovalConflict
	}
	var environment model.DeploymentEnvironment
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("environment_id").Where("environment_id = ? AND project_id = ? AND active_release_id = ?", release.EnvironmentID, release.ProjectID, release.ReleaseID).First(&environment).Error; err != nil {
		return ErrApprovalConflict
	}
	var pipeline model.Pipeline
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("pipeline_id").Where("pipeline_id = ? AND project_id = ?", release.PipelineID, release.ProjectID).First(&pipeline).Error; err != nil {
		return ErrApprovalConflict
	}
	return nil
}

// Expire 仅将已经超时的待审批请求原子推进到过期状态
func (r *ApprovalRepo) Expire(ctx context.Context, id string) (*model.ApprovalRequest, error) {
	return r.finishPending(ctx, id, model.ApprovalStatusExpired, "")
}

// Cancel 原子取消待审批请求，保留已有投票和终态
func (r *ApprovalRepo) Cancel(ctx context.Context, id, reason string) (*model.ApprovalRequest, error) {
	if len(reason) > 2048 {
		return nil, ErrApprovalInvalid
	}
	return r.finishPending(ctx, id, model.ApprovalStatusCanceled, reason)
}

func (r *ApprovalRepo) finishPending(ctx context.Context, id string, status int, reason string) (*model.ApprovalRequest, error) {
	if !validApprovalID(id) {
		return nil, ErrApprovalInvalid
	}
	initial, err := r.GetByApprovalID(ctx, id)
	if err != nil {
		return nil, err
	}
	var result *model.ApprovalRequest
	err = r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var retryJob *model.JobRun
		if initial.RetryJobRunID != nil || (initial.BindingKind == retryapproval.Kind || initial.BindingKind == retryapproval.FailedStepKind) {
			var err error
			retryJob, err = lockRetryApprovalJob(ctx, tx, initial)
			if err != nil {
				return err
			}
		}
		value, err := lockApproval(tx, id)
		if err != nil {
			return err
		}
		result = value
		if !sameDeploymentStep(initial.RetryJobRunID, value.RetryJobRunID) || initial.BindingKind != value.BindingKind {
			return ErrApprovalConflict
		}
		if value.Status != model.ApprovalStatusPending {
			return nil
		}
		if retryJob != nil {
			if _, _, err := validateRetryApprovalBinding(ctx, tx, retryJob); err != nil {
				return err
			}
		}
		if status == model.ApprovalStatusExpired && (value.ExpiresAt == nil || time.Now().Before(*value.ExpiresAt)) {
			return nil
		}
		patch := map[string]any{"status": status}
		if status == model.ApprovalStatusCanceled {
			patch["reason"] = reason
		}
		if err := patchPendingApproval(tx, value, patch); err != nil {
			return err
		}
		value.Status = status
		if status == model.ApprovalStatusCanceled {
			value.Reason = reason
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
