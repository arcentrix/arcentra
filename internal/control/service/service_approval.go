// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/google/uuid"
)

// ApprovalService 提供审批查询、候选人校验和原子决策入口
type ApprovalService struct {
	approvalRepo      repo.IApprovalRepository
	decisionRepo      repo.IApprovalDecisionRepository
	policyRepo        repo.IApprovalPolicyRepository
	decisionValidator func(context.Context, *model.ApprovalRequest, string, string) error
}

// NewApprovalService 创建审批服务，所有数据库事务由仓储执行
func NewApprovalService(approvals repo.IApprovalRepository, decisions repo.IApprovalDecisionRepository, policies repo.IApprovalPolicyRepository) *ApprovalService {
	return &ApprovalService{approvalRepo: approvals, decisionRepo: decisions, policyRepo: policies}
}

// SetDecisionValidator 在服务启动时设置部署审批的实时权限与证据校验器
func (s *ApprovalService) SetDecisionValidator(validator func(context.Context, *model.ApprovalRequest, string, string) error) {
	s.decisionValidator = validator
}

// GetApproval 返回审批请求，并安全地持久化已经到期的待审批状态
func (s *ApprovalService) GetApproval(ctx context.Context, id string) (*model.ApprovalRequest, error) {
	if s.approvalRepo == nil {
		return nil, errors.New("approval repository not configured")
	}
	value, err := s.approvalRepo.GetByApprovalID(ctx, id)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, repo.ErrApprovalNotFound
	}
	if value.Status == model.ApprovalStatusPending && value.ExpiresAt != nil && !time.Now().Before(*value.ExpiresAt) {
		return s.approvalRepo.Expire(ctx, id)
	}
	return value, nil
}

// Approve 使用已认证操作者提交同意票，不允许匿名或客户端指定身份
func (s *ApprovalService) Approve(ctx context.Context, id, actor, reason string) error {
	_, err := s.Decide(ctx, id, actor, model.ApprovalDecisionApprove, reason)
	return err
}

// Reject 使用已认证操作者提交拒绝票
func (s *ApprovalService) Reject(ctx context.Context, id, actor, reason string) error {
	_, err := s.Decide(ctx, id, actor, model.ApprovalDecisionReject, reason)
	return err
}

// Decide 校验最新部署授权，再由仓储提交唯一且不可逆的审批决策
func (s *ApprovalService) Decide(ctx context.Context, id, actor, decision, reason string) (*model.ApprovalRequest, error) {
	if actor == "" || strings.TrimSpace(actor) != actor || len(actor) > 64 || len(reason) > 2048 || (decision != model.ApprovalDecisionApprove && decision != model.ApprovalDecisionReject) {
		return nil, repo.ErrApprovalInvalid
	}
	value, err := s.GetApproval(ctx, id)
	if err != nil {
		return nil, err
	}
	if !model.IsApprovalCandidate(value, actor) {
		return nil, repo.ErrApprovalForbidden
	}
	// 终态只允许仓储返回同方向的已提交投票，绝不再次启动发布
	if value.Status == model.ApprovalStatusPending {
		if err := s.validateDeploymentDecision(ctx, value, actor, decision); err != nil {
			return nil, err
		}
	}
	return s.approvalRepo.Decide(ctx, id, actor, decision, reason)
}

func (s *ApprovalService) validateDeploymentDecision(ctx context.Context, value *model.ApprovalRequest, actor, decision string) error {
	if value == nil {
		return repo.ErrApprovalInvalid
	}
	if value.BindingKind == retryapproval.Kind || value.RetryJobRunID != nil || value.Plugin == retryapproval.Plugin {
		if value.BindingKind != retryapproval.Kind || value.RetryJobRunID == nil || value.DeploymentReleaseID != nil || value.Plugin != retryapproval.Plugin {
			return repo.ErrApprovalInvalid
		}
		return s.validateRetryDecision(ctx, value, actor, decision)
	}
	if value.BindingKind != "" {
		return repo.ErrApprovalInvalid
	}
	if value.DeploymentReleaseID == nil {
		if value.Plugin == "builtin:deployment" {
			return repo.ErrApprovalInvalid
		}
		return nil
	}
	if s.decisionValidator == nil {
		return errors.New("deployment approval validator not configured")
	}
	return s.decisionValidator(ctx, value, actor, decision)
}

// DecisionEligibility 返回当前用户的历史投票，并校验是否仍能提交新票
func (s *ApprovalService) DecisionEligibility(ctx context.Context, value *model.ApprovalRequest, actor, decision string) (*model.ApprovalDecision, error) {
	if decision != model.ApprovalDecisionApprove && decision != model.ApprovalDecisionReject {
		return nil, repo.ErrApprovalInvalid
	}
	if value == nil || !model.IsApprovalCandidate(value, actor) {
		return nil, repo.ErrApprovalForbidden
	}
	if s.decisionRepo == nil {
		return nil, errors.New("approval decision repository not configured")
	}
	old, err := s.decisionRepo.GetByApprovalAndUser(ctx, value.ApprovalID, actor)
	if err != nil && !errors.Is(err, repo.ErrApprovalNotFound) {
		return nil, err
	}
	if old != nil {
		return old, repo.ErrApprovalConflict
	}
	if value.Status != model.ApprovalStatusPending {
		return nil, repo.ErrApprovalConflict
	}
	if value.ExpiresAt != nil && !time.Now().Before(*value.ExpiresAt) {
		return nil, repo.ErrApprovalExpired
	}
	return nil, s.validateDeploymentDecision(ctx, value, actor, decision)
}

// ListByPipelineRun 返回指定执行的审批请求并处理超时
func (s *ApprovalService) ListByPipelineRun(ctx context.Context, id string) ([]*model.ApprovalRequest, error) {
	if s.approvalRepo == nil {
		return nil, errors.New("approval repository not configured")
	}
	values, err := s.approvalRepo.ListByPipelineRunID(ctx, id)
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		if value == nil || value.PipelineRunID != id {
			return nil, repo.ErrApprovalConflict
		}
		if value.Status == model.ApprovalStatusPending && value.ExpiresAt != nil && !time.Now().Before(*value.ExpiresAt) {
			values[i], err = s.approvalRepo.Expire(ctx, value.ApprovalID)
			if err != nil {
				return nil, err
			}
		}
	}
	return values, nil
}

// CreateApprovalRequest 创建具有明确候选人快照的审批请求
func (s *ApprovalService) CreateApprovalRequest(ctx context.Context, value *model.ApprovalRequest) error {
	if value == nil || s.approvalRepo == nil {
		return repo.ErrApprovalInvalid
	}
	if value.ApprovalID == "" {
		value.ApprovalID = uuid.NewString()
	}
	if value.RequiredApproverCount <= 0 {
		value.RequiredApproverCount = 1
	}
	if value.Mode == "" {
		value.Mode = "any"
	}
	if _, err := model.ApprovalCandidates(value); err != nil {
		return repo.ErrApprovalInvalid
	}
	value.Status, value.ApprovedCount, value.RejectedCount, value.Version = model.ApprovalStatusPending, 0, 0, 1
	return s.approvalRepo.Create(ctx, value)
}

// MatchPolicyByScope 按作用域匹配审批策略
func (s *ApprovalService) MatchPolicyByScope(ctx context.Context, scopeType, scopeID string) (*model.ApprovalPolicy, error) {
	if s.policyRepo == nil {
		return nil, fmt.Errorf("approval policy repository not configured")
	}
	policies, err := s.policyRepo.ListByScope(ctx, scopeType, scopeID)
	if err != nil {
		return nil, err
	}
	if len(policies) == 0 {
		return nil, repo.ErrApprovalNotFound
	}
	return &policies[0], nil
}
