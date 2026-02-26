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
	"time"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/database"
)

// IUserExtRepository defines user ext persistence with context support.
type IUserExtRepository interface {
	Get(ctx context.Context, userId string) (*model.UserExt, error)
	Create(ctx context.Context, ext *model.UserExt) error
	Update(ctx context.Context, userId string, ext *model.UserExt) error
	UpdateLastLogin(ctx context.Context, userId string) error
	UpdateTimezone(ctx context.Context, userId, timezone string) error
	UpdateInvitationStatus(ctx context.Context, userId, status string) error
	Delete(ctx context.Context, userId string) error
	Exists(ctx context.Context, userId string) (bool, error)
}

type UserExtRepo struct {
	database.IDatabase
}

func NewUserExtRepo(db database.IDatabase) IUserExtRepository {
	return &UserExtRepo{
		IDatabase: db,
	}
}

// Get returns user ext by userId.
func (uer *UserExtRepo) Get(ctx context.Context, userId string) (*model.UserExt, error) {
	var ext model.UserExt
	err := uer.Database().WithContext(ctx).Table(ext.TableName()).
		Select("id", "user_id", "timezone", "last_login_at", "invitation_status", "invited_by", "invited_at", "accepted_at", "created_at", "updated_at").
		Where("user_id = ?", userId).
		First(&ext).Error
	return &ext, err
}

// Create creates a user ext record.
func (uer *UserExtRepo) Create(ctx context.Context, ext *model.UserExt) error {
	return uer.Database().WithContext(ctx).Table(ext.TableName()).Create(ext).Error
}

// Update updates user ext information.
func (uer *UserExtRepo) Update(ctx context.Context, userId string, ext *model.UserExt) error {
	return uer.Database().WithContext(ctx).Table(ext.TableName()).
		Where("user_id = ?", userId).
		Updates(ext).Error
}

// UpdateLastLogin updates the last login timestamp.
func (uer *UserExtRepo) UpdateLastLogin(ctx context.Context, userId string) error {
	now := time.Now()
	var ext model.UserExt
	return uer.Database().WithContext(ctx).Table(ext.TableName()).
		Where("user_id = ?", userId).
		Update("last_login_at", now).Error
}

// UpdateTimezone updates user timezone.
func (uer *UserExtRepo) UpdateTimezone(ctx context.Context, userId, timezone string) error {
	var ext model.UserExt
	return uer.Database().WithContext(ctx).Table(ext.TableName()).
		Where("user_id = ?", userId).
		Update("timezone", timezone).Error
}

// UpdateInvitationStatus updates invitation status.
func (uer *UserExtRepo) UpdateInvitationStatus(ctx context.Context, userId, status string) error {
	updates := map[string]interface{}{
		"invitation_status": status,
	}
	if status == model.UserInvitationStatusAccepted {
		updates["accepted_at"] = time.Now()
	}
	var ext model.UserExt
	return uer.Database().WithContext(ctx).Table(ext.TableName()).
		Where("user_id = ?", userId).
		Updates(updates).Error
}

// Delete deletes user ext record.
func (uer *UserExtRepo) Delete(ctx context.Context, userId string) error {
	var ext model.UserExt
	return uer.Database().WithContext(ctx).Table(ext.TableName()).
		Where("user_id = ?", userId).
		Delete(&model.UserExt{}).Error
}

// Exists checks if user ext exists.
func (uer *UserExtRepo) Exists(ctx context.Context, userId string) (bool, error) {
	var count int64
	var ext model.UserExt
	err := uer.Database().WithContext(ctx).Table(ext.TableName()).
		Where("user_id = ?", userId).
		Count(&count).Error
	return count > 0, err
}
