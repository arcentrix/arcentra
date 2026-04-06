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

package auth

import (
	"context"
	"errors"

	"github.com/arcentrix/arcentra/internal/domain/notification"
)

const authHeaderAuthorization = "Authorization"

// TokenAuth implements token-based authentication.
type TokenAuth struct {
	Token string
}

func NewTokenAuth(token string) *TokenAuth {
	return &TokenAuth{Token: token}
}

func (a *TokenAuth) GetAuthType() notification.AuthType { return notification.AuthToken }

func (a *TokenAuth) Authenticate(_ context.Context) (string, error) {
	if a.Token == "" {
		return "", errors.New("token cannot be empty")
	}
	return a.Token, nil
}

func (a *TokenAuth) GetAuthHeader() (string, string) {
	return authHeaderAuthorization, "Bearer " + a.Token
}

func (a *TokenAuth) Validate() error {
	if a.Token == "" {
		return errors.New("token is required")
	}
	return nil
}

// BearerAuth implements bearer token authentication.
type BearerAuth struct {
	Token string
}

func NewBearerAuth(token string) *BearerAuth {
	return &BearerAuth{Token: token}
}

func (a *BearerAuth) GetAuthType() notification.AuthType { return notification.AuthBearer }

func (a *BearerAuth) Authenticate(_ context.Context) (string, error) {
	if a.Token == "" {
		return "", errors.New("bearer token cannot be empty")
	}
	return a.Token, nil
}

func (a *BearerAuth) GetAuthHeader() (string, string) {
	return authHeaderAuthorization, "Bearer " + a.Token
}

func (a *BearerAuth) Validate() error {
	if a.Token == "" {
		return errors.New("bearer token is required")
	}
	return nil
}

// APIKeyAuth implements API Key authentication.
type APIKeyAuth struct {
	APIKey     string
	HeaderName string
	QueryParam string
}

func NewAPIKeyAuth(apiKey, headerName string) *APIKeyAuth {
	if headerName == "" {
		headerName = "X-API-Key"
	}
	return &APIKeyAuth{APIKey: apiKey, HeaderName: headerName}
}

func (a *APIKeyAuth) GetAuthType() notification.AuthType { return notification.AuthAPIKey }

func (a *APIKeyAuth) Authenticate(_ context.Context) (string, error) {
	if a.APIKey == "" {
		return "", errors.New("api key cannot be empty")
	}
	return a.APIKey, nil
}

func (a *APIKeyAuth) GetAuthHeader() (string, string) {
	return a.HeaderName, a.APIKey
}

func (a *APIKeyAuth) Validate() error {
	if a.APIKey == "" {
		return errors.New("api key is required")
	}
	if a.HeaderName == "" {
		return errors.New("header name is required")
	}
	return nil
}

// BasicAuth implements basic authentication.
type BasicAuth struct {
	Username string
	Password string
}

func NewBasicAuth(username, password string) *BasicAuth {
	return &BasicAuth{Username: username, Password: password}
}

func (a *BasicAuth) GetAuthType() notification.AuthType { return notification.AuthBasic }

func (a *BasicAuth) Authenticate(_ context.Context) (string, error) {
	if a.Username == "" || a.Password == "" {
		return "", errors.New("username and password cannot be empty")
	}
	return "", nil
}

func (a *BasicAuth) GetAuthHeader() (string, string) {
	return "Authorization", "Basic " + a.encodeBasicAuth()
}

func (a *BasicAuth) Validate() error {
	if a.Username == "" {
		return errors.New("username is required")
	}
	if a.Password == "" {
		return errors.New("password is required")
	}
	return nil
}

// OAuth2Auth implements OAuth2 authentication.
type OAuth2Auth struct {
	ClientID     string
	ClientSecret string
	TokenURL     string
	AccessToken  string
	RefreshToken string
}

func NewOAuth2Auth(clientID, clientSecret, tokenURL string) *OAuth2Auth {
	return &OAuth2Auth{ClientID: clientID, ClientSecret: clientSecret, TokenURL: tokenURL}
}

func (a *OAuth2Auth) GetAuthType() notification.AuthType { return notification.AuthOAuth2 }

func (a *OAuth2Auth) Authenticate(_ context.Context) (string, error) {
	if a.AccessToken != "" {
		return a.AccessToken, nil
	}
	return "", errors.New("oauth2 token acquisition not implemented, please set access token directly")
}

func (a *OAuth2Auth) GetAuthHeader() (string, string) {
	if a.AccessToken == "" {
		return "", ""
	}
	return "Authorization", "Bearer " + a.AccessToken
}

func (a *OAuth2Auth) Validate() error {
	if a.ClientID == "" {
		return errors.New("client id is required")
	}
	if a.ClientSecret == "" {
		return errors.New("client secret is required")
	}
	if a.TokenURL == "" && a.AccessToken == "" {
		return errors.New("token url or access token is required")
	}
	return nil
}

func (a *OAuth2Auth) SetAccessToken(token string) {
	a.AccessToken = token
}
