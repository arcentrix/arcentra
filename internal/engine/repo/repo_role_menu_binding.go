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

// IRoleMenuBindingRepository defines role menu binding persistence with context support.
type IRoleMenuBindingRepository interface {
	List(ctx context.Context, roleId string) ([]model.RoleMenuBinding, error)
	ListByResource(ctx context.Context, roleId, resourceId string) ([]model.RoleMenuBinding, error)
	ListByRoles(ctx context.Context, roleIds []string, resourceId string) ([]model.RoleMenuBinding, error)
	Create(ctx context.Context, binding *model.RoleMenuBinding) error
	Delete(ctx context.Context, roleMenuId string) error
}

type RoleMenuBindingRepo struct {
	database.IDatabase
}

func NewRoleMenuBindingRepo(db database.IDatabase) IRoleMenuBindingRepository {
	return &RoleMenuBindingRepo{
		IDatabase: db,
	}
}

// List returns role menu bindings by roleId.
func (r *RoleMenuBindingRepo) List(ctx context.Context, roleId string) ([]model.RoleMenuBinding, error) {
	var bindings []model.RoleMenuBinding
	err := r.Database().WithContext(ctx).Select("id", "role_menu_id", "role_id", "menu_id", "resource_id", "is_visible", "is_accessible", "created_at", "updated_at").
		Where("role_id = ? AND is_accessible = ?", roleId, model.RoleMenuAccessible).Find(&bindings).Error
	return bindings, err
}

// ListByResource returns role menu bindings by roleId and resourceId.
func (r *RoleMenuBindingRepo) ListByResource(ctx context.Context, roleId, resourceId string) ([]model.RoleMenuBinding, error) {
	var bindings []model.RoleMenuBinding
	query := r.Database().WithContext(ctx).Select("id", "role_menu_id", "role_id", "menu_id", "resource_id", "is_visible", "is_accessible", "created_at", "updated_at").
		Where("role_id = ? AND is_accessible = ?", roleId, model.RoleMenuAccessible)
	if resourceId == "" {
		query = query.Where("resource_id IS NULL OR resource_id = ''")
	} else {
		query = query.Where("resource_id = ?", resourceId)
	}
	err := query.Find(&bindings).Error
	return bindings, err
}

// ListByRoles returns role menu bindings by roleIds and resourceId.
func (r *RoleMenuBindingRepo) ListByRoles(ctx context.Context, roleIds []string, resourceId string) ([]model.RoleMenuBinding, error) {
	if len(roleIds) == 0 {
		return []model.RoleMenuBinding{}, nil
	}
	var bindings []model.RoleMenuBinding
	query := r.Database().WithContext(ctx).Select("id", "role_menu_id", "role_id", "menu_id", "resource_id", "is_visible", "is_accessible", "created_at", "updated_at").
		Where("role_id IN ? AND is_accessible = ?", roleIds, model.RoleMenuAccessible)
	if resourceId == "" {
		query = query.Where("resource_id IS NULL OR resource_id = ''")
	} else {
		query = query.Where("resource_id = ?", resourceId)
	}
	err := query.Find(&bindings).Error
	return bindings, err
}

// Create creates a role menu binding.
func (r *RoleMenuBindingRepo) Create(ctx context.Context, binding *model.RoleMenuBinding) error {
	return r.Database().WithContext(ctx).Create(binding).Error
}

// Delete deletes role menu binding by roleMenuId.
func (r *RoleMenuBindingRepo) Delete(ctx context.Context, roleMenuId string) error {
	return r.Database().WithContext(ctx).Where("role_menu_id = ?", roleMenuId).Delete(&model.RoleMenuBinding{}).Error
}
