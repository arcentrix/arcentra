// Copyright 2026 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ApprovalService provides approval request operations.
type ApprovalService struct {
	approvalRepo repo.IApprovalRepository
	decisionRepo repo.IApprovalDecisionRepository
	policyRepo   repo.IApprovalPolicyRepository
}

// NewApprovalService creates a new ApprovalService.
func NewApprovalService(
	approvalRepo repo.IApprovalRepository,
	decisionRepo repo.IApprovalDecisionRepository,
	policyRepo repo.IApprovalPolicyRepository,
) *ApprovalService {
	return &ApprovalService{
		approvalRepo: approvalRepo,
		decisionRepo: decisionRepo,
		policyRepo:   policyRepo,
	}
}

// GetApproval returns an approval request by ID.
func (s *ApprovalService) GetApproval(ctx context.Context, approvalID string) (*model.ApprovalRequest, error) {
	req, err := s.approvalRepo.GetByApprovalID(ctx, approvalID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("approval request not found")
		}
		log.Errorw("failed to get approval request", "approvalId", approvalID, "error", err)
		return nil, errors.New("failed to get approval request")
	}
	return req, nil
}

// Approve approves a pending approval request.
func (s *ApprovalService) Approve(ctx context.Context, approvalID, approvedBy, reason string) error {
	req, err := s.approvalRepo.GetByApprovalID(ctx, approvalID)
	if err != nil {
		return errors.New("approval request not found")
	}
	if req.Status != model.ApprovalStatusPending {
		return errors.New("approval request is not pending")
	}
	if req.ExpiresAt != nil && time.Now().After(*req.ExpiresAt) {
		_ = s.approvalRepo.UpdateByApprovalID(ctx, approvalID, map[string]any{
			"status": model.ApprovalStatusExpired,
		})
		return errors.New("approval request has expired")
	}
	if s.decisionRepo != nil {
		if oldDecision, getErr := s.decisionRepo.GetByApprovalAndUser(ctx, approvalID, approvedBy); getErr == nil && oldDecision != nil {
			return errors.New("approval already decided by this user")
		}
		decision := &model.ApprovalDecision{
			DecisionID: uuid.NewString(),
			ApprovalID: approvalID,
			UserID:     approvedBy,
			Decision:   model.ApprovalDecisionApprove,
			Comment:    reason,
		}
		if createErr := s.decisionRepo.Create(ctx, decision); createErr != nil {
			log.Errorw("failed to create approval decision", "approvalId", approvalID, "error", createErr)
			return errors.New("failed to create approval decision")
		}
	}

	approvedCount := req.ApprovedCount + 1
	requiredCount := req.RequiredApproverCount
	if requiredCount <= 0 {
		requiredCount = 1
	}
	nextStatus := model.ApprovalStatusPending
	if approvedCount >= requiredCount {
		nextStatus = model.ApprovalStatusApproved
	}
	updates := map[string]any{
		"status":         nextStatus,
		"approved_count": approvedCount,
		"approved_by":    approvedBy,
		"reason":         reason,
	}
	if err := s.approvalRepo.UpdateByApprovalID(ctx, approvalID, updates); err != nil {
		log.Errorw("failed to approve request", "approvalId", approvalID, "error", err)
		return errors.New("failed to approve request")
	}
	log.Infow("approval request approved", "approvalId", approvalID, "approvedBy", approvedBy)
	return nil
}

// Reject rejects a pending approval request.
func (s *ApprovalService) Reject(ctx context.Context, approvalID, rejectedBy, reason string) error {
	req, err := s.approvalRepo.GetByApprovalID(ctx, approvalID)
	if err != nil {
		return errors.New("approval request not found")
	}
	if req.Status != model.ApprovalStatusPending {
		return errors.New("approval request is not pending")
	}
	if s.decisionRepo != nil {
		if oldDecision, getErr := s.decisionRepo.GetByApprovalAndUser(ctx, approvalID, rejectedBy); getErr == nil && oldDecision != nil {
			return errors.New("approval already decided by this user")
		}
		decision := &model.ApprovalDecision{
			DecisionID: uuid.NewString(),
			ApprovalID: approvalID,
			UserID:     rejectedBy,
			Decision:   model.ApprovalDecisionReject,
			Comment:    reason,
		}
		if createErr := s.decisionRepo.Create(ctx, decision); createErr != nil {
			log.Errorw("failed to create reject decision", "approvalId", approvalID, "error", createErr)
			return errors.New("failed to create reject decision")
		}
	}

	rejectedCount := req.RejectedCount + 1
	updates := map[string]any{
		"status":         model.ApprovalStatusRejected,
		"rejected_count": rejectedCount,
		"approved_by":    rejectedBy,
		"reason":         reason,
	}
	if err := s.approvalRepo.UpdateByApprovalID(ctx, approvalID, updates); err != nil {
		log.Errorw("failed to reject request", "approvalId", approvalID, "error", err)
		return errors.New("failed to reject request")
	}
	log.Infow("approval request rejected", "approvalId", approvalID, "rejectedBy", rejectedBy)
	return nil
}

// ListByPipelineRun lists all approval requests for a given pipeline run.
func (s *ApprovalService) ListByPipelineRun(ctx context.Context, pipelineRunID string) ([]*model.ApprovalRequest, error) {
	return s.approvalRepo.ListByPipelineRunID(ctx, pipelineRunID)
}

// CreateApprovalRequest 创建审批请求
func (s *ApprovalService) CreateApprovalRequest(ctx context.Context, req *model.ApprovalRequest) error {
	if req.ApprovalID == "" {
		req.ApprovalID = uuid.NewString()
	}
	if req.RequiredApproverCount <= 0 {
		req.RequiredApproverCount = 1
	}
	if req.Mode == "" {
		req.Mode = "any"
	}
	req.Status = model.ApprovalStatusPending
	return s.approvalRepo.Create(ctx, req)
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
		return nil, gorm.ErrRecordNotFound
	}
	return &policies[0], nil
}
