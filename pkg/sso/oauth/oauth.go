// Copyright 2025 Arcentra Team
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

package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
)

type UserInfo struct {
	Username  string
	Email     string
	Nickname  string
	AvatarURL string
}

type OAuthProvider struct {
	Config      *oauth2.Config
	UserInfoURL string
}

func NewOAuthProvider(clientID, clientSecret, redirectURL string, scopes []string, endpoint oauth2.Endpoint, userInfoURL string) *OAuthProvider {
	return &OAuthProvider{
		Config: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Scopes:       scopes,
			Endpoint:     endpoint,
		},
		UserInfoURL: userInfoURL,
	}
}

func (p *OAuthProvider) GetAuthURL(state string) string {
	return p.Config.AuthCodeURL(state)
}

func (p *OAuthProvider) ExchangeToken(ctx context.Context, code string) (*oauth2.Token, error) {
	return p.Config.Exchange(ctx, code)
}

func (p *OAuthProvider) GetUserInfo(ctx context.Context, token *oauth2.Token) (*UserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.UserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	token.SetAuthHeader(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user info request failed: %s", resp.Status)
	}

	// Decode as map to support dynamic field mapping
	var dataMap map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&dataMap); err != nil {
		return nil, err
	}

	// Extract default fields with fallback to common field names
	userInfo := &UserInfo{}
	if v, ok := dataMap["login"].(string); ok {
		userInfo.Username = v
	}
	if v, ok := dataMap["email"].(string); ok {
		userInfo.Email = v
	}
	if v, ok := dataMap["name"].(string); ok {
		userInfo.Nickname = v
	}
	if v, ok := dataMap["avatar_url"].(string); ok {
		userInfo.AvatarURL = v
	}

	return userInfo, nil
}

// GetRawUserInfo returns raw user info map for field mapping
func (p *OAuthProvider) GetRawUserInfo(ctx context.Context, token *oauth2.Token) (map[string]interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.UserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	token.SetAuthHeader(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user info request failed: %s", resp.Status)
	}

	var dataMap map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&dataMap); err != nil {
		return nil, err
	}

	return dataMap, nil
}
