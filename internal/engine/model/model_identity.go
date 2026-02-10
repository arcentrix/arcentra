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

package model

import (
	"golang.org/x/oauth2"
	"gorm.io/datatypes"
)

// Identity 身份提供者表
type Identity struct {
	BaseModel
	ProviderId   string         `gorm:"column:provider_id" json:"providerId"`
	Name         string         `gorm:"column:name" json:"name"`
	ProviderType string         `gorm:"column:provider_type" json:"providerType"` // oauth/ldap/oidc/saml
	Config       datatypes.JSON `gorm:"column:config" json:"config"`
	Description  string         `gorm:"column:description" json:"description"`
	Priority     int            `gorm:"column:priority" json:"priority"`
	IsEnabled    int            `gorm:"column:is_enabled" json:"isEnabled"` // 0: disabled, 1: enabled
}

func (s *Identity) TableName() string {
	return "t_identity"
}

// OAuthConfig OAuth 配置
type OAuthConfig struct {
	ClientID        string            `json:"clientId"`
	ClientSecret    string            `json:"clientSecret"`
	AuthURL         string            `json:"authURL"`
	TokenURL        string            `json:"tokenURL"`
	UserInfoURL     string            `json:"userInfoURL"`
	RedirectURL     string            `json:"redirectURL"`
	Scopes          []string          `json:"scopes"`
	Endpoint        oauth2.Endpoint   `json:"endpoint"`
	Mapping         map[string]string `json:"mapping"`
	CoverAttributes bool              `json:"coverAttributes"` // 是否覆盖已存在用户的属性
}

// LDAPConfig LDAP 配置
type LDAPConfig struct {
	Host            string            `json:"host"`
	Port            int               `json:"port"`
	UseTLS          bool              `json:"useTLS"`
	SkipVerify      bool              `json:"skipVerify"`
	BaseDN          string            `json:"baseDN"`
	BindDN          string            `json:"bindDN"`
	BindPassword    string            `json:"bindPassword"`
	UserFilter      string            `json:"userFilter"`  // (uid=%s)
	UserDN          string            `json:"userDN"`      // ou=users,dc=example,dc=com
	GroupFilter     string            `json:"groupFilter"` // (memberUid=%s)
	GroupDN         string            `json:"groupDN"`     // ou=groups,dc=example,dc=com
	Attributes      map[string]string `json:"attributes"`  // username, email, displayName, groups
	Mapping         map[string]string `json:"mapping"`
	CoverAttributes bool              `json:"coverAttributes"` // 是否覆盖已存在用户的属性
}

// OIDCConfig OIDC (OpenID Connect) 配置
type OIDCConfig struct {
	Issuer          string            `json:"issuer"`
	ClientID        string            `json:"clientId"`
	ClientSecret    string            `json:"clientSecret"`
	RedirectURL     string            `json:"redirectURL"`
	Scopes          []string          `json:"scopes"`
	UserInfoURL     string            `json:"userInfoURL,omitempty"`
	SkipVerify      bool              `json:"skipVerify"`
	HostedDomain    string            `json:"hostedDomain,omitempty"` // Google Workspace domain
	Mapping         map[string]string `json:"mapping"`
	CoverAttributes bool              `json:"coverAttributes"` // 是否覆盖已存在用户的属性
}
