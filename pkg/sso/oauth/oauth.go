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

package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
	defer func() { _ = resp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user info request failed: %s", resp.Status)
	}

	var dataMap map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&dataMap); err != nil {
		return nil, err
	}

	// GitHub-specific fallback:
	// - https://api.github.com/user often returns email null unless the email is public.
	// - With scope "user:email", we can call https://api.github.com/user/emails to get the primary email.
	// To avoid creating placeholder emails like "<username>@GitHub.com", try to populate email here.
	if isEmptyString(dataMap["email"]) && looksLikeGitHubUserInfoURL(p.UserInfoURL) {
		if email, err := fetchGitHubPrimaryEmail(ctx, token); err == nil && email != "" {
			dataMap["email"] = email
		}
	}

	return dataMap, nil
}

func isEmptyString(v any) bool {
	switch vv := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(vv) == ""
	default:
		// GitHub may return email: null, but other providers might return non-string types.
		// Treat non-string as "present" to avoid surprising overwrites.
		return false
	}
}

func looksLikeGitHubUserInfoURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	// Common GitHub userinfo endpoints:
	// - https://api.github.com/user
	// - https://github.com/api/v3/user (GitHub Enterprise)
	host := strings.ToLower(u.Host)
	path := strings.TrimSuffix(u.Path, "/")
	if !strings.Contains(host, "github") {
		return false
	}
	return path == "/user" || strings.HasSuffix(path, "/api/v3/user")
}

type gitHubEmailItem struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func fetchGitHubPrimaryEmail(ctx context.Context, token *oauth2.Token) (string, error) {
	// NOTE: GitHub requires "user:email" scope for this endpoint.
	const emailsURL = "https://api.github.com/user/emails"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, emailsURL, nil)
	if err != nil {
		return "", err
	}
	token.SetAuthHeader(req)
	// GitHub API likes an explicit accept header.
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github emails request failed: %s", resp.Status)
	}

	var items []gitHubEmailItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return "", err
	}
	// Prefer primary+verified, then primary, then first verified, then first.
	for _, it := range items {
		if it.Primary && it.Verified && strings.TrimSpace(it.Email) != "" {
			return it.Email, nil
		}
	}
	for _, it := range items {
		if it.Primary && strings.TrimSpace(it.Email) != "" {
			return it.Email, nil
		}
	}
	for _, it := range items {
		if it.Verified && strings.TrimSpace(it.Email) != "" {
			return it.Email, nil
		}
	}
	if len(items) > 0 {
		return strings.TrimSpace(items[0].Email), nil
	}
	return "", nil
}
