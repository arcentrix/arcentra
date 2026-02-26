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

package sso

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/arcentrix/arcentra/pkg/sso/oauth"
	"github.com/arcentrix/arcentra/pkg/sso/oidc"
	"golang.org/x/oauth2"
)

type ProviderConfig struct {
	Type         string // "oauth" or "oidc"
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	Endpoint     oauth2.Endpoint
	FieldMap     map[string]string // field mapping

	// OIDC fields
	Issuer     string
	SkipVerify bool

	// OAuth fields
	UserInfoURL string
}

func NewSSOProvider(conf *ProviderConfig) (ISSOProvider, error) {
	switch conf.Type {
	case "oidc":
		provider, err := oidc.NewOIDCProvider(
			conf.Issuer,
			conf.ClientID,
			conf.ClientSecret,
			conf.RedirectURL,
			conf.Scopes,
			conf.SkipVerify,
		)
		if err != nil {
			return nil, err
		}
		return &oidcAdapter{provider: provider, fieldMap: conf.FieldMap}, nil
	case "oauth":
		return &oauthAdapter{
			provider: oauth.NewOAuthProvider(
				conf.ClientID,
				conf.ClientSecret,
				conf.RedirectURL,
				conf.Scopes,
				conf.Endpoint,
				conf.UserInfoURL,
			),
			fieldMap: conf.FieldMap,
		}, nil
	default:
		return nil, fmt.Errorf("unknown provider type: %s", conf.Type)
	}
}

// oauthAdapter is a adapter for oauth provider
type oauthAdapter struct {
	provider *oauth.Provider
	fieldMap map[string]string
}

// AuthCodeURL get auth url
func (a *oauthAdapter) AuthCodeURL(state string) string {
	return a.provider.GetAuthURL(state)
}

// ExchangeToken exchange token
func (a *oauthAdapter) ExchangeToken(ctx context.Context, code string) (*oauth2.Token, error) {
	return a.provider.ExchangeToken(ctx, code)
}

// GetUserInfo get user info
func (a *oauthAdapter) GetUserInfo(ctx context.Context, token *oauth2.Token) (*UserInfoAdapter, error) {
	// Get raw user info map to support field mapping
	rawData, err := a.provider.GetRawUserInfo(ctx, token)
	if err != nil {
		return nil, err
	}

	// Apply field mapping from raw data
	return ApplyFieldMappingFromRaw(rawData, a.fieldMap), nil
}

// oidcAdapter is a adapter for oidc provider
type oidcAdapter struct {
	provider *oidc.Provider
	fieldMap map[string]string
}

// AuthCodeURL get auth url
func (a *oidcAdapter) AuthCodeURL(state string) string {
	return a.provider.GetAuthURL(state)
}

// ExchangeToken exchange token
func (a *oidcAdapter) ExchangeToken(ctx context.Context, code string) (*oauth2.Token, error) {
	return a.provider.ExchangeToken(ctx, code)
}

// GetUserInfo get user info
func (a *oidcAdapter) GetUserInfo(ctx context.Context, token *oauth2.Token) (*UserInfoAdapter, error) {
	// Get raw claims map to support field mapping
	rawClaims, err := a.provider.GetRawClaims(ctx, token)
	if err != nil {
		return nil, err
	}

	// Apply field mapping from raw claims
	return ApplyFieldMappingFromRaw(rawClaims, a.fieldMap), nil
}

// Default field names used for fallback when mapping is not provided or incomplete
var (
	defaultUsernameFields = []string{"login", "preferred_username", "email"}
	defaultIDFields       = []string{"sub", "id"}
	defaultAvatarFields   = []string{"avatar_url", "picture"}
)

// ApplyFieldMappingFromRaw extracts and maps user information from raw provider response data.
//
// The function applies field mapping rules defined in fieldMap, where each entry maps
// a target field name to a source field name in the raw data.
//
// Mapping format: {"targetField": "sourceField"}
// Example: {"username": "login", "email": "email_address", "name": "display_name"}
//
// If a field is not mapped or the mapped source field is missing, the function will
// attempt to use default field names as fallback.
//
// Supported target fields: username, email, name, nickname, avatar_url/avatarUrl, picture, id/sub
func ApplyFieldMappingFromRaw(
	rawData map[string]any,
	fieldMap map[string]string,
) *UserInfoAdapter {
	result := &UserInfoAdapter{}

	// Extract string value from raw data with type conversion support
	getStringValue := func(key string) string {
		v, ok := rawData[key]
		if !ok || v == nil {
			return ""
		}

		switch val := v.(type) {
		case string:
			return val
		case float64:
			return strconv.FormatInt(int64(val), 10)
		case int:
			return strconv.Itoa(val)
		case int64:
			return strconv.FormatInt(val, 10)
		case json.Number:
			return val.String()
		default:
			return fmt.Sprintf("%v", val)
		}
	}

	// Field setters map: maps target field names to setter functions
	setters := map[string]func(string){
		"username": func(v string) { result.Username = v },
		"email":    func(v string) { result.Email = v },
		"name":     func(v string) { result.Name = v },
		"nickname": func(v string) { result.Nickname = v },

		"avatar_url": func(v string) { result.AvatarURL = v },
		"avatarUrl":  func(v string) { result.AvatarURL = v },
		"picture":    func(v string) { result.Picture = v },

		"id":  func(v string) { result.ID = v },
		"sub": func(v string) { result.ID = v },
	}

	// Apply field mappings from configuration
	for targetField, sourceField := range fieldMap {
		value := getStringValue(sourceField)
		if value == "" {
			continue
		}
		if setter, ok := setters[targetField]; ok {
			setter(value)
		}
	}

	// Apply fallback values for missing fields
	applyFallbacks(result, getStringValue)

	return result
}

// applyFallbacks applies default field values when mapped fields are missing.
func applyFallbacks(result *UserInfoAdapter, getStringValue func(string) string) {
	if result.ID == "" {
		result.ID = getFirstNonEmpty(getStringValue, defaultIDFields...)
	}

	if result.Username == "" {
		result.Username = getFirstNonEmpty(getStringValue, defaultUsernameFields...)
	}

	if result.Email == "" {
		result.Email = getStringValue("email")
	}

	if result.Name == "" {
		result.Name = getStringValue("name")
	}

	if result.Nickname == "" {
		result.Nickname = getFirstNonEmpty(getStringValue, "nickname", "name")
	}

	if result.AvatarURL == "" {
		result.AvatarURL = getFirstNonEmpty(getStringValue, defaultAvatarFields...)
	}
}

// getFirstNonEmpty returns the first non-empty value from the given fields.
func getFirstNonEmpty(getValue func(string) string, fields ...string) string {
	for _, field := range fields {
		if v := getValue(field); v != "" {
			return v
		}
	}
	return ""
}
