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

// IIdentityRepository defines identity provider persistence with context support.
type IIdentityRepository interface {
	GetProvider(ctx context.Context, name string) (*model.Identity, error)
	GetProviderByType(ctx context.Context, providerType string) ([]model.Identity, error)
	GetProviderList(ctx context.Context) ([]model.Identity, error)
	GetProviderTypeList(ctx context.Context) ([]string, error)
	CreateProvider(ctx context.Context, provider *model.Identity) error
	UpdateProvider(ctx context.Context, name string, provider *model.Identity) error
	DeleteProvider(ctx context.Context, name string) error
	ProviderExists(ctx context.Context, name string) (bool, error)
	ToggleProvider(ctx context.Context, name string) error
}

type IdentityRepo struct {
	database.IDatabase
}

func NewIdentityRepo(db database.IDatabase) IIdentityRepository {
	return &IdentityRepo{
		IDatabase: db,
	}
}

// GetProvider returns identity provider by name.
func (ii *IdentityRepo) GetProvider(ctx context.Context, name string) (*model.Identity, error) {
	var identity model.Identity
	if err := ii.Database().WithContext(ctx).Table(identity.TableName()).
		Where("name = ?", name).
		Select("provider_id, provider_type, name, description, config, priority, is_enabled").
		First(&identity).Error; err != nil {
		return nil, err
	}
	return &identity, nil
}

// GetProviderByType returns identity providers by type.
func (ii *IdentityRepo) GetProviderByType(ctx context.Context, providerType string) ([]model.Identity, error) {
	var identitys []model.Identity
	var identity model.Identity
	if err := ii.Database().WithContext(ctx).Table(identity.TableName()).
		Where("provider_type = ?", providerType).
		Order("priority ASC").
		Select("provider_id, provider_type, name, description, priority, is_enabled").
		Find(&identitys).Error; err != nil {
		return nil, err
	}
	return identitys, nil
}

// GetProviderList returns all identity providers.
func (ii *IdentityRepo) GetProviderList(ctx context.Context) ([]model.Identity, error) {
	var identitys []model.Identity
	var identity model.Identity
	if err := ii.Database().WithContext(ctx).Table(identity.TableName()).
		Order("priority ASC").
		Select("provider_id, provider_type, name, description, priority, is_enabled").
		Find(&identitys).Error; err != nil {
		return nil, err
	}
	return identitys, nil
}

// GetProviderTypeList returns distinct provider types.
func (ii *IdentityRepo) GetProviderTypeList(ctx context.Context) ([]string, error) {
	var providerTypes []string
	var identity model.Identity
	if err := ii.Database().WithContext(ctx).Table(identity.TableName()).
		Distinct("provider_type").
		Select("provider_type").
		Pluck("provider_type", &providerTypes).Error; err != nil {
		return nil, err
	}
	return providerTypes, nil
}

// CreateProvider creates an identity provider.
func (ii *IdentityRepo) CreateProvider(ctx context.Context, provider *model.Identity) error {
	return ii.Database().WithContext(ctx).Table(provider.TableName()).Create(provider).Error
}

// UpdateProvider updates an identity provider.
func (ii *IdentityRepo) UpdateProvider(ctx context.Context, name string, identity *model.Identity) error {
	return ii.Database().WithContext(ctx).Table(identity.TableName()).
		Where("name = ?", name).
		Omit("name", "provider_id", "provider_type", "created_at").
		Updates(identity).Error
}

// DeleteProvider deletes an identity provider.
func (ii *IdentityRepo) DeleteProvider(ctx context.Context, name string) error {
	var identity model.Identity
	return ii.Database().WithContext(ctx).Table(identity.TableName()).
		Where("name = ?", name).
		Delete(&model.Identity{}).Error
}

// ProviderExists checks if a provider exists.
func (ii *IdentityRepo) ProviderExists(ctx context.Context, name string) (bool, error) {
	var count int64
	var identity model.Identity
	err := ii.Database().WithContext(ctx).Table(identity.TableName()).
		Where("name = ?", name).
		Count(&count).Error
	return count > 0, err
}

// ToggleProvider toggles the enabled status of an identity provider.
func (ii *IdentityRepo) ToggleProvider(ctx context.Context, name string) error {
	var identity model.Identity
	if err := ii.Database().WithContext(ctx).Table(identity.TableName()).
		Where("name = ?", name).
		Select("is_enabled").
		First(&identity).Error; err != nil {
		return err
	}

	newStatus := 1 - identity.IsEnabled

	return ii.Database().WithContext(ctx).Table(identity.TableName()).
		Where("name = ?", name).
		Update("is_enabled", newStatus).Error
}
