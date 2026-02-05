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

package oidc

import (
	"context"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type UserInfo struct {
	Username  string
	Email     string
	Nickname  string
	AvatarURL string
}

type OIDCProvider struct {
	Provider *oidc.Provider
	Verifier *oidc.IDTokenVerifier
	Config   *oauth2.Config
}

func NewOIDCProvider(issuer, clientID, clientSecret, redirectURL string, scopes []string, skipVerify bool) (*OIDCProvider, error) {
	provider, err := oidc.NewProvider(context.Background(), issuer)
	if err != nil {
		return nil, err
	}

	return &OIDCProvider{
		Provider: provider,
		Verifier: provider.Verifier(&oidc.Config{ClientID: clientID, SkipClientIDCheck: skipVerify}),
		Config: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Scopes:       append([]string{oidc.ScopeOpenID, "email", "profile"}, scopes...),
			Endpoint:     provider.Endpoint(),
		},
	}, nil
}

func (p *OIDCProvider) GetAuthURL(state string) string {
	return p.Config.AuthCodeURL(state)
}

func (p *OIDCProvider) ExchangeToken(ctx context.Context, code string) (*oauth2.Token, error) {
	return p.Config.Exchange(ctx, code)
}

func (p *OIDCProvider) GetUserInfo(ctx context.Context, token *oauth2.Token) (*UserInfo, error) {
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("missing id_token")
	}

	idToken, err := p.Verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("invalid id_token: %w", err)
	}

	// Extract all claims as a map to support dynamic field mapping
	var claimsMap map[string]any
	if err := idToken.Claims(&claimsMap); err != nil {
		return nil, err
	}

	// Extract default fields with fallback to common field names
	userInfo := &UserInfo{}
	if v, ok := claimsMap["sub"].(string); ok {
		userInfo.Username = v
	}
	if v, ok := claimsMap["email"].(string); ok {
		userInfo.Email = v
	}
	if v, ok := claimsMap["name"].(string); ok {
		userInfo.Nickname = v
	}
	if v, ok := claimsMap["picture"].(string); ok {
		userInfo.AvatarURL = v
	}

	return userInfo, nil
}

// GetRawClaims returns raw claims map for field mapping
func (p *OIDCProvider) GetRawClaims(ctx context.Context, token *oauth2.Token) (map[string]interface{}, error) {
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("missing id_token")
	}

	idToken, err := p.Verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("invalid id_token: %w", err)
	}

	var claimsMap map[string]interface{}
	if err := idToken.Claims(&claimsMap); err != nil {
		return nil, err
	}

	return claimsMap, nil
}
