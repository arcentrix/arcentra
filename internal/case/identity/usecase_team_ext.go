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

	"github.com/arcentrix/arcentra/internal/domain/identity"
	"github.com/google/uuid"
)

func (uc *ManageTeamUseCase) CreateTeamFull(ctx context.Context, orgID, name, displayName, description string, visibility int, createdBy string) (*identity.Team, error) {
	teamID := uuid.NewString()
	t := &identity.Team{
		TeamID:      teamID,
		OrgID:       orgID,
		Name:        name,
		DisplayName: displayName,
		Description: description,
		Visibility:  identity.TeamVisibility(visibility),
		IsEnabled:   true,
	}
	if err := uc.teamRepo.Create(ctx, t); err != nil {
		return nil, err
	}
	return uc.teamRepo.Get(ctx, teamID)
}

func (uc *ManageTeamUseCase) UpdateTeam(ctx context.Context, teamID string, updates map[string]any) error {
	return uc.teamRepo.Update(ctx, teamID, updates)
}

func (uc *ManageTeamUseCase) ListTeams(ctx context.Context, orgID, name, parentTeamID string, visibility, isEnabled *int, page, pageSize int) (any, error) {
	teams, total, err := uc.teamRepo.List(ctx, orgID, page, pageSize)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"list":     teams,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	}, nil
}

func (uc *ManageTeamUseCase) GetTeamsByOrgID(ctx context.Context, orgID string) ([]*identity.Team, error) {
	return uc.teamRepo.ListByOrg(ctx, orgID)
}

func (uc *ManageTeamUseCase) GetSubTeams(ctx context.Context, teamID string) ([]*identity.Team, error) {
	return uc.teamRepo.ListSubTeams(ctx, teamID)
}

func (uc *ManageTeamUseCase) GetTeamsByUserID(ctx context.Context, userID string) ([]*identity.Team, error) {
	return uc.teamRepo.ListByUser(ctx, userID)
}

func (uc *ManageTeamUseCase) EnableTeam(ctx context.Context, teamID string) error {
	return uc.teamRepo.Update(ctx, teamID, map[string]any{"is_enabled": true})
}

func (uc *ManageTeamUseCase) DisableTeam(ctx context.Context, teamID string) error {
	return uc.teamRepo.Update(ctx, teamID, map[string]any{"is_enabled": false})
}

func (uc *ManageTeamUseCase) UpdateTeamStatistics(ctx context.Context, teamID string) error {
	return nil
}
