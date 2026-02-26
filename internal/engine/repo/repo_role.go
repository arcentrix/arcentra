// Copyright 2025 Arcentra Authors.
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

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/database"
)

// IRoleRepository defines role persistence with context support for timeout, tracing and cancellation.
type IRoleRepository interface {
	Create(ctx context.Context, role *model.Role) error
	Get(ctx context.Context, roleId string) (*model.Role, error)
	BatchGet(ctx context.Context, roleIds []string) ([]model.Role, error)
	List(ctx context.Context, pageNum, pageSize int) ([]model.Role, int64, error)
	Update(ctx context.Context, roleId string, updates map[string]any) error
	Delete(ctx context.Context, roleId string) error
}

type RoleRepo struct {
	database.IDatabase
}

// NewRoleRepo creates a role repository.
func NewRoleRepo(db database.IDatabase) IRoleRepository {
	return &RoleRepo{IDatabase: db}
}

var roleSelectFields = []string{"id", "role_id", "name", "display_name", "description", "is_enabled", "created_at", "updated_at"}

// Create creates a new role.
func (r *RoleRepo) Create(ctx context.Context, role *model.Role) error {
	return r.Database().WithContext(ctx).Table(role.TableName()).Create(role).Error
}

// Get returns role by roleId.
func (r *RoleRepo) Get(ctx context.Context, roleId string) (*model.Role, error) {
	var role model.Role
	err := r.Database().WithContext(ctx).Select(roleSelectFields).
		Where("role_id = ? AND is_enabled = ?", roleId, 1).First(&role).Error
	if err != nil {
		return nil, err
	}
	return &role, nil
}

// BatchGet returns roles by roleIds.
func (r *RoleRepo) BatchGet(ctx context.Context, roleIds []string) ([]model.Role, error) {
	if len(roleIds) == 0 {
		return []model.Role{}, nil
	}
	var roles []model.Role
	err := r.Database().WithContext(ctx).Select(roleSelectFields).
		Where("role_id IN ? AND is_enabled = ?", roleIds, 1).Find(&roles).Error
	return roles, err
}

// List lists roles with pagination.
func (r *RoleRepo) List(ctx context.Context, pageNum, pageSize int) ([]model.Role, int64, error) {
	var roles []model.Role
	var role model.Role
	var count int64
	offset := (pageNum - 1) * pageSize

	if err := r.Database().WithContext(ctx).Table(role.TableName()).Count(&count).Error; err != nil {
		return nil, 0, err
	}

	if err := r.Database().WithContext(ctx).Select(roleSelectFields).
		Table(role.TableName()).
		Offset(offset).Limit(pageSize).
		Order("created_at DESC").
		Find(&roles).Error; err != nil {
		return nil, 0, err
	}
	return roles, count, nil
}

// Update updates role by roleId.
func (r *RoleRepo) Update(ctx context.Context, roleId string, updates map[string]any) error {
	return r.Database().WithContext(ctx).Table((&model.Role{}).TableName()).
		Where("role_id = ?", roleId).Updates(updates).Error
}

// Delete soft-deletes role by roleId (sets is_enabled=0).
func (r *RoleRepo) Delete(ctx context.Context, roleId string) error {
	return r.Database().WithContext(ctx).Table((&model.Role{}).TableName()).
		Where("role_id = ?", roleId).Updates(map[string]any{"is_enabled": 0}).Error
}
