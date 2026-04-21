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
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/log"
	"gorm.io/gorm"
)

// ApprovalService provides approval request operations.
type ApprovalService struct {
	approvalRepo repo.IApprovalRepository
}

// NewApprovalService creates a new ApprovalService.
func NewApprovalService(approvalRepo repo.IApprovalRepository) *ApprovalService {
	return &ApprovalService{approvalRepo: approvalRepo}
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
	updates := map[string]any{
		"status":      model.ApprovalStatusApproved,
		"approved_by": approvedBy,
		"reason":      reason,
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
	updates := map[string]any{
		"status":      model.ApprovalStatusRejected,
		"approved_by": rejectedBy,
		"reason":      reason,
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
