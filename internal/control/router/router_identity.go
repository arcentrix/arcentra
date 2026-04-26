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

package router

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) identityRouter(r fiber.Router, auth fiber.Handler) {
	identityGroup := r.Group("/identity")
	{
		// Provider resource management (authentication required)
		identityGroup.Get(
			"/providers",
			auth,
			rt.listProviders,
		) // GET /identity/providers - list all providers (supports ?type=xxx filter)
		identityGroup.Post("/providers", auth, rt.createProvider)         // POST /identity/providers - create provider
		identityGroup.Get("/providers/types", auth, rt.listProviderTypes) // GET /identity/providers/types - list all provider types
		identityGroup.Get("/providers/:name", auth, rt.getProvider)       // GET /identity/providers/:name - get specific provider
		identityGroup.Put("/providers/:name", auth, rt.updateProvider)    // PUT /identity/providers/:name - update provider
		identityGroup.Put(
			"/providers/:name/toggle",
			auth,
			rt.toggleProvider,
		) // PUT /identity/providers/:name/toggle - toggle enabled status
		identityGroup.Delete("/providers/:name", auth, rt.deleteProvider) // DELETE /identity/providers/:name - delete provider

		// Authentication flow (no authentication required)
		identityGroup.Get("/login/providers", rt.listLoginProviders) // GET /identity/login/providers - list providers visible on the login page
		identityGroup.Get("/authorize/:provider", rt.authorize)      // GET /identity/authorize/:provider - initiate authorization (OAuth/OIDC)
		identityGroup.Get("/callback/:provider", rt.callback)        // GET /identity/callback/:provider - authorization callback
		identityGroup.Post("/ldap/login/:provider", rt.ldapLogin)    // POST /identity/ldap/login/:provider - LDAP login
	}
}

// listLoginProviders lists enabled identity providers exposed to the login page (no auth required).
func (rt *Router) listLoginProviders(c *fiber.Ctx) error {
	providers, err := rt.Services.Identity.ListPublicLoginProviders(c.Context())
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, providers)
}

// authorize initiates authorization (OAuth/OIDC)
func (rt *Router) authorize(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	providerName := c.Params("provider")
	if providerName == "" {
		return http.Err(c, http.ProviderIsRequired.Code, http.ProviderIsRequired.Msg)
	}

	authorize, err := identityService.Authorize(c.Context(), providerName)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return c.Redirect(authorize, fiber.StatusTemporaryRedirect)
}

// callback handles OAuth/OIDC authorization callback
func (rt *Router) callback(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	providerName := c.Params("provider")
	// Get raw state parameter and ensure it's properly decoded
	stateRaw := c.Query("state")
	codeRaw := c.Query("code")

	if stateRaw == "" || codeRaw == "" || providerName == "" {
		return http.Err(c, http.InvalidStatusParameter.Code, http.InvalidStatusParameter.Msg)
	}

	// Ensure URL decoding (Fiber should do this automatically, but we ensure it)
	state, err := url.QueryUnescape(stateRaw)
	if err != nil {
		log.Warnw("failed to decode state parameter", "stateRaw", stateRaw, "error", err)
		state = stateRaw // fallback to raw value
	}

	code, err := url.QueryUnescape(codeRaw)
	if err != nil {
		log.Warnw("failed to decode code parameter", "codeRaw", codeRaw, "error", err)
		code = codeRaw // fallback to raw value
	}

	userInfo, _, err := identityService.Callback(c.Context(), providerName, state, code)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	// 自动登录：使用 Login 方法（密码为空时跳过密码验证）
	userService := rt.Services.User
	loginReq := &model.Login{
		Username: userInfo.Username,
		Email:    userInfo.Email,
		Password: "", // OAuth 登录不需要密码
	}
	loginResp, err := userService.Login(loginReq, rt.HTTP.Auth)
	if err != nil {
		log.Errorw("OAuth auto login failed", "provider", providerName, "userId", userInfo.UserID, "error", err)
		return http.Err(c, http.Failed.Code, fmt.Sprintf("OAuth login failed: %v", err))
	}

	// 解析 expireAt 以设置 cookie 过期时间（expireAt 是 Unix 时间戳字符串）
	var expireAt time.Time
	if expireAtUnix, err := strconv.ParseInt(loginResp.Token["expireAt"], 10, 64); err == nil {
		expireAt = time.Unix(expireAtUnix, 0)
	} else {
		// 如果解析失败，使用默认过期时间（AccessExpire 分钟）
		expireAt = time.Now().Add(rt.HTTP.Auth.AccessExpire)
	}

	// 从数据库获取 cookie path，如果获取失败则使用默认值 "/"
	cookiePath := rt.getCookiePath()

	// 设置 accessToken cookie（HTTP-only）
	// 注意：在 302 重定向时，cookie 会随响应头一起发送
	c.Cookie(&fiber.Cookie{
		Name:     "accessToken",
		Value:    loginResp.Token["accessToken"],
		Path:     cookiePath,
		Expires:  expireAt,
		HTTPOnly: true,
		Secure:   false, // 在生产环境应设置为 true（HTTPS）
		SameSite: fiber.CookieSameSiteLaxMode,
	})

	// 设置 refreshToken cookie（HTTP-only）
	refreshExpireAt := time.Now().Add(rt.HTTP.Auth.RefreshExpire)
	c.Cookie(&fiber.Cookie{
		Name:     "refreshToken",
		Value:    loginResp.Token["refreshToken"],
		Path:     cookiePath,
		Expires:  refreshExpireAt,
		HTTPOnly: true,
		Secure:   false, // 在生产环境应设置为 true（HTTPS）
		SameSite: fiber.CookieSameSiteLaxMode,
	})

	// 在 Fiber 中，c.Cookie() 会立即将 cookie 添加到响应头
	// c.Redirect() 会发送 302 响应，cookie 会随响应头一起发送
	// 使用 StatusFound (302) 进行临时重定向
	return c.Redirect(rt.getBaseURL(), fiber.StatusFound)
}

// listProviders lists all providers (supports ?type=xxx filter)
func (rt *Router) listProviders(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	// support filtering by type through query parameter
	providerType := c.Query("type")

	var err error
	var integrations any

	if providerType != "" {
		integrations, err = identityService.GetProviderByType(c.Context(), providerType)
	} else {
		integrations, err = identityService.GetProviderList(c.Context())
	}

	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	// build response without timestamps
	type ProviderResponse struct {
		ProviderID   string `json:"providerId"`
		Name         string `json:"name"`
		ProviderType string `json:"providerType"`
		Description  string `json:"description"`
		Priority     int    `json:"priority"`
		IsEnabled    int    `json:"isEnabled"`
	}

	var response []ProviderResponse
	switch v := integrations.(type) {
	case []model.Identity:
		for _, integration := range v {
			response = append(response, ProviderResponse{
				ProviderID:   integration.ProviderID,
				Name:         integration.Name,
				ProviderType: integration.ProviderType,
				Description:  integration.Description,
				Priority:     integration.Priority,
				IsEnabled:    integration.IsEnabled,
			})
		}
	}

	return http.Detail(c, response)
}

// getProvider gets a specific provider
func (rt *Router) getProvider(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	name := c.Params("name")
	if name == "" {
		return http.Err(c, http.ProviderIsRequired.Code, http.ProviderIsRequired.Msg)
	}

	provider, err := identityService.GetProvider(c.Context(), name)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, provider)
}

// listProviderTypes lists all provider types
func (rt *Router) listProviderTypes(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	providerTypes, err := identityService.GetProviderTypeList(c.Context())
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, providerTypes)
}

// ldapLogin handles LDAP login (authentication methods requiring username and password)
func (rt *Router) ldapLogin(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	providerName := c.Params("provider")
	if providerName == "" {
		return http.Err(c, http.ProviderIsRequired.Code, http.ProviderIsRequired.Msg)
	}

	var req service.LDAPLoginRequest
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, http.BadRequest.Msg)
	}

	if req.Username == "" || req.Password == "" {
		return http.Err(c, http.UsernameArePasswordIsRequired.Code, http.UsernameArePasswordIsRequired.Msg)
	}

	// Step 1: Verify LDAP identity and map/create Arcentra user
	userInfo, err := identityService.LDAPLogin(c.Context(), providerName, req.Username, req.Password)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	// Step 2 & 3: Generate Arcentra token using Login method (password empty for LDAP)
	// This follows the unified flow: verify identity → map/create user → generate Arcentra token
	userService := rt.Services.User
	loginReq := &model.Login{
		Username: userInfo.Username,
		Email:    userInfo.Email,
		Password: "", // LDAP login: password already verified, use empty for token generation
	}
	loginResp, err := userService.Login(loginReq, rt.HTTP.Auth)
	if err != nil {
		log.Errorw("LDAP auto login failed", "provider", providerName, "userId", userInfo.UserID, "error", err)
		return http.Err(c, http.Failed.Code, fmt.Sprintf("failed to generate token: %v", err))
	}

	// Step 4: Return LoginResp with Arcentra token (subsequent requests only use Arcentra token)
	return http.Detail(c, loginResp)
}

// createProvider creates an identity provider
func (rt *Router) createProvider(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	var provider model.Identity
	if err := c.BodyParser(&provider); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request parameters")
	}

	// validate required fields
	if provider.Name == "" || provider.ProviderType == "" {
		return http.Err(c, http.BadRequest.Code, "name and providerType are required fields")
	}

	if err := identityService.CreateProvider(c.Context(), &provider); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, provider)
}

// updateProvider updates an identity provider
func (rt *Router) updateProvider(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	name := c.Params("name")
	if name == "" {
		return http.Err(c, http.ProviderIsRequired.Code, http.ProviderIsRequired.Msg)
	}

	var provider model.Identity
	if err := c.BodyParser(&provider); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request parameters")
	}

	if err := identityService.UpdateProvider(c.Context(), name, &provider); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

// toggleProvider toggles the enabled status of an identity provider
func (rt *Router) toggleProvider(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	name := c.Params("name")
	if name == "" {
		return http.Err(c, http.ProviderIsRequired.Code, http.ProviderIsRequired.Msg)
	}

	if err := identityService.ToggleProvider(c.Context(), name); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

// deleteProvider deletes an identity provider
func (rt *Router) deleteProvider(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	name := c.Params("name")
	if name == "" {
		return http.Err(c, http.ProviderIsRequired.Code, http.ProviderIsRequired.Msg)
	}

	if err := identityService.DeleteProvider(c.Context(), name); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

// getCookiePath gets cookie path from database configuration.
// Returns the configured cookie path, or "/" as default if not found or error occurs.
func (rt *Router) getCookiePath() string {
	const (
		defaultCookiePath = "/"
		name              = "base_path"
	)

	setting, err := rt.Services.Setting.GetSetting(context.Background(), name)
	if err != nil {
		log.Debugw("failed to get cookie path from database, using default", "name", name, "error", err)
		return defaultCookiePath
	}

	if len(setting.Value) == 0 {
		log.Debugw("cookie path configuration data is empty, using default", "name", name)
		return defaultCookiePath
	}

	var configData map[string]any
	if err = sonic.Unmarshal(setting.Value, &configData); err != nil {
		log.Warnw("failed to unmarshal cookie path configuration, using default", "name", name, "error", err)
		return defaultCookiePath
	}

	basePathValue, ok := configData["base_path"]
	if !ok {
		log.Debugw("base_path not found in configuration data, using default", "name", name)
		return defaultCookiePath
	}

	basePathStr, ok := basePathValue.(string)
	if !ok || basePathStr == "" {
		log.Debugw("base_path is not a valid string, using default", "name", name)
		return defaultCookiePath
	}

	parsedURL, err := url.Parse(basePathStr)
	if err != nil {
		log.Warnw("failed to parse base_path as URL, using default", "base_path", basePathStr, "error", err)
		return defaultCookiePath
	}

	cookiePath := parsedURL.Path
	if cookiePath == "" {
		cookiePath = defaultCookiePath
	}

	log.Debugw("cookie path extracted from base_path", "base_path", basePathStr, "cookie_path", cookiePath)
	return cookiePath
}

// getBaseURL gets frontend base URL from database configuration.
// Returns the configured frontend URL, or "/" as default if not found or error occurs.
func (rt *Router) getBaseURL() string {
	const (
		defaultBaseURL = "/"
		name           = "base_path"
	)

	setting, err := rt.Services.Setting.GetSetting(context.Background(), name)
	if err != nil {
		log.Debugw("failed to get frontend base URL from database, using default", "name", name, "error", err)
		return defaultBaseURL
	}

	if len(setting.Value) == 0 {
		log.Debugw("frontend base URL configuration data is empty, using default", "name", name)
		return defaultBaseURL
	}

	var configData map[string]any
	if err = sonic.Unmarshal(setting.Value, &configData); err != nil {
		log.Warnw("failed to unmarshal frontend base URL configuration, using default", "name", name, "error", err)
		return defaultBaseURL
	}

	basePathValue, ok := configData["base_path"]
	if !ok {
		log.Debugw("base_path not found in configuration data, using default", "name", name)
		return defaultBaseURL
	}

	basePathStr, ok := basePathValue.(string)
	if !ok || basePathStr == "" {
		log.Debugw("base_path is not a valid string, using default", "name", name)
		return defaultBaseURL
	}

	parsedURL, err := url.Parse(basePathStr)
	if err != nil {
		log.Warnw("failed to parse base_path as URL, using default", "base_path", basePathStr, "error", err)
		return defaultBaseURL
	}

	frontendURL := fmt.Sprintf("%s://%s%s", parsedURL.Scheme, parsedURL.Host, parsedURL.Path)
	if parsedURL.Path == "" {
		frontendURL = fmt.Sprintf("%s://%s/", parsedURL.Scheme, parsedURL.Host)
	}

	return frontendURL
}
