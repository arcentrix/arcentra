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

// IUserRoleBindingRepository defines user role binding persistence with context support.
type IUserRoleBindingRepository interface {
	List(ctx context.Context, userId string) ([]model.UserRoleBinding, error)
	GetByRole(ctx context.Context, userId, roleId string) (*model.UserRoleBinding, error)
	Create(ctx context.Context, binding *model.UserRoleBinding) error
	Delete(ctx context.Context, bindingId string) error
	DeleteByUser(ctx context.Context, userId string) error
}

type UserRoleBindingRepo struct {
	database.IDatabase
}

func NewUserRoleBindingRepo(db database.IDatabase) IUserRoleBindingRepository {
	return &UserRoleBindingRepo{
		IDatabase: db,
	}
}

// List returns user role bindings by userId.
func (r *UserRoleBindingRepo) List(ctx context.Context, userId string) ([]model.UserRoleBinding, error) {
	var bindings []model.UserRoleBinding
	err := r.Database().WithContext(ctx).Select("binding_id", "user_id", "role_id", "granted_by", "create_time", "update_time").
		Where("user_id = ?", userId).Find(&bindings).Error
	return bindings, err
}

// GetByRole returns user role binding by userId and roleId.
func (r *UserRoleBindingRepo) GetByRole(ctx context.Context, userId, roleId string) (*model.UserRoleBinding, error) {
	var binding model.UserRoleBinding
	err := r.Database().WithContext(ctx).Select("binding_id", "user_id", "role_id", "granted_by", "create_time", "update_time").
		Where("user_id = ? AND role_id = ?", userId, roleId).First(&binding).Error
	if err != nil {
		return nil, err
	}
	return &binding, nil
}

// Create creates a user role binding.
func (r *UserRoleBindingRepo) Create(ctx context.Context, binding *model.UserRoleBinding) error {
	return r.Database().WithContext(ctx).Create(binding).Error
}

// Delete deletes user role binding by bindingId.
func (r *UserRoleBindingRepo) Delete(ctx context.Context, bindingId string) error {
	return r.Database().WithContext(ctx).Where("binding_id = ?", bindingId).Delete(&model.UserRoleBinding{}).Error
}

// DeleteByUser deletes all user role bindings by userId.
func (r *UserRoleBindingRepo) DeleteByUser(ctx context.Context, userId string) error {
	return r.Database().WithContext(ctx).Where("user_id = ?", userId).Delete(&model.UserRoleBinding{}).Error
}
