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

package identity

import (
	"context"
	"fmt"

	"github.com/arcentrix/arcentra/internal/domain/identity"
)

func (uc *ManageUserUseCase) GetUserExt(ctx context.Context, userID string) (*identity.UserExt, error) {
	if uc.extRepo == nil {
		return nil, fmt.Errorf("user ext repository not configured")
	}
	return uc.extRepo.Get(ctx, userID)
}

func (uc *ManageUserUseCase) UpdateUserExt(ctx context.Context, userID string, updates map[string]any) error {
	if uc.extRepo == nil {
		return fmt.Errorf("user ext repository not configured")
	}
	ext := &identity.UserExt{UserID: userID}
	if tz, ok := updates["timezone"].(string); ok {
		ext.Timezone = tz
	}
	return uc.extRepo.Update(ctx, userID, ext)
}

func (uc *ManageUserUseCase) UpdateTimezone(ctx context.Context, userID, timezone string) error {
	if uc.extRepo == nil {
		return fmt.Errorf("user ext repository not configured")
	}
	return uc.extRepo.UpdateTimezone(ctx, userID, timezone)
}

func (uc *ManageUserUseCase) UpdateInvitationStatus(ctx context.Context, userID, status string) error {
	if uc.extRepo == nil {
		return fmt.Errorf("user ext repository not configured")
	}
	return uc.extRepo.UpdateInvitationStatus(ctx, userID, identity.InvitationStatus(status))
}
