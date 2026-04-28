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
)

// IRegistrationTokenRepository defines persistence for registration tokens.
type IRegistrationTokenRepository interface {
	Create(ctx context.Context, token *model.RegistrationToken) error
	List(ctx context.Context, page, size int) ([]model.RegistrationToken, int64, error)
	GetAllActive(ctx context.Context) ([]model.RegistrationToken, error)
	GetByID(ctx context.Context, id uint64) (*model.RegistrationToken, error)
	IncrementUseCount(ctx context.Context, id uint64) error
	Deactivate(ctx context.Context, id uint64) error
}

type RegistrationTokenRepo struct {
	database.IDatabase
}

// NewRegistrationTokenRepo creates a new registration token repository.
func NewRegistrationTokenRepo(db database.IDatabase) IRegistrationTokenRepository {
	return &RegistrationTokenRepo{IDatabase: db}
}

func (r *RegistrationTokenRepo) Create(ctx context.Context, token *model.RegistrationToken) error {
	return r.Database().WithContext(ctx).Create(token).Error
}

func (r *RegistrationTokenRepo) List(ctx context.Context, page, size int) ([]model.RegistrationToken, int64, error) {
	var tokens []model.RegistrationToken
	var count int64
	var t model.RegistrationToken

	if err := r.Database().WithContext(ctx).Model(&t).Count(&count).Error; err != nil {
		return nil, 0, err
	}
	offset := (page - 1) * size
	if err := r.Database().WithContext(ctx).Model(&t).
		Order("created_at DESC").Offset(offset).Limit(size).Find(&tokens).Error; err != nil {
		return nil, 0, err
	}
	return tokens, count, nil
}

func (r *RegistrationTokenRepo) GetAllActive(ctx context.Context) ([]model.RegistrationToken, error) {
	var tokens []model.RegistrationToken
	if err := r.Database().WithContext(ctx).
		Where("is_active = 1").Find(&tokens).Error; err != nil {
		return nil, err
	}
	return tokens, nil
}

func (r *RegistrationTokenRepo) GetByID(ctx context.Context, id uint64) (*model.RegistrationToken, error) {
	var token model.RegistrationToken
	if err := r.Database().WithContext(ctx).First(&token, id).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

func (r *RegistrationTokenRepo) IncrementUseCount(ctx context.Context, id uint64) error {
	return r.Database().WithContext(ctx).Model(&model.RegistrationToken{}).
		Where("id = ?", id).UpdateColumn("use_count", r.Database().Raw("use_count + 1")).Error
}

func (r *RegistrationTokenRepo) Deactivate(ctx context.Context, id uint64) error {
	return r.Database().WithContext(ctx).Model(&model.RegistrationToken{}).
		Where("id = ?", id).Update("is_active", 0).Error
}
