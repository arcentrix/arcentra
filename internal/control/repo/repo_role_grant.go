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

// IRoleGrantRepository 定义角色授权仓储接口
type IRoleGrantRepository interface {
	BatchUpsert(ctx context.Context, grants []model.RoleGrant) error
	Create(ctx context.Context, grant *model.RoleGrant) error
	ListAllEnabled(ctx context.Context) ([]model.RoleGrant, error)
	ListBySubject(ctx context.Context, subjectType, subjectID string) ([]model.RoleGrant, error)
	ListBySubjects(ctx context.Context, subjectType string, subjectIDs []string) ([]model.RoleGrant, error)
	ListByScope(ctx context.Context, scopeType, scopeID string) ([]model.RoleGrant, error)
	DisableGrant(ctx context.Context, grantID string) error
	DisableBySubject(ctx context.Context, subjectType, subjectID string) error
}

// RoleGrantRepo 表示角色授权仓储实现
type RoleGrantRepo struct {
	database.IDatabase
}

// NewRoleGrantRepo 创建角色授权仓储
func NewRoleGrantRepo(db database.IDatabase) IRoleGrantRepository {
	return &RoleGrantRepo{IDatabase: db}
}

// BatchUpsert 批量写入角色授权
func (r *RoleGrantRepo) BatchUpsert(ctx context.Context, grants []model.RoleGrant) error {
	if len(grants) == 0 {
		return nil
	}
	return r.Database().WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "subject_type"},
			{Name: "subject_id"},
			{Name: "role_id"},
			{Name: "scope_type"},
			{Name: "scope_id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"granted_by", "expires_at", "is_enabled", "updated_at"}),
	}).Create(&grants).Error
}

// Create 创建角色授权
func (r *RoleGrantRepo) Create(ctx context.Context, grant *model.RoleGrant) error {
	return r.Database().WithContext(ctx).Create(grant).Error
}

// ListAllEnabled 返回全部有效授权
func (r *RoleGrantRepo) ListAllEnabled(ctx context.Context) ([]model.RoleGrant, error) {
	var grants []model.RoleGrant
	err := r.Database().WithContext(ctx).
		Where("is_enabled = ?", 1).
		Find(&grants).Error
	return grants, err
}

// ListBySubject 返回指定主体的有效授权
func (r *RoleGrantRepo) ListBySubject(ctx context.Context, subjectType, subjectID string) ([]model.RoleGrant, error) {
	var grants []model.RoleGrant
	err := r.Database().WithContext(ctx).
		Where("subject_type = ? AND subject_id = ? AND is_enabled = ?", subjectType, subjectID, 1).
		Find(&grants).Error
	return grants, err
}

// ListBySubjects 批量查询主体授权
func (r *RoleGrantRepo) ListBySubjects(ctx context.Context, subjectType string, subjectIDs []string) ([]model.RoleGrant, error) {
	if len(subjectIDs) == 0 {
		return []model.RoleGrant{}, nil
	}
	var grants []model.RoleGrant
	err := r.Database().WithContext(ctx).
		Where("subject_type = ? AND subject_id IN ? AND is_enabled = ?", subjectType, subjectIDs, 1).
		Find(&grants).Error
	return grants, err
}

// ListByScope 返回指定作用域授权
func (r *RoleGrantRepo) ListByScope(ctx context.Context, scopeType, scopeID string) ([]model.RoleGrant, error) {
	var grants []model.RoleGrant
	err := r.Database().WithContext(ctx).
		Where("scope_type = ? AND scope_id = ? AND is_enabled = ?", scopeType, scopeID, 1).
		Find(&grants).Error
	return grants, err
}

// DisableGrant 按 grantID 撤销授权
func (r *RoleGrantRepo) DisableGrant(ctx context.Context, grantID string) error {
	return r.Database().WithContext(ctx).Model(&model.RoleGrant{}).
		Where("grant_id = ?", grantID).
		Update("is_enabled", 0).Error
}

// DisableBySubject 按主体撤销全部授权
func (r *RoleGrantRepo) DisableBySubject(ctx context.Context, subjectType, subjectID string) error {
	return r.Database().WithContext(ctx).Model(&model.RoleGrant{}).
		Where("subject_type = ? AND subject_id = ?", subjectType, subjectID).
		Update("is_enabled", 0).Error
}
