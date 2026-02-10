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
	"time"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/database"
)

type IUserExtRepository interface {
	GetByUserId(userId string) (*model.UserExt, error)
	Create(ext *model.UserExt) error
	Update(userId string, ext *model.UserExt) error
	UpdateLastLogin(userId string) error
	UpdateTimezone(userId, timezone string) error
	UpdateInvitationStatus(userId, status string) error
	Delete(userId string) error
	Exists(userId string) (bool, error)
}

type UserExtRepo struct {
	database.IDatabase
}

func NewUserExtRepo(db database.IDatabase) IUserExtRepository {
	return &UserExtRepo{
		IDatabase: db,
	}
}

// GetByUserId gets user ext by user ID
func (uer *UserExtRepo) GetByUserId(userId string) (*model.UserExt, error) {
	var ext model.UserExt
	err := uer.Database().Table(ext.TableName()).
		Select("id", "user_id", "timezone", "last_login_at", "invitation_status", "invited_by", "invited_at", "accepted_at", "created_at", "updated_at").
		Where("user_id = ?", userId).
		First(&ext).Error
	return &ext, err
}

// Create creates a user ext record
func (uer *UserExtRepo) Create(ext *model.UserExt) error {
	return uer.Database().Table(ext.TableName()).Create(ext).Error
}

// Update updates user ext information
func (uer *UserExtRepo) Update(userId string, ext *model.UserExt) error {
	return uer.Database().Table(ext.TableName()).
		Where("user_id = ?", userId).
		Updates(ext).Error
}

// UpdateLastLogin updates the last login timestamp
func (uer *UserExtRepo) UpdateLastLogin(userId string) error {
	now := time.Now()
	var ext model.UserExt
	return uer.Database().Table(ext.TableName()).
		Where("user_id = ?", userId).
		Update("last_login_at", now).Error
}

// UpdateTimezone updates user timezone
func (uer *UserExtRepo) UpdateTimezone(userId, timezone string) error {
	var ext model.UserExt
	return uer.Database().Table(ext.TableName()).
		Where("user_id = ?", userId).
		Update("timezone", timezone).Error
}

// UpdateInvitationStatus updates invitation status
func (uer *UserExtRepo) UpdateInvitationStatus(userId, status string) error {
	updates := map[string]interface{}{
		"invitation_status": status,
	}

	// if status is accepted, set accepted_at timestamp
	if status == model.UserInvitationStatusAccepted {
		updates["accepted_at"] = time.Now()
	}

	var ext model.UserExt
	return uer.Database().Table(ext.TableName()).
		Where("user_id = ?", userId).
		Updates(updates).Error
}

// Delete deletes user ext record
func (uer *UserExtRepo) Delete(userId string) error {
	var ext model.UserExt
	return uer.Database().Table(ext.TableName()).
		Where("user_id = ?", userId).
		Delete(&model.UserExt{}).Error
}

// Exists checks if user ext exists
func (uer *UserExtRepo) Exists(userId string) (bool, error) {
	var count int64
	var ext model.UserExt
	err := uer.Database().Table(ext.TableName()).
		Where("user_id = ?", userId).
		Count(&count).Error
	return count > 0, err
}
