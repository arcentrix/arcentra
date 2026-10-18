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
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
)

// ISecretRepository defines secret persistence with context support.
type ISecretRepository interface {
	Create(ctx context.Context, secret *model.Secret) error
	Update(ctx context.Context, secret *model.Secret) error
	Get(ctx context.Context, secretID string) (*model.Secret, error)
	List(ctx context.Context, pageNum, pageSize int, secretType, scope, scopeID, createdBy string) ([]*model.Secret, int64, error)
	Delete(ctx context.Context, secretID string) error
	ListByScope(ctx context.Context, scope, scopeID string) ([]*model.Secret, error)
	GetValue(ctx context.Context, secretID string) (string, error)
}

type SecretRepo struct {
	database.IDatabase
}

var secretSelectFields = []string{
	"id",
	"secret_id",
	"name",
	"secret_type",
	"secret_value",
	"description",
	"scope",
	"scope_id",
	"created_by",
	"created_at",
	"updated_at",
}

// NewSecretRepo creates a secret repository.
func NewSecretRepo(db database.IDatabase) ISecretRepository {
	return &SecretRepo{IDatabase: db}
}

// Create creates a new secret.
func (sr *SecretRepo) Create(ctx context.Context, secret *model.Secret) error {
	if secret == nil || strings.EqualFold(strings.TrimSpace(secret.Scope), "project") {
		return ErrProjectSecretInvalid
	}
	return sr.Database().WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Table(secret.TableName()).Create(secret).Error
}

// Update updates a secret by secretId.
func (sr *SecretRepo) Update(ctx context.Context, secret *model.Secret) error {
	if secret == nil || strings.EqualFold(strings.TrimSpace(secret.Scope), "project") {
		return ErrProjectSecretInvalid
	}
	return sr.Database().WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Table(secret.TableName()).
		Omit("id", "secret_id", "scope", "scope_id", "version", "is_enabled", "created_by", "created_at").
		Where("secret_id = ? AND LOWER(TRIM(scope)) <> ?", secret.SecretID, "project").Updates(secret).Error
}

// Get returns secret by secretId.
func (sr *SecretRepo) Get(ctx context.Context, secretID string) (*model.Secret, error) {
	var rows []model.Secret
	err := sr.Database().WithContext(ctx).Clauses(database.Write()).Select(secretSelectFields).Where("secret_id = ? AND LOWER(TRIM(scope)) <> ?", secretID, "project").Limit(2).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 || rows[0].SecretID != secretID || strings.EqualFold(strings.TrimSpace(rows[0].Scope), "project") {
		return nil, gorm.ErrRecordNotFound
	}
	return &rows[0], nil
}

// List lists secrets with pagination and filters.
func (sr *SecretRepo) List(
	ctx context.Context,
	pageNum, pageSize int,
	secretType, scope, scopeID, createdBy string,
) ([]*model.Secret, int64, error) {
	return sr.list(ctx, pageNum, pageSize, secretType, scope, scopeID, createdBy, false)
}

// ListPlatform 仅返回旧平台端点可管理的非项目元数据
func (sr *SecretRepo) ListPlatform(ctx context.Context, pageNum, pageSize int, secretType, scope, scopeID, createdBy string) ([]*model.Secret, int64, error) {
	return sr.list(ctx, pageNum, pageSize, secretType, scope, scopeID, createdBy, true)
}

func (sr *SecretRepo) list(ctx context.Context, pageNum, pageSize int, secretType, scope, scopeID, createdBy string, platform bool) ([]*model.Secret, int64, error) {
	var secrets []*model.Secret
	var secret model.Secret
	var total int64

	query := sr.Database().WithContext(ctx).Clauses(database.Write()).Table(secret.TableName())
	if platform {
		query = query.Where("LOWER(TRIM(scope)) <> ?", "project")
	}

	if secretType != "" {
		query = query.Where("secret_type = ?", secretType)
	}
	if scope != "" {
		query = query.Where("scope = ?", scope)
	}
	if scopeID != "" {
		query = query.Where("scope_id = ?", scopeID)
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
func (sr *SecretRepo) Delete(ctx context.Context, secretID string) error {
	var secret model.Secret
	return sr.Database().WithContext(ctx).Table(secret.TableName()).
		Where("secret_id = ? AND LOWER(TRIM(scope)) <> ?", secretID, "project").
		Delete(&model.Secret{}).Error
}

// ListByScope lists secrets by scope and scopeId.
func (sr *SecretRepo) ListByScope(ctx context.Context, scope, scopeID string) ([]*model.Secret, error) {
	var secrets []*model.Secret
	var secret model.Secret
	err := sr.Database().WithContext(ctx).Table(secret.TableName()).
		Select("id", "secret_id", "name", "secret_type", "description", "scope", "scope_id", "created_by").
		Where("scope = ? AND scope_id = ? AND LOWER(TRIM(scope)) <> ?", scope, scopeID, "project").
		Find(&secrets).Error
	return secrets, err
}

// GetValue returns secret value by secretId.
func (sr *SecretRepo) GetValue(ctx context.Context, secretID string) (string, error) {
	secret, err := sr.Get(ctx, secretID)
	if err != nil {
		return "", err
	}
	return secret.SecretValue, nil
}
