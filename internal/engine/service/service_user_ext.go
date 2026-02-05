// Copyright 2025 Arcentra Team
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
	"fmt"
	"slices"
	"time"

	"github.com/arcentrix/arcentra/internal/engine/model"
	userrepo "github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/arcentrix/arcentra/pkg/log"
)

type UserExt struct {
	userExtRepo userrepo.IUserExtRepository
}

func NewUserExt(userExtRepo userrepo.IUserExtRepository) *UserExt {
	return &UserExt{
		userExtRepo: userExtRepo,
	}
}

// GetUserExt gets user Ext information
func (ues *UserExt) GetUserExt(userId string) (*model.UserExt, error) {
	Ext, err := ues.userExtRepo.GetByUserId(userId)
	if err != nil {
		log.Errorw("failed to get user Ext", "userId", userId, "error", err)
		return nil, err
	}
	return Ext, nil
}

// CreateUserExt creates user Ext record
func (ues *UserExt) CreateUserExt(Ext *model.UserExt) error {
	// check if already exists
	exists, err := ues.userExtRepo.Exists(Ext.UserId)
	if err != nil {
		log.Errorw("failed to check user Ext exists", "userId", Ext.UserId, "error", err)
		return err
	}
	if exists {
		return fmt.Errorf("user Ext already exists for user: %s", Ext.UserId)
	}

	if err := ues.userExtRepo.Create(Ext); err != nil {
		log.Errorw("failed to create user Ext", "userId", Ext.UserId, "error", err)
		return err
	}

	return nil
}

// UpdateUserExt updates user Ext information
func (ues *UserExt) UpdateUserExt(userId string, Ext *model.UserExt) error {
	// check if exists
	exists, err := ues.userExtRepo.Exists(userId)
	if err != nil {
		log.Errorw("failed to check user Ext exists", "userId", userId, "error", err)
		return err
	}
	if !exists {
		return fmt.Errorf("user Ext not found for user: %s", userId)
	}

	if err := ues.userExtRepo.Update(userId, Ext); err != nil {
		log.Errorw("failed to update user Ext", "userId", userId, "error", err)
		return err
	}

	return nil
}

// UpdateLastLogin updates user's last login timestamp
func (ues *UserExt) UpdateLastLogin(userId string) error {
	// create Ext record if not exists
	exists, err := ues.userExtRepo.Exists(userId)
	if err != nil {
		log.Errorw("failed to check user Ext exists", "userId", userId, "error", err)
		return err
	}

	if !exists {
		// auto-create Ext record with default values
		now := time.Now()
		Ext := &model.UserExt{
			UserId:           userId,
			Timezone:         "UTC",
			LastLoginAt:      &now,
			InvitationStatus: model.UserInvitationStatusAccepted,
		}
		if err := ues.userExtRepo.Create(Ext); err != nil {
			log.Errorw("failed to create user Ext", "userId", userId, "error", err)
			return err
		}
		return nil
	}

	if err := ues.userExtRepo.UpdateLastLogin(userId); err != nil {
		log.Errorw("failed to update last login", "userId", userId, "error", err)
		return err
	}

	return nil
}

// UpdateTimezone updates user timezone
func (ues *UserExt) UpdateTimezone(userId, timezone string) error {
	if err := ues.userExtRepo.UpdateTimezone(userId, timezone); err != nil {
		log.Errorw("failed to update timezone", "userId", userId, "timezone", timezone, "error", err)
		return err
	}
	return nil
}

// UpdateInvitationStatus updates invitation status
func (ues *UserExt) UpdateInvitationStatus(userId, status string) error {
	// validate status
	validStatuses := []string{
		model.UserInvitationStatusPending,
		model.UserInvitationStatusAccepted,
		model.UserInvitationStatusExpired,
		model.UserInvitationStatusRevoked,
	}

	isValid := slices.Contains(validStatuses, status)
	if !isValid {
		return fmt.Errorf("invalid invitation status: %s", status)
	}

	if err := ues.userExtRepo.UpdateInvitationStatus(userId, status); err != nil {
		log.Errorw("failed to update invitation status", "userId", userId, "status", status, "error", err)
		return err
	}

	return nil
}
