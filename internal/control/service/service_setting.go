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

package service

import (
	"context"
	"errors"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/log"
	"gorm.io/gorm"
)

// SettingService provides workspace-scoped setting operations.
type SettingService struct {
	settingRepo repo.ISettingRepository
}

// NewSettingService creates a new SettingService.
func NewSettingService(settingRepo repo.ISettingRepository) *SettingService {
	return &SettingService{
		settingRepo: settingRepo,
	}
}

// GetSetting returns a single setting by name.
func (ss *SettingService) GetSetting(ctx context.Context, name string) (*model.Setting, error) {
	setting, err := ss.settingRepo.Get(ctx, name)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("setting not found")
		}
		log.Errorw("failed to get setting", "name", name, "error", err)
		return nil, errors.New("failed to get setting")
	}
	return setting, nil
}

// UpsertSetting creates or updates a setting.
func (ss *SettingService) UpsertSetting(ctx context.Context, setting *model.Setting) error {
	if setting.Name == "" {
		return errors.New("name is required")
	}

	if err := ss.settingRepo.Upsert(ctx, setting); err != nil {
		log.Errorw("failed to upsert setting", "name", setting.Name, "error", err)
		return errors.New("failed to upsert setting")
	}

	return nil
}

// ListSettings returns every setting in the table, ordered by name.
func (ss *SettingService) ListSettings(ctx context.Context) ([]*model.Setting, error) {
	settings, err := ss.settingRepo.ListAll(ctx)
	if err != nil {
		log.Errorw("failed to list settings", "error", err)
		return nil, errors.New("failed to list settings")
	}
	return settings, nil
}

// DeleteSetting removes a setting by name.
func (ss *SettingService) DeleteSetting(ctx context.Context, name string) error {
	if err := ss.settingRepo.Delete(ctx, name); err != nil {
		log.Errorw("failed to delete setting", "name", name, "error", err)
		return errors.New("failed to delete setting")
	}

	return nil
}
