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

type IUserRoleBindingRepository interface {
	GetUserRoleBindings(userId string) ([]model.UserRoleBinding, error)
	GetUserRoleBindingByRole(userId, roleId string) (*model.UserRoleBinding, error)
	CreateUserRoleBinding(binding *model.UserRoleBinding) error
	DeleteUserRoleBinding(bindingId string) error
	DeleteUserRoleBindingsByUser(userId string) error
}

type UserRoleBindingRepo struct {
	database.IDatabase
}

func NewUserRoleBindingRepo(db database.IDatabase) IUserRoleBindingRepository {
	return &UserRoleBindingRepo{
		IDatabase: db,
	}
}

// GetUserRoleBindings 获取用户的所有角色绑定
func (r *UserRoleBindingRepo) GetUserRoleBindings(userId string) ([]model.UserRoleBinding, error) {
	var bindings []model.UserRoleBinding
	err := r.Database().Select("binding_id", "user_id", "role_id", "granted_by", "create_time", "update_time").
		Where("user_id = ?", userId).Find(&bindings).Error
	return bindings, err
}

// GetUserRoleBindingByRole 获取用户指定角色的绑定
func (r *UserRoleBindingRepo) GetUserRoleBindingByRole(userId, roleId string) (*model.UserRoleBinding, error) {
	var binding model.UserRoleBinding
	err := r.Database().Select("binding_id", "user_id", "role_id", "granted_by", "create_time", "update_time").
		Where("user_id = ? AND role_id = ?", userId, roleId).First(&binding).Error
	if err != nil {
		return nil, err
	}
	return &binding, nil
}

// CreateUserRoleBinding 创建用户角色绑定
func (r *UserRoleBindingRepo) CreateUserRoleBinding(binding *model.UserRoleBinding) error {
	return r.Database().Create(binding).Error
}

// DeleteUserRoleBinding 删除用户角色绑定
func (r *UserRoleBindingRepo) DeleteUserRoleBinding(bindingId string) error {
	return r.Database().Where("binding_id = ?", bindingId).Delete(&model.UserRoleBinding{}).Error
}

// DeleteUserRoleBindingsByUser 删除用户的所有角色绑定
func (r *UserRoleBindingRepo) DeleteUserRoleBindingsByUser(userId string) error {
	return r.Database().Where("user_id = ?", userId).Delete(&model.UserRoleBinding{}).Error
}
