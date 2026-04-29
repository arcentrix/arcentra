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

// IApprovalPolicyRepository 定义审批策略仓储接口
type IApprovalPolicyRepository interface {
	Create(ctx context.Context, policy *model.ApprovalPolicy) error
	Update(ctx context.Context, policyID string, updates map[string]any) error
	Get(ctx context.Context, policyID string) (*model.ApprovalPolicy, error)
	ListByScope(ctx context.Context, scopeType, scopeID string) ([]model.ApprovalPolicy, error)
	Delete(ctx context.Context, policyID string) error
}

// ApprovalPolicyRepo 表示审批策略仓储实现
type ApprovalPolicyRepo struct {
	database.IDatabase
}

// NewApprovalPolicyRepo 创建审批策略仓储
func NewApprovalPolicyRepo(db database.IDatabase) IApprovalPolicyRepository {
	return &ApprovalPolicyRepo{IDatabase: db}
}

// Create 创建审批策略
func (r *ApprovalPolicyRepo) Create(ctx context.Context, policy *model.ApprovalPolicy) error {
	return r.Database().WithContext(ctx).Create(policy).Error
}

// Update 更新审批策略
func (r *ApprovalPolicyRepo) Update(ctx context.Context, policyID string, updates map[string]any) error {
	return r.Database().WithContext(ctx).Model(&model.ApprovalPolicy{}).
		Where("policy_id = ?", policyID).
		Updates(updates).Error
}

// Get 获取审批策略
func (r *ApprovalPolicyRepo) Get(ctx context.Context, policyID string) (*model.ApprovalPolicy, error) {
	var policy model.ApprovalPolicy
	err := r.Database().WithContext(ctx).
		Where("policy_id = ?", policyID).
		First(&policy).Error
	if err != nil {
		return nil, err
	}
	return &policy, nil
}

// ListByScope 返回作用域下的审批策略
func (r *ApprovalPolicyRepo) ListByScope(ctx context.Context, scopeType, scopeID string) ([]model.ApprovalPolicy, error) {
	var policies []model.ApprovalPolicy
	err := r.Database().WithContext(ctx).
		Where("scope_type = ? AND scope_id = ? AND enabled = ?", scopeType, scopeID, 1).
		Order("created_at DESC").
		Find(&policies).Error
	return policies, err
}

// Delete 删除审批策略
func (r *ApprovalPolicyRepo) Delete(ctx context.Context, policyID string) error {
	return r.Database().WithContext(ctx).
		Where("policy_id = ?", policyID).
		Delete(&model.ApprovalPolicy{}).Error
}
