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

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/google/uuid"
)

// ApprovalPolicyService 表示审批策略服务
type ApprovalPolicyService struct {
	repo repo.IApprovalPolicyRepository
}

// NewApprovalPolicyService 创建审批策略服务
func NewApprovalPolicyService(policyRepo repo.IApprovalPolicyRepository) *ApprovalPolicyService {
	return &ApprovalPolicyService{repo: policyRepo}
}

// Create 创建审批策略
func (s *ApprovalPolicyService) Create(ctx context.Context, policy *model.ApprovalPolicy) error {
	if policy.PolicyID == "" {
		policy.PolicyID = uuid.NewString()
	}
	if policy.Enabled == 0 {
		policy.Enabled = 1
	}
	if policy.MinApprovals <= 0 {
		policy.MinApprovals = 1
	}
	if policy.TimeoutSeconds <= 0 {
		policy.TimeoutSeconds = 3600
	}
	return s.repo.Create(ctx, policy)
}

// Update 更新审批策略
func (s *ApprovalPolicyService) Update(ctx context.Context, policyID string, updates map[string]any) error {
	return s.repo.Update(ctx, policyID, updates)
}

// Get 查询审批策略
func (s *ApprovalPolicyService) Get(ctx context.Context, policyID string) (*model.ApprovalPolicy, error) {
	return s.repo.Get(ctx, policyID)
}

// ListByScope 查询指定作用域审批策略
func (s *ApprovalPolicyService) ListByScope(ctx context.Context, scopeType, scopeID string) ([]model.ApprovalPolicy, error) {
	return s.repo.ListByScope(ctx, scopeType, scopeID)
}

// Delete 删除审批策略
func (s *ApprovalPolicyService) Delete(ctx context.Context, policyID string) error {
	return s.repo.Delete(ctx, policyID)
}
