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

// RoleGrantService 表示角色授权服务
type RoleGrantService struct {
	repo repo.IRoleGrantRepository
}

// NewRoleGrantService 创建角色授权服务
func NewRoleGrantService(grantRepo repo.IRoleGrantRepository) *RoleGrantService {
	return &RoleGrantService{repo: grantRepo}
}

// Grant 授予角色
func (s *RoleGrantService) Grant(ctx context.Context, grant *model.RoleGrant) error {
	if grant.GrantID == "" {
		grant.GrantID = uuid.NewString()
	}
	if grant.IsEnabled == 0 {
		grant.IsEnabled = 1
	}
	return s.repo.Create(ctx, grant)
}

// RevokeByGrantID 撤销授权
func (s *RoleGrantService) RevokeByGrantID(ctx context.Context, grantID string) error {
	return s.repo.DisableGrant(ctx, grantID)
}

// ListBySubject 查询主体授权列表
func (s *RoleGrantService) ListBySubject(ctx context.Context, subjectType, subjectID string) ([]model.RoleGrant, error) {
	return s.repo.ListBySubject(ctx, subjectType, subjectID)
}
