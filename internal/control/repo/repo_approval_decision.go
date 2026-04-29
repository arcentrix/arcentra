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

package repo

import (
	"context"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
)

// IApprovalDecisionRepository 定义审批决策仓储接口
type IApprovalDecisionRepository interface {
	Create(ctx context.Context, decision *model.ApprovalDecision) error
	GetByApprovalAndUser(ctx context.Context, approvalID, userID string) (*model.ApprovalDecision, error)
	ListByApproval(ctx context.Context, approvalID string) ([]model.ApprovalDecision, error)
}

// ApprovalDecisionRepo 表示审批决策仓储实现
type ApprovalDecisionRepo struct {
	database.IDatabase
}

// NewApprovalDecisionRepo 创建审批决策仓储
func NewApprovalDecisionRepo(db database.IDatabase) IApprovalDecisionRepository {
	return &ApprovalDecisionRepo{IDatabase: db}
}

// Create 创建审批决策记录
func (r *ApprovalDecisionRepo) Create(ctx context.Context, decision *model.ApprovalDecision) error {
	return r.Database().WithContext(ctx).Create(decision).Error
}

// GetByApprovalAndUser 查询指定审批单下的用户决策
func (r *ApprovalDecisionRepo) GetByApprovalAndUser(ctx context.Context, approvalID, userID string) (*model.ApprovalDecision, error) {
	var decision model.ApprovalDecision
	err := r.Database().WithContext(ctx).
		Where("approval_id = ? AND user_id = ?", approvalID, userID).
		First(&decision).Error
	if err != nil {
		return nil, err
	}
	return &decision, nil
}

// ListByApproval 查询审批单的全部决策
func (r *ApprovalDecisionRepo) ListByApproval(ctx context.Context, approvalID string) ([]model.ApprovalDecision, error) {
	var decisions []model.ApprovalDecision
	err := r.Database().WithContext(ctx).
		Where("approval_id = ?", approvalID).
		Order("created_at ASC").
		Find(&decisions).Error
	return decisions, err
}
