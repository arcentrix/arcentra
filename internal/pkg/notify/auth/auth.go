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

package auth

import (
	"context"
	"errors"
)

// AuthType represents the authentication type
type AuthType string

const (
	AuthTypeToken  AuthType = "token"  // Token authentication
	AuthTypeOAuth2 AuthType = "oauth2" // OAuth2 authentication
	AuthTypeAPIKey AuthType = "apikey" // API Key authentication
	AuthTypeBasic  AuthType = "basic"  // Basic authentication
	AuthTypeBearer AuthType = "bearer" // Bearer token authentication
)

// IAuthProvider defines the interface for authentication providers
type IAuthProvider interface {
	// GetAuthType gets the authentication type
	GetAuthType() AuthType
	// Authenticate performs authentication and returns token or credentials
	Authenticate(ctx context.Context) (string, error)
	// GetAuthHeader gets the authentication header key and value
	GetAuthHeader() (string, string)
	// Validate validates the authentication configuration
	Validate() error
}

// TokenAuth implements token-based authentication
type TokenAuth struct {
	Token string
}

func NewTokenAuth(token string) *TokenAuth {
	return &TokenAuth{Token: token}
}

func (a *TokenAuth) GetAuthType() AuthType {
	return AuthTypeToken
}

func (a *TokenAuth) Authenticate(ctx context.Context) (string, error) {
	if a.Token == "" {
		return "", errors.New("token cannot be empty")
	}
	return a.Token, nil
}

func (a *TokenAuth) GetAuthHeader() (string, string) {
	return "Authorization", "Bearer " + a.Token
}

func (a *TokenAuth) Validate() error {
	if a.Token == "" {
		return errors.New("token is required")
	}
	return nil
}

// BearerAuth implements bearer token authentication
type BearerAuth struct {
	Token string
}

func NewBearerAuth(token string) *BearerAuth {
	return &BearerAuth{Token: token}
}

func (a *BearerAuth) GetAuthType() AuthType {
	return AuthTypeBearer
}

func (a *BearerAuth) Authenticate(ctx context.Context) (string, error) {
	if a.Token == "" {
		return "", errors.New("bearer token cannot be empty")
	}
	return a.Token, nil
}

func (a *BearerAuth) GetAuthHeader() (string, string) {
	return "Authorization", "Bearer " + a.Token
}

func (a *BearerAuth) Validate() error {
	if a.Token == "" {
		return errors.New("bearer token is required")
	}
	return nil
}

// APIKeyAuth API Key 认证
type APIKeyAuth struct {
	APIKey     string
	HeaderName string // 默认为 "X-API-Key"
	QueryParam string // 可选：作为查询参数传递
}

func NewAPIKeyAuth(apiKey, headerName string) *APIKeyAuth {
	if headerName == "" {
		headerName = "X-API-Key"
	}
	return &APIKeyAuth{
		APIKey:     apiKey,
		HeaderName: headerName,
	}
}

func (a *APIKeyAuth) GetAuthType() AuthType {
	return AuthTypeAPIKey
}

func (a *APIKeyAuth) Authenticate(ctx context.Context) (string, error) {
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

// BasicAuth implements basic authentication
type BasicAuth struct {
	Username string
	Password string
}

func NewBasicAuth(username, password string) *BasicAuth {
	return &BasicAuth{
		Username: username,
		Password: password,
	}
}

func (a *BasicAuth) GetAuthType() AuthType {
	return AuthTypeBasic
}

func (a *BasicAuth) Authenticate(ctx context.Context) (string, error) {
	if a.Username == "" || a.Password == "" {
		return "", errors.New("username and password cannot be empty")
	}
	return "", nil // Basic Auth 不需要返回 token
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

// OAuth2Auth implements OAuth2 authentication
type OAuth2Auth struct {
	ClientID     string
	ClientSecret string
	TokenURL     string
	AccessToken  string // obtained access token
	RefreshToken string // refresh token (optional)
}

func NewOAuth2Auth(clientID, clientSecret, tokenURL string) *OAuth2Auth {
	return &OAuth2Auth{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     tokenURL,
	}
}

func (a *OAuth2Auth) GetAuthType() AuthType {
	return AuthTypeOAuth2
}

func (a *OAuth2Auth) Authenticate(ctx context.Context) (string, error) {
	// Return the access token if already available
	if a.AccessToken != "" {
		return a.AccessToken, nil
	}

	// Otherwise need to acquire token from TokenURL
	// Simplified implementation, actual OAuth2 flow should be used
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

// SetAccessToken sets the access token
func (a *OAuth2Auth) SetAccessToken(token string) {
	a.AccessToken = token
}
