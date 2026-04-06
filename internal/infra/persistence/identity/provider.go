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
	domain "github.com/arcentrix/arcentra/internal/domain/identity"
	"github.com/google/wire"
)

var ProviderSet = wire.NewSet(
	NewUserRepo, wire.Bind(new(domain.IUserRepository), new(*UserRepo)),
	NewUserExtRepo, wire.Bind(new(domain.IUserExtRepository), new(*UserExtRepo)),
	NewRoleRepo, wire.Bind(new(domain.IRoleRepository), new(*RoleRepo)),
	NewMenuRepo, wire.Bind(new(domain.IMenuRepository), new(*MenuRepo)),
	NewTeamRepo, wire.Bind(new(domain.ITeamRepository), new(*TeamRepo)),
	NewTeamMemberRepo, wire.Bind(new(domain.ITeamMemberRepository), new(*TeamMemberRepo)),
	NewUserRoleBindingRepo, wire.Bind(new(domain.IUserRoleBindingRepository), new(*UserRoleBindingRepo)),
	NewRoleMenuBindingRepo, wire.Bind(new(domain.IRoleMenuBindingRepository), new(*RoleMenuBindingRepo)),
	NewIdentityProviderRepo, wire.Bind(new(domain.IIdentityProviderRepository), new(*IdentityProviderRepo)),
)
