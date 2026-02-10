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
	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/database"
)

type ISecretRepository interface {
	CreateSecret(secret *model.Secret) error
	UpdateSecret(secret *model.Secret) error
	GetSecretByID(secretId string) (*model.Secret, error)
	GetSecretList(pageNum, pageSize int, secretType, scope, scopeId, createdBy string) ([]*model.Secret, int64, error)
	DeleteSecret(secretId string) error
	GetSecretsByScope(scope, scopeId string) ([]*model.Secret, error)
	GetSecretValue(secretId string) (string, error)
}

type SecretRepo struct {
	database.IDatabase
}

func NewSecretRepo(db database.IDatabase) ISecretRepository {
	return &SecretRepo{
		IDatabase: db,
	}
}

// CreateSecret creates a new secret
func (sr *SecretRepo) CreateSecret(secret *model.Secret) error {
	return sr.Database().Table(secret.TableName()).Create(secret).Error
}

// UpdateSecret updates a secret by secret_id
func (sr *SecretRepo) UpdateSecret(secret *model.Secret) error {
	return sr.Database().Table(secret.TableName()).
		Omit("id", "secret_id", "created_at").
		Where("secret_id = ?", secret.SecretId).
		Updates(secret).Error
}

// GetSecretByID gets a secret by secret_id
func (sr *SecretRepo) GetSecretByID(secretId string) (*model.Secret, error) {
	var secret model.Secret
	err := sr.Database().Table(secret.TableName()).
		Select("id", "secret_id", "name", "secret_type", "secret_value", "description", "scope", "scope_id", "created_by", "created_at", "updated_at").
		Where("secret_id = ?", secretId).
		First(&secret).Error
	return &secret, err
}

// GetSecretList gets secret list with pagination and filters
func (sr *SecretRepo) GetSecretList(pageNum, pageSize int, secretType, scope, scopeId, createdBy string) ([]*model.Secret, int64, error) {
	var secrets []*model.Secret
	var secret model.Secret
	var total int64

	query := sr.Database().Table(secret.TableName())

	// apply filters
	if secretType != "" {
		query = query.Where("secret_type = ?", secretType)
	}
	if scope != "" {
		query = query.Where("scope = ?", scope)
	}
	if scopeId != "" {
		query = query.Where("scope_id = ?", scopeId)
	}
	if createdBy != "" {
		query = query.Where("created_by = ?", createdBy)
	}

	// get total count
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// get paginated list (don't select secret_value, created_at and updated_at for list view)
	offset := (pageNum - 1) * pageSize
	err := query.Select("id", "secret_id", "name", "secret_type", "description", "scope", "scope_id", "created_by").
		Order("id DESC").
		Limit(pageSize).
		Offset(offset).
		Find(&secrets).Error

	return secrets, total, err
}

// DeleteSecret deletes a secret by secret_id
func (sr *SecretRepo) DeleteSecret(secretId string) error {
	var secret model.Secret
	return sr.Database().Table(secret.TableName()).
		Where("secret_id = ?", secretId).
		Delete(&model.Secret{}).Error
}

// GetSecretsByScope gets secrets by scope and scope_id
func (sr *SecretRepo) GetSecretsByScope(scope, scopeId string) ([]*model.Secret, error) {
	var secrets []*model.Secret
	var secret model.Secret
	err := sr.Database().Table(secret.TableName()).
		Select("id", "secret_id", "name", "secret_type", "description", "scope", "scope_id", "created_by").
		Where("scope = ? AND scope_id = ?", scope, scopeId).
		Find(&secrets).Error
	return secrets, err
}

// GetSecretValue gets the secret value (use with caution, only when needed)
func (sr *SecretRepo) GetSecretValue(secretId string) (string, error) {
	var secret model.Secret
	err := sr.Database().Table(secret.TableName()).
		Select("secret_value").
		Where("secret_id = ?", secretId).
		First(&secret).Error
	return secret.SecretValue, err
}
