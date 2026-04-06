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
	"encoding/json"
	"time"
)

// User represents a user account in the system.
type User struct {
	ID           uint64    `json:"id"`
	UserID       string    `json:"userId"`
	Username     string    `json:"username"`
	FullName     string    `json:"fullName"`
	Password     string    `json:"-"`
	Avatar       string    `json:"avatar"`
	Email        string    `json:"email"`
	Phone        string    `json:"phone"`
	IsEnabled    bool      `json:"isEnabled"`
	IsSuperAdmin bool      `json:"isSuperAdmin"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// UserExt holds extended user information.
type UserExt struct {
	ID               uint64           `json:"id"`
	UserID           string           `json:"userId"`
	Timezone         string           `json:"timezone"`
	LastLoginAt      *time.Time       `json:"lastLoginAt"`
	InvitationStatus InvitationStatus `json:"invitationStatus"`
	InvitedBy        string           `json:"invitedBy"`
	InvitedAt        *time.Time       `json:"invitedAt"`
	AcceptedAt       *time.Time       `json:"acceptedAt"`
	CreatedAt        time.Time        `json:"createdAt"`
	UpdatedAt        time.Time        `json:"updatedAt"`
}

// Role represents a role with a set of permissions.
type Role struct {
	ID          uint64    `json:"id"`
	RoleID      string    `json:"roleId"`
	Name        string    `json:"name"`
	DisplayName string    `json:"displayName"`
	Description string    `json:"description"`
	IsEnabled   bool      `json:"isEnabled"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Menu represents a navigable menu item in the UI.
type Menu struct {
	ID          uint64    `json:"id"`
	MenuID      string    `json:"menuId"`
	ParentID    string    `json:"parentId"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Component   string    `json:"component"`
	Icon        string    `json:"icon"`
	Order       int       `json:"order"`
	IsVisible   bool      `json:"isVisible"`
	IsEnabled   bool      `json:"isEnabled"`
	Description string    `json:"description"`
	Meta        string    `json:"meta"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Team represents an organizational team.
type Team struct {
	ID            uint64          `json:"id"`
	TeamID        string          `json:"teamId"`
	OrgID         string          `json:"orgId"`
	Name          string          `json:"name"`
	DisplayName   string          `json:"displayName"`
	Description   string          `json:"description"`
	Avatar        string          `json:"avatar"`
	ParentTeamID  string          `json:"parentTeamId"`
	Path          string          `json:"path"`
	Level         int             `json:"level"`
	Settings      json.RawMessage `json:"settings"`
	Visibility    TeamVisibility  `json:"visibility"`
	IsEnabled     bool            `json:"isEnabled"`
	TotalMembers  int             `json:"totalMembers"`
	TotalProjects int             `json:"totalProjects"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

// TeamMember represents the membership of a user in a team.
type TeamMember struct {
	ID        uint64    `json:"id"`
	TeamID    string    `json:"teamId"`
	UserID    string    `json:"userId"`
	RoleID    string    `json:"roleId"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// UserRoleBinding links a user to a role.
type UserRoleBinding struct {
	ID        int       `json:"id"`
	BindingID string    `json:"bindingId"`
	UserID    string    `json:"userId"`
	RoleID    string    `json:"roleId"`
	GrantedBy *string   `json:"grantedBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// RoleMenuBinding links a role to a menu with access control.
type RoleMenuBinding struct {
	ID           uint64    `json:"id"`
	RoleMenuID   string    `json:"roleMenuId"`
	RoleID       string    `json:"roleId"`
	MenuID       string    `json:"menuId"`
	ResourceID   string    `json:"resourceId"`
	IsVisible    bool      `json:"isVisible"`
	IsAccessible bool      `json:"isAccessible"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// IdentityProvider represents an external identity provider (OAuth, LDAP, OIDC).
type IdentityProvider struct {
	ID           uint64          `json:"id"`
	ProviderID   string          `json:"providerId"`
	Name         string          `json:"name"`
	ProviderType ProviderType    `json:"providerType"`
	Config       json.RawMessage `json:"config"`
	Description  string          `json:"description"`
	Priority     int             `json:"priority"`
	IsEnabled    bool            `json:"isEnabled"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

// OAuthEndpoint holds OAuth token/auth URLs (pure domain, no oauth2 dependency).
type OAuthEndpoint struct {
	AuthURL  string `json:"authUrl"`
	TokenURL string `json:"tokenUrl"`
}

// OAuthConfig holds OAuth provider configuration.
type OAuthConfig struct {
	ClientID        string            `json:"clientId"`
	ClientSecret    string            `json:"clientSecret"`
	AuthURL         string            `json:"authUrl"`
	TokenURL        string            `json:"tokenUrl"`
	UserInfoURL     string            `json:"userInfoUrl"`
	RedirectURL     string            `json:"redirectUrl"`
	Scopes          []string          `json:"scopes"`
	Endpoint        OAuthEndpoint     `json:"endpoint"`
	Mapping         map[string]string `json:"mapping"`
	CoverAttributes bool              `json:"coverAttributes"`
}

// LDAPConfig holds LDAP provider configuration.
type LDAPConfig struct {
	Host            string            `json:"host"`
	Port            int               `json:"port"`
	UseTLS          bool              `json:"useTLS"`
	SkipVerify      bool              `json:"skipVerify"`
	BaseDN          string            `json:"baseDN"`
	BindDN          string            `json:"bindDN"`
	BindPassword    string            `json:"bindPassword"`
	UserFilter      string            `json:"userFilter"`
	UserDN          string            `json:"userDN"`
	GroupFilter     string            `json:"groupFilter"`
	GroupDN         string            `json:"groupDN"`
	Attributes      map[string]string `json:"attributes"`
	Mapping         map[string]string `json:"mapping"`
	CoverAttributes bool              `json:"coverAttributes"`
}

// OIDCConfig holds OpenID Connect provider configuration.
type OIDCConfig struct {
	Issuer          string            `json:"issuer"`
	ClientID        string            `json:"clientId"`
	ClientSecret    string            `json:"clientSecret"`
	RedirectURL     string            `json:"redirectUrl"`
	Scopes          []string          `json:"scopes"`
	UserInfoURL     string            `json:"userInfoUrl"`
	SkipVerify      bool              `json:"skipVerify"`
	HostedDomain    string            `json:"hostedDomain"`
	Mapping         map[string]string `json:"mapping"`
	CoverAttributes bool              `json:"coverAttributes"`
}
