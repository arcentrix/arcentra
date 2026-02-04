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

package service

import (
	"errors"

	"github.com/arcentrix/arcade/internal/engine/model"
	generalrepo "github.com/arcentrix/arcade/internal/engine/repo"
	"github.com/arcentrix/arcade/pkg/log"
	"gorm.io/gorm"
)

type GeneralSettingsService struct {
	generalSettingsRepo generalrepo.IGeneralSettingsRepository
}

func NewGeneralSettingsService(generalSettingsRepo generalrepo.IGeneralSettingsRepository) *GeneralSettingsService {
	return &GeneralSettingsService{
		generalSettingsRepo: generalSettingsRepo,
	}
}

// UpdateGeneralSettings updates a general settings
func (gss *GeneralSettingsService) UpdateGeneralSettings(settingsId string, settings *model.GeneralSettings) error {
	// check if settings exists
	existing, err := gss.generalSettingsRepo.GetGeneralSettingsByID(settingsId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("general settings not found")
		}
		log.Errorw("failed to get general settings", "settingsId", settingsId, "error", err)
		return errors.New("failed to get general settings")
	}

	// prevent changing category and name
	settings.SettingsId = settingsId
	settings.Category = existing.Category
	settings.Name = existing.Name

	if err := gss.generalSettingsRepo.UpdateGeneralSettings(settings); err != nil {
		log.Errorw("failed to update general settings", "settingsId", settingsId, "error", err)
		return errors.New("failed to update general settings")
	}

	log.Infow("general settings updated successfully", "settingsId", settingsId)
	return nil
}

// GetGeneralSettingsByID gets a general settings by settings ID
func (gss *GeneralSettingsService) GetGeneralSettingsByID(settingsId string) (*model.GeneralSettings, error) {
	settings, err := gss.generalSettingsRepo.GetGeneralSettingsByID(settingsId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("general settings not found")
		}
		log.Errorw("failed to get general settings", "settingsId", settingsId, "error", err)
		return nil, errors.New("failed to get general settings")
	}
	return settings, nil
}

// GetGeneralSettingsByName gets a general settings by category and name
func (gss *GeneralSettingsService) GetGeneralSettingsByName(category, name string) (*model.GeneralSettings, error) {
	settings, err := gss.generalSettingsRepo.GetGeneralSettingsByName(category, name)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("general settings not found")
		}
		log.Errorw("failed to get general settings", "category", category, "name", name, "error", err)
		return nil, errors.New("failed to get general settings")
	}
	return settings, nil
}

// GetGeneralSettingsList gets general settings list with pagination and filters
func (gss *GeneralSettingsService) GetGeneralSettingsList(pageNum, pageSize int, category string) ([]*model.GeneralSettings, int64, error) {
	// set default pagination
	if pageNum <= 0 {
		pageNum = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}

	settingsList, total, err := gss.generalSettingsRepo.GetGeneralSettingsList(pageNum, pageSize, category)
	if err != nil {
		log.Errorw("failed to get general settings list", "category", category, "error", err)
		return nil, 0, errors.New("failed to get general settings list")
	}

	return settingsList, total, nil
}

// GetCategories gets all distinct categories
func (gss *GeneralSettingsService) GetCategories() ([]string, error) {
	categories, err := gss.generalSettingsRepo.GetCategories()
	if err != nil {
		log.Errorw("failed to get categories", "error", err)
		return nil, errors.New("failed to get categories")
	}
	return categories, nil
}
