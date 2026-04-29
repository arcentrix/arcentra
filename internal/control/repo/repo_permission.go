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
	"gorm.io/gorm/clause"
)

// IPermissionRepository 定义权限字典与角色权限绑定仓储接口
type IPermissionRepository interface {
	BatchUpsertPermissions(ctx context.Context, permissions []model.Permission) error
	BatchUpsertRoleBindings(ctx context.Context, bindings []model.RolePermissionBinding) error
	ListPermissions(ctx context.Context) ([]model.Permission, error)
	GetByPermissionID(ctx context.Context, permissionID string) (*model.Permission, error)
	GetByPermissionIDs(ctx context.Context, permissionIDs []string) ([]model.Permission, error)
	CreatePermission(ctx context.Context, permission *model.Permission) error
	UpdatePermission(ctx context.Context, permissionID string, updates map[string]any) error
	DeletePermission(ctx context.Context, permissionID string) error
	ListRoleBindings(ctx context.Context, roleIDs []string) ([]model.RolePermissionBinding, error)
	ListAllRoleBindings(ctx context.Context) ([]model.RolePermissionBinding, error)
	DeleteRoleBindingsByRole(ctx context.Context, roleID string) error
	DeleteRoleBindingsByPermission(ctx context.Context, permissionID string) error
}

// PermissionRepo 表示权限仓储实现
type PermissionRepo struct {
	database.IDatabase
}

// NewPermissionRepo 创建权限仓储
func NewPermissionRepo(db database.IDatabase) IPermissionRepository {
	return &PermissionRepo{IDatabase: db}
}

// BatchUpsertPermissions 批量写入权限字典
func (r *PermissionRepo) BatchUpsertPermissions(ctx context.Context, permissions []model.Permission) error {
	if len(permissions) == 0 {
		return nil
	}
	return r.Database().WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "permission_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"resource_type", "action", "scope_type", "description", "is_system", "updated_at"}),
	}).Create(&permissions).Error
}

// BatchUpsertRoleBindings 批量写入角色权限绑定
func (r *PermissionRepo) BatchUpsertRoleBindings(ctx context.Context, bindings []model.RolePermissionBinding) error {
	if len(bindings) == 0 {
		return nil
	}
	return r.Database().WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "role_id"}, {Name: "permission_id"}},
		DoNothing: true,
	}).Create(&bindings).Error
}

// ListPermissions 返回所有权限字典
func (r *PermissionRepo) ListPermissions(ctx context.Context) ([]model.Permission, error) {
	var permissions []model.Permission
	err := r.Database().WithContext(ctx).Order("permission_id ASC").Find(&permissions).Error
	return permissions, err
}

// GetByPermissionID 按 permissionID 查询单条
func (r *PermissionRepo) GetByPermissionID(ctx context.Context, permissionID string) (*model.Permission, error) {
	var permission model.Permission
	err := r.Database().WithContext(ctx).
		Where("permission_id = ?", permissionID).
		First(&permission).Error
	if err != nil {
		return nil, err
	}
	return &permission, nil
}

// GetByPermissionIDs 按 permissionID 批量查询
func (r *PermissionRepo) GetByPermissionIDs(ctx context.Context, permissionIDs []string) ([]model.Permission, error) {
	if len(permissionIDs) == 0 {
		return []model.Permission{}, nil
	}
	var permissions []model.Permission
	err := r.Database().WithContext(ctx).
		Where("permission_id IN ?", permissionIDs).
		Find(&permissions).Error
	return permissions, err
}

// CreatePermission 创建权限
func (r *PermissionRepo) CreatePermission(ctx context.Context, permission *model.Permission) error {
	return r.Database().WithContext(ctx).Create(permission).Error
}

// UpdatePermission 更新权限
func (r *PermissionRepo) UpdatePermission(ctx context.Context, permissionID string, updates map[string]any) error {
	return r.Database().WithContext(ctx).Model(&model.Permission{}).
		Where("permission_id = ?", permissionID).
		Updates(updates).Error
}

// DeletePermission 删除权限
func (r *PermissionRepo) DeletePermission(ctx context.Context, permissionID string) error {
	return r.Database().WithContext(ctx).
		Where("permission_id = ?", permissionID).
		Delete(&model.Permission{}).Error
}

// ListRoleBindings 按角色列表查询绑定关系
func (r *PermissionRepo) ListRoleBindings(ctx context.Context, roleIDs []string) ([]model.RolePermissionBinding, error) {
	if len(roleIDs) == 0 {
		return []model.RolePermissionBinding{}, nil
	}
	var bindings []model.RolePermissionBinding
	err := r.Database().WithContext(ctx).
		Where("role_id IN ?", roleIDs).
		Find(&bindings).Error
	return bindings, err
}

// ListAllRoleBindings 返回全部角色权限绑定
func (r *PermissionRepo) ListAllRoleBindings(ctx context.Context) ([]model.RolePermissionBinding, error) {
	var bindings []model.RolePermissionBinding
	err := r.Database().WithContext(ctx).Find(&bindings).Error
	return bindings, err
}

// DeleteRoleBindingsByRole 删除角色的所有权限绑定
func (r *PermissionRepo) DeleteRoleBindingsByRole(ctx context.Context, roleID string) error {
	return r.Database().WithContext(ctx).
		Where("role_id = ?", roleID).
		Delete(&model.RolePermissionBinding{}).Error
}

// DeleteRoleBindingsByPermission 按权限删除全部角色绑定
func (r *PermissionRepo) DeleteRoleBindingsByPermission(ctx context.Context, permissionID string) error {
	return r.Database().WithContext(ctx).
		Where("permission_id = ?", permissionID).
		Delete(&model.RolePermissionBinding{}).Error
}
