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

package model

import (
	"time"
)

// UserExt user ext information table
type UserExt struct {
	BaseModel
	UserId           string     `gorm:"column:user_id;uniqueIndex" json:"userId"`                           // user ID (foreign key)
	Timezone         string     `gorm:"column:timezone;default:'UTC'" json:"timezone"`                      // user timezone (e.g., 'Asia/Shanghai', 'America/New_York')
	LastLoginAt      *time.Time `gorm:"column:last_login_at" json:"lastLoginAt"`                            // last login timestamp
	InvitationStatus string     `gorm:"column:invitation_status;default:'pending'" json:"invitationStatus"` // invitation status: pending, accepted, expired, revoked
	InvitedBy        string     `gorm:"column:invited_by" json:"invitedBy"`                                 // invited by user ID
	InvitedAt        *time.Time `gorm:"column:invited_at" json:"invitedAt"`                                 // invitation timestamp
	AcceptedAt       *time.Time `gorm:"column:accepted_at" json:"acceptedAt"`                               // invitation accepted timestamp
}

func (UserExt) TableName() string {
	return "t_user_ext"
}

// UserInvitationStatus constants for user ext
const (
	UserInvitationStatusPending  = "pending"  // pending acceptance
	UserInvitationStatusAccepted = "accepted" // accepted
	UserInvitationStatusExpired  = "expired"  // expired
	UserInvitationStatusRevoked  = "revoked"  // revoked by admin
)
