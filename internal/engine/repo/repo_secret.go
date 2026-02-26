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

// ISecretRepository defines secret persistence with context support.
type ISecretRepository interface {
	Create(ctx context.Context, secret *model.Secret) error
	Update(ctx context.Context, secret *model.Secret) error
	Get(ctx context.Context, secretId string) (*model.Secret, error)
	List(ctx context.Context, pageNum, pageSize int, secretType, scope, scopeId, createdBy string) ([]*model.Secret, int64, error)
	Delete(ctx context.Context, secretId string) error
	ListByScope(ctx context.Context, scope, scopeId string) ([]*model.Secret, error)
	GetValue(ctx context.Context, secretId string) (string, error)
}

type SecretRepo struct {
	database.IDatabase
}

// NewSecretRepo creates a secret repository.
func NewSecretRepo(db database.IDatabase) ISecretRepository {
	return &SecretRepo{IDatabase: db}
}

// Create creates a new secret.
func (sr *SecretRepo) Create(ctx context.Context, secret *model.Secret) error {
	return sr.Database().WithContext(ctx).Table(secret.TableName()).Create(secret).Error
}

// Update updates a secret by secretId.
func (sr *SecretRepo) Update(ctx context.Context, secret *model.Secret) error {
	return sr.Database().WithContext(ctx).Table(secret.TableName()).
		Omit("id", "secret_id", "created_at").
		Where("secret_id = ?", secret.SecretId).
		Updates(secret).Error
}

// Get returns secret by secretId.
func (sr *SecretRepo) Get(ctx context.Context, secretId string) (*model.Secret, error) {
	var secret model.Secret
	err := sr.Database().WithContext(ctx).Table(secret.TableName()).
		Select("id", "secret_id", "name", "secret_type", "secret_value", "description", "scope", "scope_id", "created_by", "created_at", "updated_at").
		Where("secret_id = ?", secretId).
		First(&secret).Error
	return &secret, err
}

// List lists secrets with pagination and filters.
func (sr *SecretRepo) List(ctx context.Context, pageNum, pageSize int, secretType, scope, scopeId, createdBy string) ([]*model.Secret, int64, error) {
	var secrets []*model.Secret
	var secret model.Secret
	var total int64

	query := sr.Database().WithContext(ctx).Table(secret.TableName())

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

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (pageNum - 1) * pageSize
	err := query.Select("id", "secret_id", "name", "secret_type", "description", "scope", "scope_id", "created_by").
		Order("id DESC").
		Limit(pageSize).
		Offset(offset).
		Find(&secrets).Error

	return secrets, total, err
}

// Delete deletes secret by secretId.
func (sr *SecretRepo) Delete(ctx context.Context, secretId string) error {
	var secret model.Secret
	return sr.Database().WithContext(ctx).Table(secret.TableName()).
		Where("secret_id = ?", secretId).
		Delete(&model.Secret{}).Error
}

// ListByScope lists secrets by scope and scopeId.
func (sr *SecretRepo) ListByScope(ctx context.Context, scope, scopeId string) ([]*model.Secret, error) {
	var secrets []*model.Secret
	var secret model.Secret
	err := sr.Database().WithContext(ctx).Table(secret.TableName()).
		Select("id", "secret_id", "name", "secret_type", "description", "scope", "scope_id", "created_by").
		Where("scope = ? AND scope_id = ?", scope, scopeId).
		Find(&secrets).Error
	return secrets, err
}

// GetValue returns secret value by secretId.
func (sr *SecretRepo) GetValue(ctx context.Context, secretId string) (string, error) {
	var secret model.Secret
	err := sr.Database().WithContext(ctx).Table(secret.TableName()).
		Select("secret_value").
		Where("secret_id = ?", secretId).
		First(&secret).Error
	return secret.SecretValue, err
}
