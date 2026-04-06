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

package identity

import (
	"context"

	"github.com/arcentrix/arcentra/internal/domain/identity"
	"github.com/google/uuid"
)

type ManageUserUseCase struct {
	userRepo        identity.IUserRepository
	extRepo         identity.IUserExtRepository
	idpRepo         identity.IIdentityProviderRepository
	roleBindingRepo identity.IUserRoleBindingRepository
	roleRepo        identity.IRoleRepository
}

func NewManageUserUseCase(
	userRepo identity.IUserRepository,
	extRepo identity.IUserExtRepository,
	idpRepo identity.IIdentityProviderRepository,
	roleBindingRepo identity.IUserRoleBindingRepository,
	roleRepo identity.IRoleRepository,
) *ManageUserUseCase {
	return &ManageUserUseCase{
		userRepo:        userRepo,
		extRepo:         extRepo,
		idpRepo:         idpRepo,
		roleBindingRepo: roleBindingRepo,
		roleRepo:        roleRepo,
	}
}

func (uc *ManageUserUseCase) CreateUser(ctx context.Context, input RegisterInput) (*identity.User, error) {
	u := &identity.User{
		UserID:       input.UserID,
		Username:     input.Username,
		FullName:     input.FullName,
		Email:        input.Email,
		Password:     input.Password,
		IsEnabled:    true,
		IsSuperAdmin: false,
	}
	if err := uc.userRepo.Create(ctx, u); err != nil {
		return nil, err
	}
	return uc.userRepo.Get(ctx, input.UserID)
}

func (uc *ManageUserUseCase) GetUser(ctx context.Context, userID string) (*identity.User, error) {
	return uc.userRepo.Get(ctx, userID)
}

func (uc *ManageUserUseCase) UpdateUser(ctx context.Context, userID string, input UpdateUserInput) error {
	updates := make(map[string]any)
	if input.FullName != nil {
		updates["full_name"] = *input.FullName
	}
	if input.Avatar != nil {
		updates["avatar"] = *input.Avatar
	}
	if input.Email != nil {
		updates["email"] = *input.Email
	}
	if input.Phone != nil {
		updates["phone"] = *input.Phone
	}
	if input.IsEnabled != nil {
		updates["is_enabled"] = *input.IsEnabled
	}
	if len(updates) == 0 {
		return nil
	}
	return uc.userRepo.Update(ctx, userID, updates)
}

func (uc *ManageUserUseCase) DeleteUser(ctx context.Context, userID string) error {
	return uc.userRepo.Delete(ctx, userID)
}

func (uc *ManageUserUseCase) ListUsers(ctx context.Context, page, size int) ([]identity.User, int64, error) {
	return uc.userRepo.List(ctx, page, size)
}

func (uc *ManageUserUseCase) ResetPassword(ctx context.Context, userID, newPasswordHash string) error {
	return uc.userRepo.ResetPassword(ctx, userID, newPasswordHash)
}

type ManageRoleUseCase struct {
	roleRepo identity.IRoleRepository
}

func NewManageRoleUseCase(roleRepo identity.IRoleRepository) *ManageRoleUseCase {
	return &ManageRoleUseCase{roleRepo: roleRepo}
}

func (uc *ManageRoleUseCase) CreateRole(ctx context.Context, input CreateRoleInput) (*identity.Role, error) {
	r := &identity.Role{
		RoleID:      input.RoleID,
		Name:        input.Name,
		DisplayName: input.DisplayName,
		Description: input.Description,
		IsEnabled:   true,
	}
	if err := uc.roleRepo.Create(ctx, r); err != nil {
		return nil, err
	}
	return uc.roleRepo.Get(ctx, input.RoleID)
}

func (uc *ManageRoleUseCase) GetRole(ctx context.Context, roleID string) (*identity.Role, error) {
	return uc.roleRepo.Get(ctx, roleID)
}

func (uc *ManageRoleUseCase) ListRoles(ctx context.Context, page, size int) ([]identity.Role, int64, error) {
	return uc.roleRepo.List(ctx, page, size)
}

func (uc *ManageRoleUseCase) UpdateRole(ctx context.Context, roleID string, updates map[string]any) error {
	return uc.roleRepo.Update(ctx, roleID, updates)
}

func (uc *ManageRoleUseCase) DeleteRole(ctx context.Context, roleID string) error {
	return uc.roleRepo.Delete(ctx, roleID)
}

type ManageTeamUseCase struct {
	teamRepo   identity.ITeamRepository
	memberRepo identity.ITeamMemberRepository
}

func NewManageTeamUseCase(
	teamRepo identity.ITeamRepository,
	memberRepo identity.ITeamMemberRepository,
) *ManageTeamUseCase {
	return &ManageTeamUseCase{teamRepo: teamRepo, memberRepo: memberRepo}
}

func (uc *ManageTeamUseCase) CreateTeam(ctx context.Context, input CreateTeamInput) (*identity.Team, error) {
	teamID := uuid.NewString()
	t := &identity.Team{
		TeamID:      teamID,
		OrgID:       input.OrgID,
		Name:        input.Name,
		DisplayName: input.DisplayName,
		Description: input.Description,
		Visibility:  identity.TeamVisibility(input.Visibility),
		IsEnabled:   true,
	}
	if err := uc.teamRepo.Create(ctx, t); err != nil {
		return nil, err
	}
	return uc.teamRepo.Get(ctx, teamID)
}

func (uc *ManageTeamUseCase) GetTeam(ctx context.Context, teamID string) (*identity.Team, error) {
	return uc.teamRepo.Get(ctx, teamID)
}

func (uc *ManageTeamUseCase) DeleteTeam(ctx context.Context, teamID string) error {
	return uc.teamRepo.Delete(ctx, teamID)
}

func (uc *ManageTeamUseCase) AddMember(ctx context.Context, teamID, userID, roleID string) error {
	return uc.memberRepo.Add(ctx, &identity.TeamMember{
		TeamID: teamID,
		UserID: userID,
		RoleID: roleID,
	})
}

func (uc *ManageTeamUseCase) RemoveMember(ctx context.Context, teamID, userID string) error {
	return uc.memberRepo.Remove(ctx, teamID, userID)
}
