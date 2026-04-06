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

import "context"

// IUserRepository defines persistence operations for User entities.
type IUserRepository interface {
	Create(ctx context.Context, user *User) error
	Get(ctx context.Context, userID string) (*User, error)
	GetByUsername(ctx context.Context, username string) (*User, error)
	GetByEmail(ctx context.Context, email string) (*User, error)
	Update(ctx context.Context, userID string, updates map[string]any) error
	Delete(ctx context.Context, userID string) error
	List(ctx context.Context, page, size int) ([]User, int64, error)
	GetPassword(ctx context.Context, userID string) (string, error)
	ResetPassword(ctx context.Context, userID, passwordHash string) error
}

// IUserExtRepository defines persistence operations for UserExt entities.
type IUserExtRepository interface {
	Get(ctx context.Context, userID string) (*UserExt, error)
	Create(ctx context.Context, ext *UserExt) error
	Update(ctx context.Context, userID string, ext *UserExt) error
	UpdateLastLogin(ctx context.Context, userID string) error
	UpdateTimezone(ctx context.Context, userID, timezone string) error
	UpdateInvitationStatus(ctx context.Context, userID string, status InvitationStatus) error
	Delete(ctx context.Context, userID string) error
	Exists(ctx context.Context, userID string) (bool, error)
}

// IRoleRepository defines persistence operations for Role entities.
type IRoleRepository interface {
	Create(ctx context.Context, role *Role) error
	Get(ctx context.Context, roleID string) (*Role, error)
	BatchGet(ctx context.Context, roleIDs []string) ([]Role, error)
	List(ctx context.Context, page, size int) ([]Role, int64, error)
	Update(ctx context.Context, roleID string, updates map[string]any) error
	Delete(ctx context.Context, roleID string) error
}

// IMenuRepository defines persistence operations for Menu entities.
type IMenuRepository interface {
	Get(ctx context.Context, menuID string) (*Menu, error)
	BatchGet(ctx context.Context, menuIDs []string) ([]Menu, error)
	List(ctx context.Context) ([]Menu, error)
	ListByParent(ctx context.Context, parentID string) ([]Menu, error)
}

// ITeamRepository defines persistence operations for Team entities.
type ITeamRepository interface {
	Create(ctx context.Context, team *Team) error
	Get(ctx context.Context, teamID string) (*Team, error)
	GetByName(ctx context.Context, orgID, name string) (*Team, error)
	Update(ctx context.Context, teamID string, updates map[string]any) error
	Delete(ctx context.Context, teamID string) error
	List(ctx context.Context, orgID string, page, size int) ([]*Team, int64, error)
	ListByOrg(ctx context.Context, orgID string) ([]*Team, error)
	ListSubTeams(ctx context.Context, parentTeamID string) ([]*Team, error)
	ListByUser(ctx context.Context, userID string) ([]*Team, error)
	BatchGet(ctx context.Context, teamIDs []string) ([]*Team, error)
	Exists(ctx context.Context, teamID string) (bool, error)
	NameExists(ctx context.Context, orgID, name string, excludeTeamID ...string) (bool, error)
	UpdatePath(ctx context.Context, teamID, path string, level int) error
	IncrementMembers(ctx context.Context, teamID string, delta int) error
	IncrementProjects(ctx context.Context, teamID string, delta int) error
}

// ITeamMemberRepository defines persistence operations for TeamMember entities.
type ITeamMemberRepository interface {
	Get(ctx context.Context, teamID, userID string) (*TeamMember, error)
	ListByTeam(ctx context.Context, teamID string) ([]TeamMember, error)
	ListByUser(ctx context.Context, userID string) ([]TeamMember, error)
	Add(ctx context.Context, member *TeamMember) error
	UpdateRole(ctx context.Context, teamID, userID, roleID string) error
	Remove(ctx context.Context, teamID, userID string) error
}

// IUserRoleBindingRepository defines persistence operations for UserRoleBinding entities.
type IUserRoleBindingRepository interface {
	List(ctx context.Context, userID string) ([]UserRoleBinding, error)
	GetByRole(ctx context.Context, userID, roleID string) (*UserRoleBinding, error)
	Create(ctx context.Context, binding *UserRoleBinding) error
	Delete(ctx context.Context, bindingID string) error
	DeleteByUser(ctx context.Context, userID string) error
}

// IRoleMenuBindingRepository defines persistence operations for RoleMenuBinding entities.
type IRoleMenuBindingRepository interface {
	List(ctx context.Context, roleID string) ([]RoleMenuBinding, error)
	ListByResource(ctx context.Context, roleID, resourceID string) ([]RoleMenuBinding, error)
	ListByRoles(ctx context.Context, roleIDs []string, resourceID string) ([]RoleMenuBinding, error)
	Create(ctx context.Context, binding *RoleMenuBinding) error
	Delete(ctx context.Context, roleMenuID string) error
}

// IIdentityProviderRepository defines persistence operations for IdentityProvider entities.
type IIdentityProviderRepository interface {
	Get(ctx context.Context, name string) (*IdentityProvider, error)
	GetByType(ctx context.Context, providerType ProviderType) ([]IdentityProvider, error)
	List(ctx context.Context) ([]IdentityProvider, error)
	ListTypes(ctx context.Context) ([]string, error)
	Create(ctx context.Context, provider *IdentityProvider) error
	Update(ctx context.Context, name string, provider *IdentityProvider) error
	Delete(ctx context.Context, name string) error
	Exists(ctx context.Context, name string) (bool, error)
	Toggle(ctx context.Context, name string) error
}
