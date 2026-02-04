// Copyright 2025 Arcade Team
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
	"github.com/arcentrix/arcade/internal/engine/model"
	"github.com/arcentrix/arcade/pkg/database"
)

type IRoleMenuBindingRepository interface {
	GetRoleMenuBindings(roleId string) ([]model.RoleMenuBinding, error)
	GetRoleMenuBindingsByResource(roleId, resourceId string) ([]model.RoleMenuBinding, error)
	GetMenuBindingsByRoles(roleIds []string, resourceId string) ([]model.RoleMenuBinding, error)
	CreateRoleMenuBinding(binding *model.RoleMenuBinding) error
	DeleteRoleMenuBinding(roleMenuId string) error
}

type RoleMenuBindingRepo struct {
	database.IDatabase
}

func NewRoleMenuBindingRepo(db database.IDatabase) IRoleMenuBindingRepository {
	return &RoleMenuBindingRepo{
		IDatabase: db,
	}
}

// GetRoleMenuBindings 获取角色的所有菜单绑定
func (r *RoleMenuBindingRepo) GetRoleMenuBindings(roleId string) ([]model.RoleMenuBinding, error) {
	var bindings []model.RoleMenuBinding
	err := r.Database().Select("id", "role_menu_id", "role_id", "menu_id", "resource_id", "is_visible", "is_accessible", "created_at", "updated_at").
		Where("role_id = ? AND is_accessible = ?", roleId, model.RoleMenuAccessible).Find(&bindings).Error
	return bindings, err
}

// GetRoleMenuBindingsByResource 获取角色在指定资源下的菜单绑定
func (r *RoleMenuBindingRepo) GetRoleMenuBindingsByResource(roleId, resourceId string) ([]model.RoleMenuBinding, error) {
	var bindings []model.RoleMenuBinding
	query := r.Database().Select("id", "role_menu_id", "role_id", "menu_id", "resource_id", "is_visible", "is_accessible", "created_at", "updated_at").
		Where("role_id = ? AND is_accessible = ?", roleId, model.RoleMenuAccessible)
	if resourceId == "" {
		query = query.Where("resource_id IS NULL OR resource_id = ''")
	} else {
		query = query.Where("resource_id = ?", resourceId)
	}
	err := query.Find(&bindings).Error
	return bindings, err
}

// GetMenuBindingsByRoles 获取多个角色在指定资源下的菜单绑定
func (r *RoleMenuBindingRepo) GetMenuBindingsByRoles(roleIds []string, resourceId string) ([]model.RoleMenuBinding, error) {
	if len(roleIds) == 0 {
		return []model.RoleMenuBinding{}, nil
	}
	var bindings []model.RoleMenuBinding
	query := r.Database().Select("id", "role_menu_id", "role_id", "menu_id", "resource_id", "is_visible", "is_accessible", "created_at", "updated_at").
		Where("role_id IN ? AND is_accessible = ?", roleIds, model.RoleMenuAccessible)
	if resourceId == "" {
		query = query.Where("resource_id IS NULL OR resource_id = ''")
	} else {
		query = query.Where("resource_id = ?", resourceId)
	}
	err := query.Find(&bindings).Error
	return bindings, err
}

// CreateRoleMenuBinding 创建角色菜单绑定
func (r *RoleMenuBindingRepo) CreateRoleMenuBinding(binding *model.RoleMenuBinding) error {
	return r.Database().Create(binding).Error
}

// DeleteRoleMenuBinding 删除角色菜单绑定
func (r *RoleMenuBindingRepo) DeleteRoleMenuBinding(roleMenuId string) error {
	return r.Database().Where("role_menu_id = ?", roleMenuId).Delete(&model.RoleMenuBinding{}).Error
}
