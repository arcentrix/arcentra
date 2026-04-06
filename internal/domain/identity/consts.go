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

// Built-in organization role IDs.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Built-in team member role IDs.
const (
	TeamRoleOwner      = "owner"
	TeamRoleMaintainer = "maintainer"
	TeamRoleDeveloper  = "developer"
	TeamRoleReporter   = "reporter"
	TeamRoleGuest      = "guest"
)

// InvitationStatus represents the state of a user invitation.
type InvitationStatus string

const (
	InvitationPending  InvitationStatus = "pending"
	InvitationAccepted InvitationStatus = "accepted"
	InvitationExpired  InvitationStatus = "expired"
	InvitationRevoked  InvitationStatus = "revoked"
)

// TeamVisibility controls who can see the team.
type TeamVisibility int

const (
	TeamVisibilityPrivate  TeamVisibility = 0
	TeamVisibilityInternal TeamVisibility = 1
	TeamVisibilityPublic   TeamVisibility = 2
)

func (v TeamVisibility) String() string {
	switch v {
	case TeamVisibilityPrivate:
		return "private"
	case TeamVisibilityInternal:
		return "internal"
	case TeamVisibilityPublic:
		return "public"
	default:
		return "unknown"
	}
}

// ProviderType represents the type of identity provider.
type ProviderType string

const (
	ProviderTypeOAuth ProviderType = "oauth"
	ProviderTypeLDAP  ProviderType = "ldap"
	ProviderTypeOIDC  ProviderType = "oidc"
	ProviderTypeSAML  ProviderType = "saml"
)

func (t ProviderType) IsValid() bool {
	switch t {
	case ProviderTypeOAuth, ProviderTypeLDAP, ProviderTypeOIDC, ProviderTypeSAML:
		return true
	default:
		return false
	}
}
