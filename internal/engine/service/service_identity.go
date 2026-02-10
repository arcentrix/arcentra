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

package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/sso"
	"github.com/arcentrix/arcentra/pkg/sso/ldap"
	"github.com/arcentrix/arcentra/pkg/sso/oidc"
	"github.com/arcentrix/arcentra/pkg/sso/util"
	"github.com/bytedance/sonic"
	"golang.org/x/oauth2"
)

type IdentityService struct {
	identityRepo repo.IIdentityRepository
	userRepo     repo.IUserRepository
	userExtRepo  repo.IUserExtRepository
}

func NewIdentityService(identityRepo repo.IIdentityRepository, userRepo repo.IUserRepository, userExtRepo repo.IUserExtRepository) *IdentityService {
	return &IdentityService{
		identityRepo: identityRepo,
		userRepo:     userRepo,
		userExtRepo:  userExtRepo,
	}
}

func (iis *IdentityService) Authorize(providerName string) (string, error) {
	integration, err := iis.identityRepo.GetProvider(providerName)
	if err != nil {
		log.Errorw("failed to get oauth provider", "provider", providerName, "error", err)
		return "", err
	}

	log.Debugw("OAuth authorize", "urlProvider", providerName, "dbProviderName", integration.Name, "providerType", integration.ProviderType)

	cfg, err := iis.convertToProviderConfig(integration)
	if err != nil {
		return "", fmt.Errorf("convert provider config failed: %w", err)
	}

	provider, err := sso.NewSSOProvider(cfg)
	if err != nil {
		log.Errorw("failed to create SSO provider", "provider", providerName, "error", err)
		return "", fmt.Errorf("failed to create SSO provider: %w", err)
	}

	state := util.GenState()
	// Store provider name and redirectURI in state
	util.StoreState(state, integration.Name)
	log.Debugw("State stored", "state", state, "storedProviderName", integration.Name, "urlProvider", providerName)

	switch integration.ProviderType {
	case "oauth":
		return provider.AuthCodeURL(state), nil
	case "oidc":
		return provider.AuthCodeURL(state), nil
	default:
		return "", fmt.Errorf("invalid provider type: %s", integration.ProviderType)
	}
}

// Callback 统一的 OAuth/OIDC 回调处理（根据 provider 类型自动识别）
func (iis *IdentityService) Callback(providerName, state, code string) (*model.Register, string, error) {
	// Check state before loading (for debugging)
	if stateData, exists := util.CheckState(state); exists {
		log.Debugw("state found in store", "provider", providerName, "storedProviderName", stateData.ProviderName, "state", state)
	}

	stateData, ok := util.LoadAndDeleteState(state)
	if !ok {
		log.Warnw("state not found in store", "provider", providerName, "state", state, "stateLength", len(state))
		// Log all states in store for debugging (be careful in production)
		util.StateStore.Range(func(key, value interface{}) bool {
			log.Debugw("state in store", "key", key, "value", value)
			return true
		})
		return nil, "", fmt.Errorf("invalid state parameter: state not found or expired")
	}

	log.Debugw("State loaded", "provider", providerName, "storedProviderName", stateData.ProviderName, "redirectURI", stateData.RedirectURI, "redirectURILength", len(stateData.RedirectURI))

	// Get the actual provider from database to compare with stored name
	integration, err := iis.identityRepo.GetProvider(providerName)
	if err != nil {
		log.Errorw("failed to get provider in callback", "provider", providerName, "error", err)
		return nil, "", fmt.Errorf("load provider failed: %w", err)
	}

	// Compare stored provider name with database provider name
	// This handles cases where URL parameter might differ from database name
	if stateData.ProviderName != integration.Name {
		log.Warnw("state provider mismatch", "urlProvider", providerName, "storedProviderName", stateData.ProviderName, "dbProviderName", integration.Name, "state", state)
		return nil, "", fmt.Errorf("invalid state parameter: provider mismatch (stored: %s, url: %s, db: %s)", stateData.ProviderName, providerName, integration.Name)
	}

	// Use the actual provider name from database for consistency
	actualProviderName := integration.Name

	cfg, err := iis.convertToProviderConfig(integration)
	if err != nil {
		log.Errorw("invalid provider config", "provider", providerName, "error", err)
		return nil, "", fmt.Errorf("provider config invalid: %w", err)
	}

	provider, err := sso.NewSSOProvider(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("create provider failed: %w", err)
	}

	token, err := provider.ExchangeToken(context.Background(), code)
	if err != nil {
		return nil, "", fmt.Errorf("token exchange failed: %w", err)
	}

	userInfo, err := provider.GetUserInfo(context.Background(), token)
	if err != nil {
		return nil, "", fmt.Errorf("get user info failed: %w", err)
	}

	// Get CoverAttributes configuration
	var coverAttributes bool
	switch integration.ProviderType {
	case "oauth":
		var oauthCfg model.OAuthConfig
		if err := sonic.Unmarshal(integration.Config, &oauthCfg); err == nil {
			coverAttributes = oauthCfg.CoverAttributes
		}
	case "oidc":
		var oidcCfg model.OIDCConfig
		if err := sonic.Unmarshal(integration.Config, &oidcCfg); err == nil {
			coverAttributes = oidcCfg.CoverAttributes
		}
	}

	registerInfo, err := iis.registerOrLoginUser(actualProviderName, &sso.UserInfoAdapter{
		Username:  userInfo.Username,
		Email:     userInfo.Email,
		Name:      userInfo.Name,
		Nickname:  userInfo.Nickname,
		AvatarURL: userInfo.AvatarURL,
	}, coverAttributes)
	if err != nil {
		return nil, "", err
	}

	return registerInfo, stateData.RedirectURI, nil
}

// splitName splits a full name into first and last name
func splitName(name, nickname string) string {
	// use name first, fallback to nickname
	fullName := name
	if fullName == "" {
		fullName = nickname
	}

	if fullName == "" {
		return ""
	}

	// split by space
	parts := strings.Fields(fullName)
	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, " ")
}

// convertToProviderConfig converts database config to sso.ProviderConfig
func (iis *IdentityService) convertToProviderConfig(integration *model.Identity) (*sso.ProviderConfig, error) {
	cfg := &sso.ProviderConfig{
		Type: integration.ProviderType,
	}

	switch integration.ProviderType {
	case "oauth":
		var oauthCfg model.OAuthConfig
		if err := sonic.Unmarshal(integration.Config, &oauthCfg); err != nil {
			return nil, fmt.Errorf("unmarshal oauth config failed: %w", err)
		}

		cfg.ClientID = oauthCfg.ClientID
		cfg.ClientSecret = oauthCfg.ClientSecret
		cfg.RedirectURL = oauthCfg.RedirectURL
		cfg.Scopes = oauthCfg.Scopes
		cfg.UserInfoURL = oauthCfg.UserInfoURL
		cfg.FieldMap = oauthCfg.Mapping

		// Convert AuthURL and TokenURL to Endpoint
		if oauthCfg.Endpoint.AuthURL != "" && oauthCfg.Endpoint.TokenURL != "" {
			cfg.Endpoint = oauthCfg.Endpoint
		} else if oauthCfg.AuthURL != "" && oauthCfg.TokenURL != "" {
			cfg.Endpoint = oauth2.Endpoint{
				AuthURL:  oauthCfg.AuthURL,
				TokenURL: oauthCfg.TokenURL,
			}
		} else {
			return nil, fmt.Errorf("missing authURL or tokenURL in oauth config")
		}

	case "oidc":
		var oidcCfg model.OIDCConfig
		if err := sonic.Unmarshal(integration.Config, &oidcCfg); err != nil {
			return nil, fmt.Errorf("unmarshal oidc config failed: %w", err)
		}

		cfg.Issuer = oidcCfg.Issuer
		cfg.ClientID = oidcCfg.ClientID
		cfg.ClientSecret = oidcCfg.ClientSecret
		cfg.RedirectURL = oidcCfg.RedirectURL
		cfg.Scopes = oidcCfg.Scopes
		cfg.SkipVerify = oidcCfg.SkipVerify
		cfg.FieldMap = oidcCfg.Mapping

	default:
		return nil, fmt.Errorf("unsupported provider type: %s", integration.ProviderType)
	}

	return cfg, nil
}

func (iis *IdentityService) GetProviderByType(providerType string) ([]model.Identity, error) {
	integrations, err := iis.identityRepo.GetProviderByType(providerType)
	if err != nil {
		log.Errorw("failed to get provider by type", "providerType", providerType, "error", err)
		return nil, err
	}
	return integrations, nil
}

func (iis *IdentityService) GetProvider(name string) (*model.Identity, error) {
	integration, err := iis.identityRepo.GetProvider(name)
	if err != nil {
		log.Errorw("failed to get provider", "name", name, "error", err)
		return nil, err
	}
	return integration, nil
}

// GetProviderList 获取提供者列表
func (iis *IdentityService) GetProviderList() ([]model.Identity, error) {
	integrations, err := iis.identityRepo.GetProviderList()
	if err != nil {
		log.Errorw("failed to get provider list", "error", err)
		return nil, err
	}
	return integrations, nil
}

func (iis *IdentityService) GetProviderTypeList() ([]string, error) {
	providerTypes, err := iis.identityRepo.GetProviderTypeList()
	if err != nil {
		log.Errorw("failed to get provider type list", "error", err)
		return nil, err
	}
	return providerTypes, nil
}

// OIDCLogin OIDC 登录
func (iis *IdentityService) OIDCLogin(providerName string) (string, error) {
	integration, err := iis.identityRepo.GetProvider(providerName)
	if err != nil {
		log.Errorw("failed to get OIDC provider", "provider", providerName, "error", err)
		return "", err
	}

	var oidcCfg model.OIDCConfig
	if err := sonic.Unmarshal(integration.Config, &oidcCfg); err != nil {
		return "", fmt.Errorf("unmarshal provider config failed: %w", err)
	}

	// 创建 OIDC provider
	provider, err := oidc.NewOIDCProvider(
		oidcCfg.Issuer,
		oidcCfg.ClientID,
		oidcCfg.ClientSecret,
		oidcCfg.RedirectURL,
		oidcCfg.Scopes,
		oidcCfg.SkipVerify,
	)
	if err != nil {
		log.Errorw("failed to create OIDC provider", "provider", providerName, "error", err)
		return "", err
	}

	state := util.GenState()
	util.StateStore.Store(state, providerName)

	return provider.GetAuthURL(state), nil
}

// LDAP 相关方法

type LDAPLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LDAPLogin LDAP 登录
func (iis *IdentityService) LDAPLogin(providerName, username, password string) (*model.Register, error) {
	integration, err := iis.identityRepo.GetProvider(providerName)
	if err != nil {
		log.Errorw("failed to get LDAP provider", "provider", providerName, "error", err)
		return nil, err
	}

	var ldapCfg model.LDAPConfig
	if err := sonic.Unmarshal(integration.Config, &ldapCfg); err != nil {
		return nil, fmt.Errorf("unmarshal provider config failed: %w", err)
	}

	// 创建 LDAP 客户端
	ldapClient := ldap.NewLDAPClient(
		ldapCfg.Host,
		ldapCfg.Port,
		ldapCfg.UseTLS,
		ldapCfg.SkipVerify,
		ldapCfg.BaseDN,
		ldapCfg.BindDN,
		ldapCfg.BindPassword,
	)

	// 认证用户
	userInfo, err := ldapClient.Authenticate(username, password)
	if err != nil {
		log.Errorw("LDAP authentication failed", "provider", providerName, "username", username, "error", err)
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	// Convert LDAP attributes to map[string]any for field mapping
	rawData := make(map[string]any)
	for key, values := range userInfo.Attributes {
		if len(values) > 0 {
			rawData[key] = values[0]
		}
	}
	// Add standard fields
	rawData["username"] = userInfo.Username
	rawData["email"] = userInfo.Email
	rawData["displayName"] = userInfo.DisplayName
	rawData["dn"] = userInfo.DN

	// Apply field mapping if configured
	userInfoAdapter := applyLDAPFieldMapping(rawData, ldapCfg.Mapping, userInfo)

	// use common user registration logic
	return iis.registerOrLoginUser(providerName, userInfoAdapter, ldapCfg.CoverAttributes)
}

// applyLDAPFieldMapping applies field mapping rules for LDAP user information.
// It converts LDAP attributes to UserInfoAdapter using the provided mapping configuration.
func applyLDAPFieldMapping(
	rawData map[string]any,
	fieldMap map[string]string,
	ldapUserInfo *ldap.UserInfo,
) *sso.UserInfoAdapter {
	// Use the shared field mapping function
	result := sso.ApplyFieldMappingFromRaw(rawData, fieldMap)

	// Apply LDAP-specific fallback values for missing fields
	if result.Username == "" {
		result.Username = ldapUserInfo.Username
	}
	if result.Email == "" {
		result.Email = ldapUserInfo.Email
	}
	if result.Name == "" {
		result.Name = ldapUserInfo.DisplayName
	}
	if result.Nickname == "" {
		result.Nickname = ldapUserInfo.DisplayName
	}
	if result.ID == "" {
		result.ID = ldapUserInfo.DN
	}

	return result
}

// registerOrLoginUser handles user registration or login logic according to the SSO flow.
// It checks if user exists, and either creates a new user or updates existing user based on CoverAttributes configuration.
func (iis *IdentityService) registerOrLoginUser(providerName string, userInfo *sso.UserInfoAdapter, coverAttributes bool) (*model.Register, error) {
	// Check if user exists by username
	userId, err := iis.userRepo.GetUserByUsername(userInfo.Username)
	if err == nil && userId != "" {
		// User exists - check CoverAttributes configuration
		log.Debugw("user already exists", "provider", providerName, "username", userInfo.Username, "coverAttributes", coverAttributes)

		existingUser, err := iis.userRepo.GetUserByUserId(userId)
		if err != nil {
			log.Errorw("failed to get existing user details", "provider", providerName, "userId", userId, "error", err)
			return nil, fmt.Errorf("user exists but failed to get user details: %w", err)
		}

		// Update SSO fields if CoverAttributes is enabled
		if coverAttributes {
			log.Debugw("updating SSO fields for existing user", "provider", providerName, "userId", userId)
			if err := iis.updateSsoFields(userId, userInfo); err != nil {
				log.Errorw("failed to update SSO fields", "provider", providerName, "userId", userId, "error", err)
				// Continue with login even if update fails
			} else {
				// Fetch updated user info after successful update
				existingUser, err = iis.userRepo.GetUserByUserId(userId)
				if err != nil {
					log.Errorw("failed to get updated user details", "provider", providerName, "userId", userId, "error", err)
				}
			}
		} else {
			log.Debugw("keeping original fields for existing user", "provider", providerName, "userId", userId)
		}

		// Return existing user info for login
		return &model.Register{
			UserId:    existingUser.UserId,
			Username:  existingUser.Username,
			FullName:  existingUser.FullName,
			Avatar:    existingUser.Avatar,
			Email:     existingUser.Email,
			Password:  existingUser.Password,
			CreatedAt: existingUser.CreatedAt,
		}, nil
	}

	// User does not exist (err != nil means user not found) - create new user with full SSO fields
	log.Debugw("creating new user", "provider", providerName, "username", userInfo.Username)
	return iis.createUserWithFullSsoFields(providerName, userInfo)
}

// updateSsoFields updates SSO-related fields for an existing user.
func (iis *IdentityService) updateSsoFields(userId string, userInfo *sso.UserInfoAdapter) error {
	fullName := splitName(userInfo.Name, userInfo.Nickname)
	if fullName == "" {
		fullName = userInfo.Username
	}

	updates := make(map[string]any)
	if userInfo.Email != "" {
		updates["email"] = userInfo.Email
	}
	if fullName != "" {
		updates["full_name"] = fullName
	}
	if userInfo.AvatarURL != "" {
		updates["avatar"] = userInfo.AvatarURL
	}

	if len(updates) == 0 {
		return nil
	}

	updates["updated_at"] = time.Now()
	return iis.userRepo.UpdateUser(userId, updates)
}

// createUserWithFullSsoFields creates a new user with complete SSO fields.
func (iis *IdentityService) createUserWithFullSsoFields(providerName string, userInfo *sso.UserInfoAdapter) (*model.Register, error) {
	fullName := splitName(userInfo.Name, userInfo.Nickname)

	registerUserInfo := &model.Register{
		UserId:   id.GetUUIDWithoutDashes(),
		Username: userInfo.Username,
		FullName: fullName,
		Avatar:   userInfo.AvatarURL,
		Email:    userInfo.Email,
	}

	// Generate default email if missing
	if registerUserInfo.Email == "" {
		registerUserInfo.Email = fmt.Sprintf("%s@%s.com", userInfo.Username, providerName)
	}

	// Use username as full name if missing
	if registerUserInfo.FullName == "" {
		registerUserInfo.FullName = userInfo.Username
	}

	registerUserInfo.CreatedAt = time.Now()
	password, err := getPassword(userInfo.Username)
	if err != nil {
		return nil, err
	}
	registerUserInfo.Password = string(password)

	err = iis.userRepo.Register(registerUserInfo)
	if err != nil {
		log.Errorw("failed to register user", "provider", providerName, "username", userInfo.Username, "error", err)
		return nil, err
	}

	// Create UserExt record
	err = iis.createUserExtIfNotExists(registerUserInfo.UserId)
	if err != nil {
		log.Errorw("failed to create user ext after registration", "provider", providerName, "username", userInfo.Username, "userId", registerUserInfo.UserId, "error", err)
		// Don't return error, as user creation succeeded
	}

	return registerUserInfo, nil
}

// createUserExtIfNotExists creates UserExt record if it doesn't exist
// This replaces the database trigger that was inserting into t_user_extension
func (iis *IdentityService) createUserExtIfNotExists(userId string) error {
	exists, err := iis.userExtRepo.Exists(userId)
	if err != nil {
		return fmt.Errorf("failed to check user ext exists: %w", err)
	}
	if exists {
		return nil // already exists, no need to create
	}

	// Create UserExt record with default values (matching the database trigger)
	now := time.Now()
	userExt := &model.UserExt{
		UserId:           userId,
		Timezone:         "UTC",
		InvitationStatus: model.UserInvitationStatusAccepted,
	}
	userExt.CreatedAt = now
	userExt.UpdatedAt = now

	if err := iis.userExtRepo.Create(userExt); err != nil {
		return fmt.Errorf("failed to create user ext: %w", err)
	}

	return nil
}

// CreateProvider creates an identity provider
func (iis *IdentityService) CreateProvider(provider *model.Identity) error {
	// check if provider name already exists
	exists, err := iis.identityRepo.ProviderExists(provider.Name)
	if err != nil {
		log.Errorw("failed to check provider exists", "name", provider.Name, "error", err)
		return err
	}
	if exists {
		return fmt.Errorf("provider name already exists: %s", provider.Name)
	}

	// generate provider ID
	provider.ProviderId = id.GetUUID()

	if err := iis.identityRepo.CreateProvider(provider); err != nil {
		log.Errorw("failed to create provider", "name", provider.Name, "error", err)
		return err
	}

	return nil
}

// UpdateProvider updates an identity provider
func (iis *IdentityService) UpdateProvider(name string, provider *model.Identity) error {
	// check if provider exists
	existing, err := iis.identityRepo.GetProvider(name)
	if err != nil {
		log.Errorw("failed to get provider", "name", name, "error", err)
		return fmt.Errorf("provider not found: %s", name)
	}

	// preserve immutable configuration fields
	if err := iis.preserveConfigKeys(existing, provider); err != nil {
		return err
	}

	if err := iis.identityRepo.UpdateProvider(name, provider); err != nil {
		log.Errorw("failed to update provider", "name", name, "error", err)
		return err
	}

	return nil
}

// preserveConfigKeys ensures key fields in config cannot be updated
func (iis *IdentityService) preserveConfigKeys(existing, updated *model.Identity) error {
	// unmarshal existing config
	var existingConfig map[string]interface{}
	if err := sonic.Unmarshal(existing.Config, &existingConfig); err != nil {
		return fmt.Errorf("failed to unmarshal existing config: %w", err)
	}

	// unmarshal updated config
	var updatedConfig map[string]interface{}
	if err := sonic.Unmarshal(updated.Config, &updatedConfig); err != nil {
		return fmt.Errorf("failed to unmarshal updated config: %w", err)
	}

	// define immutable keys based on provider type
	immutableKeys := getImmutableConfigKeys(existing.ProviderType)

	// preserve immutable keys from existing config
	for _, key := range immutableKeys {
		if existingValue, exists := existingConfig[key]; exists {
			updatedConfig[key] = existingValue
		}
	}

	// marshal back to JSON
	newConfig, err := sonic.Marshal(updatedConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal updated config: %w", err)
	}

	updated.Config = newConfig
	return nil
}

// getImmutableConfigKeys returns list of immutable config keys for each provider type
func getImmutableConfigKeys(providerType string) []string {
	switch providerType {
	case "oauth":
		return []string{"clientId"}
	case "oidc":
		return []string{"clientId", "issuer"}
	case "ldap":
		return []string{"host", "port", "baseDN"}
	default:
		return []string{}
	}
}

// DeleteProvider deletes an identity provider
func (iis *IdentityService) DeleteProvider(name string) error {
	// check if provider exists
	exists, err := iis.identityRepo.ProviderExists(name)
	if err != nil {
		log.Errorw("failed to check provider exists", "name", name, "error", err)
		return err
	}
	if !exists {
		return fmt.Errorf("provider not found: %s", name)
	}

	if err := iis.identityRepo.DeleteProvider(name); err != nil {
		log.Errorw("failed to delete provider", "name", name, "error", err)
		return err
	}

	return nil
}

// ToggleProvider toggles the enabled status of an identity provider
func (iis *IdentityService) ToggleProvider(name string) error {
	// check if provider exists
	exists, err := iis.identityRepo.ProviderExists(name)
	if err != nil {
		log.Errorw("failed to check provider exists", "name", name, "error", err)
		return err
	}
	if !exists {
		return fmt.Errorf("provider not found: %s", name)
	}

	if err := iis.identityRepo.ToggleProvider(name); err != nil {
		log.Errorw("failed to toggle provider", "name", name, "error", err)
		return err
	}

	return nil
}
