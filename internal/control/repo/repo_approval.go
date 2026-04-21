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

// IApprovalRepository defines persistence operations for approval requests.
type IApprovalRepository interface {
	Create(ctx context.Context, req *model.ApprovalRequest) error
	GetByApprovalID(ctx context.Context, approvalID string) (*model.ApprovalRequest, error)
	UpdateByApprovalID(ctx context.Context, approvalID string, updates map[string]any) error
	ListByPipelineRunID(ctx context.Context, pipelineRunID string) ([]*model.ApprovalRequest, error)
}

// ApprovalRepo implements IApprovalRepository using GORM.
type ApprovalRepo struct {
	database.IDatabase
}

// NewApprovalRepo creates a new approval repository.
func NewApprovalRepo(db database.IDatabase) IApprovalRepository {
	return &ApprovalRepo{IDatabase: db}
}

// Create persists a new approval request.
func (r *ApprovalRepo) Create(ctx context.Context, req *model.ApprovalRequest) error {
	return r.Database().WithContext(ctx).Create(req).Error
}

// GetByApprovalID returns an approval request by its business ID.
func (r *ApprovalRepo) GetByApprovalID(ctx context.Context, approvalID string) (*model.ApprovalRequest, error) {
	var req model.ApprovalRequest
	err := r.Database().WithContext(ctx).
		Where("approval_id = ?", approvalID).
		First(&req).Error
	if err != nil {
		return nil, err
	}
	return &req, nil
}

// UpdateByApprovalID patches an approval request by its business ID.
func (r *ApprovalRepo) UpdateByApprovalID(ctx context.Context, approvalID string, updates map[string]any) error {
	return r.Database().WithContext(ctx).
		Model(&model.ApprovalRequest{}).
		Where("approval_id = ?", approvalID).
		Updates(updates).Error
}

// ListByPipelineRunID returns all approval requests for a pipeline run.
func (r *ApprovalRepo) ListByPipelineRunID(ctx context.Context, pipelineRunID string) ([]*model.ApprovalRequest, error) {
	var reqs []*model.ApprovalRequest
	err := r.Database().WithContext(ctx).
		Where("pipeline_run_id = ?", pipelineRunID).
		Order("created_at ASC").
		Find(&reqs).Error
	return reqs, err
}
