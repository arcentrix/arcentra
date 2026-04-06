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

import "time"

// UserRegistered is raised when a new user account is created.
type UserRegistered struct {
	UserID     string
	Username   string
	OccurredAt time.Time
}

func (e UserRegistered) EventType() string { return "identity.user_registered" }

// UserRoleChanged is raised when a user's role assignment changes.
type UserRoleChanged struct {
	UserID     string
	OldRoleID  string
	NewRoleID  string
	OccurredAt time.Time
}

func (e UserRoleChanged) EventType() string { return "identity.user_role_changed" }

// TeamCreated is raised when a new team is created.
type TeamCreated struct {
	TeamID     string
	OrgID      string
	Name       string
	OccurredAt time.Time
}

func (e TeamCreated) EventType() string { return "identity.team_created" }

// TeamMemberAdded is raised when a user is added to a team.
type TeamMemberAdded struct {
	TeamID     string
	UserID     string
	RoleID     string
	OccurredAt time.Time
}

func (e TeamMemberAdded) EventType() string { return "identity.team_member_added" }

// IdentityProviderConfigured is raised when an identity provider is added or updated.
type IdentityProviderConfigured struct {
	ProviderName string
	ProviderType ProviderType
	OccurredAt   time.Time
}

func (e IdentityProviderConfigured) EventType() string {
	return "identity.provider_configured"
}
