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

type IIdentityRepository interface {
	GetProvider(name string) (*model.Identity, error)
	GetProviderByType(providerType string) ([]model.Identity, error)
	GetProviderList() ([]model.Identity, error)
	GetProviderTypeList() ([]string, error)
	CreateProvider(provider *model.Identity) error
	UpdateProvider(name string, provider *model.Identity) error
	DeleteProvider(name string) error
	ProviderExists(name string) (bool, error)
	ToggleProvider(name string) error
}

type IdentityRepo struct {
	database.IDatabase
}

func NewIdentityRepo(db database.IDatabase) IIdentityRepository {
	return &IdentityRepo{
		IDatabase: db,
	}
}

func (ii *IdentityRepo) GetProvider(name string) (*model.Identity, error) {
	var identity model.Identity
	if err := ii.Database().Table(identity.TableName()).
		Where("name = ?", name).
		Select("provider_id, provider_type, name, description, config, priority, is_enabled").
		First(&identity).Error; err != nil {
		return nil, err
	}
	return &identity, nil
}

func (ii *IdentityRepo) GetProviderByType(providerType string) ([]model.Identity, error) {
	var identitys []model.Identity
	var identity model.Identity
	if err := ii.Database().Table(identity.TableName()).
		Where("provider_type = ?", providerType).
		Order("priority ASC").
		Select("provider_id, provider_type, name, description, priority, is_enabled").
		Find(&identitys).Error; err != nil {
		return nil, err
	}
	return identitys, nil
}

func (ii *IdentityRepo) GetProviderList() ([]model.Identity, error) {
	var identitys []model.Identity
	var identity model.Identity
	if err := ii.Database().Table(identity.TableName()).
		Order("priority ASC").
		Select("provider_id, provider_type, name, description, priority, is_enabled").
		Find(&identitys).Error; err != nil {
		return nil, err
	}
	return identitys, nil
}

func (ii *IdentityRepo) GetProviderTypeList() ([]string, error) {
	var providerTypes []string
	var identity model.Identity
	if err := ii.Database().Table(identity.TableName()).
		Distinct("provider_type").
		Select("provider_type").
		Pluck("provider_type", &providerTypes).Error; err != nil {
		return nil, err
	}
	return providerTypes, nil
}

// CreateProvider creates an identity provider
func (ii *IdentityRepo) CreateProvider(provider *model.Identity) error {
	return ii.Database().Table(provider.TableName()).Create(provider).Error
}

// UpdateProvider updates an identity provider (name and provider_type fields cannot be updated)
func (ii *IdentityRepo) UpdateProvider(name string, identity *model.Identity) error {
	return ii.Database().Table(identity.TableName()).
		Where("name = ?", name).
		Omit("name", "provider_id", "provider_type", "created_at").
		Updates(identity).Error
}

// DeleteProvider deletes an identity provider
func (ii *IdentityRepo) DeleteProvider(name string) error {
	var identity model.Identity
	return ii.Database().Table(identity.TableName()).
		Where("name = ?", name).
		Delete(&model.Identity{}).Error
}

// ProviderExists checks if a provider exists
func (ii *IdentityRepo) ProviderExists(name string) (bool, error) {
	var count int64
	var identity model.Identity
	err := ii.Database().Table(identity.TableName()).
		Where("name = ?", name).
		Count(&count).Error
	return count > 0, err
}

// ToggleProvider toggles the enabled status of an identity provider (enable/disable)
func (ii *IdentityRepo) ToggleProvider(name string) error {
	// query current status
	var identity model.Identity
	if err := ii.Database().Table(identity.TableName()).
		Where("name = ?", name).
		Select("is_enabled").
		First(&identity).Error; err != nil {
		return err
	}

	// toggle status: 0 -> 1, 1 -> 0
	newStatus := 1 - identity.IsEnabled

	// update status
	return ii.Database().Table(identity.TableName()).
		Where("name = ?", name).
		Update("is_enabled", newStatus).Error
}
