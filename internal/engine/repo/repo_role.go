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

type IRoleRepository interface {
	CreateRole(role *model.Role) error
	GetRole(roleId string) (*model.Role, error)
	GetRoleById(id uint64) (*model.Role, error)
	GetRolesByRoleIds(roleIds []string) ([]model.Role, error)
	ListRoles(pageNum, pageSize int) ([]model.Role, int64, error)
	UpdateRoleById(id uint64, updates map[string]any) error
	UpdateRoleByRoleId(roleId string, updates map[string]any) error
	DeleteRole(id uint64) error
	DeleteRoleByRoleId(roleId string) error
}

type RoleRepo struct {
	database.IDatabase
}

func NewRoleRepo(db database.IDatabase) IRoleRepository {
	return &RoleRepo{
		IDatabase: db,
	}
}

// GetRole 获取角色
func (r *RoleRepo) GetRole(roleId string) (*model.Role, error) {
	var role model.Role
	err := r.Database().Select("id", "role_id", "name", "display_name", "description", "is_enabled", "created_at", "updated_at").
		Where("role_id = ? AND is_enabled = ?", roleId, 1).First(&role).Error
	if err != nil {
		return nil, err
	}
	return &role, nil
}

// GetRolesByRoleIds 根据角色ID列表获取角色
func (r *RoleRepo) GetRolesByRoleIds(roleIds []string) ([]model.Role, error) {
	if len(roleIds) == 0 {
		return []model.Role{}, nil
	}
	var roles []model.Role
	err := r.Database().Select("id", "role_id", "name", "display_name", "description", "is_enabled", "created_at", "updated_at").
		Where("role_id IN ? AND is_enabled = ?", roleIds, 1).Find(&roles).Error
	return roles, err
}

// CreateRole 创建角色
func (r *RoleRepo) CreateRole(role *model.Role) error {
	if err := r.Database().Table(role.TableName()).Create(role).Error; err != nil {
		return err
	}
	return nil
}

// GetRoleById 根据ID获取角色
func (r *RoleRepo) GetRoleById(id uint64) (*model.Role, error) {
	var role model.Role
	err := r.Database().Select("id", "role_id", "name", "display_name", "description", "is_enabled", "created_at", "updated_at").
		Where("id = ?", id).First(&role).Error
	if err != nil {
		return nil, err
	}
	return &role, nil
}

// ListRoles 列出角色（支持分页）
func (r *RoleRepo) ListRoles(pageNum, pageSize int) ([]model.Role, int64, error) {
	var roles []model.Role
	var role model.Role
	var count int64
	offset := (pageNum - 1) * pageSize

	if err := r.Database().Table(role.TableName()).Count(&count).Error; err != nil {
		return nil, 0, err
	}

	if err := r.Database().Select("id", "role_id", "name", "display_name", "description", "is_enabled", "created_at", "updated_at").
		Table(role.TableName()).
		Offset(offset).Limit(pageSize).
		Order("created_at DESC").
		Find(&roles).Error; err != nil {
		return nil, 0, err
	}
	return roles, count, nil
}

// UpdateRoleById 根据ID更新角色
func (r *RoleRepo) UpdateRoleById(id uint64, updates map[string]any) error {
	var role model.Role
	if err := r.Database().Table(role.TableName()).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}
	return nil
}

// UpdateRoleByRoleId 根据RoleId更新角色
func (r *RoleRepo) UpdateRoleByRoleId(roleId string, updates map[string]any) error {
	var role model.Role
	if err := r.Database().Table(role.TableName()).Where("role_id = ?", roleId).Updates(updates).Error; err != nil {
		return err
	}
	return nil
}

// DeleteRole 根据ID删除角色（软删除，设置is_enabled=0）
func (r *RoleRepo) DeleteRole(id uint64) error {
	var role model.Role
	updates := map[string]any{
		"is_enabled": 0,
	}
	if err := r.Database().Table(role.TableName()).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}
	return nil
}

// DeleteRoleByRoleId 根据RoleId删除角色（软删除，设置is_enabled=0）
func (r *RoleRepo) DeleteRoleByRoleId(roleId string) error {
	var role model.Role
	updates := map[string]any{
		"is_enabled": 0,
	}
	if err := r.Database().Table(role.TableName()).Where("role_id = ?", roleId).Updates(updates).Error; err != nil {
		return err
	}
	return nil
}
