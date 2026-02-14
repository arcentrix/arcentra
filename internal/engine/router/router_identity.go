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
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/internal/engine/service"
	httpx "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) identityRouter(r fiber.Router, auth fiber.Handler) {
	identityGroup := r.Group("/identity")
	{
		// Provider resource management (authentication required)
		identityGroup.Get("/providers", auth, rt.listProviders)               // GET /identity/providers - list all providers (supports ?type=xxx filter)
		identityGroup.Post("/providers", auth, rt.createProvider)             // POST /identity/providers - create provider
		identityGroup.Get("/providers/types", auth, rt.listProviderTypes)     // GET /identity/providers/types - list all provider types
		identityGroup.Get("/providers/:name", auth, rt.getProvider)           // GET /identity/providers/:name - get specific provider
		identityGroup.Put("/providers/:name", auth, rt.updateProvider)        // PUT /identity/providers/:name - update provider
		identityGroup.Put("/providers/:name/toggle", auth, rt.toggleProvider) // PUT /identity/providers/:name/toggle - toggle enabled status
		identityGroup.Delete("/providers/:name", auth, rt.deleteProvider)     // DELETE /identity/providers/:name - delete provider

		// Authentication flow (no authentication required)
		identityGroup.Get("/authorize/:provider", rt.authorize)   // GET /identity/authorize/:provider - initiate authorization (OAuth/OIDC)
		identityGroup.Get("/callback/:provider", rt.callback)     // GET /identity/callback/:provider - authorization callback
		identityGroup.Post("/ldap/login/:provider", rt.ldapLogin) // POST /identity/ldap/login/:provider - LDAP login
	}
}

// authorize initiates authorization (OAuth/OIDC)
func (rt *Router) authorize(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	providerName := c.Params("provider")
	if providerName == "" {
		return httpx.WithRepErrMsg(c, httpx.ProviderIsRequired.Code, httpx.ProviderIsRequired.Msg, c.Path())
	}

	url, err := identityService.Authorize(providerName)
	if err != nil {
		return httpx.WithRepErrMsg(c, httpx.Failed.Code, err.Error(), c.Path())
	}

	return c.Redirect(url, http.StatusTemporaryRedirect)
}

// callback handles OAuth/OIDC authorization callback
func (rt *Router) callback(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	providerName := c.Params("provider")
	// Get raw state parameter and ensure it's properly decoded
	stateRaw := c.Query("state")
	codeRaw := c.Query("code")

	if stateRaw == "" || codeRaw == "" || providerName == "" {
		return httpx.WithRepErrMsg(c, httpx.InvalidStatusParameter.Code, httpx.InvalidStatusParameter.Msg, c.Path())
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

	log.Debugw("OAuth callback received", "provider", providerName, "state", state, "codeLength", len(code))

	userInfo, _, err := identityService.Callback(providerName, state, code)
	if err != nil {
		return httpx.WithRepErrMsg(c, httpx.Failed.Code, err.Error(), c.Path())
	}

	// 自动登录：使用 Login 方法（密码为空时跳过密码验证）
	userService := rt.Services.User
	loginReq := &model.Login{
		Username: userInfo.Username,
		Email:    userInfo.Email,
		Password: "", // OAuth 登录不需要密码
	}
	loginResp, err := userService.Login(loginReq, rt.Http.Auth)
	if err != nil {
		log.Errorw("OAuth auto login failed", "provider", providerName, "userId", userInfo.UserId, "error", err)
		return httpx.WithRepErrMsg(c, httpx.Failed.Code, fmt.Sprintf("OAuth login failed: %v", err), c.Path())
	}

	// 解析 expireAt 以设置 cookie 过期时间（expireAt 是 Unix 时间戳字符串）
	var expireAt time.Time
	if expireAtUnix, err := strconv.ParseInt(loginResp.Token["expireAt"], 10, 64); err == nil {
		expireAt = time.Unix(expireAtUnix, 0)
	} else {
		// 如果解析失败，使用默认过期时间（AccessExpire 分钟）
		expireAt = time.Now().Add(rt.Http.Auth.AccessExpire)
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
	refreshExpireAt := time.Now().Add(rt.Http.Auth.RefreshExpire)
	c.Cookie(&fiber.Cookie{
		Name:     "refreshToken",
		Value:    loginResp.Token["refreshToken"],
		Path:     cookiePath,
		Expires:  refreshExpireAt,
		HTTPOnly: true,
		Secure:   false, // 在生产环境应设置为 true（HTTPS）
		SameSite: fiber.CookieSameSiteLaxMode,
	})

	// Get frontend base URL from database configuration for redirect
	baseURL := rt.getBaseURL()
	log.Debugw("OAuth callback success, auto login completed, cookies set, redirecting to frontend",
		"provider", providerName,
		"username", userInfo.Username,
		"userId", userInfo.UserId,
		"baseURL", baseURL,
		"cookiePath", cookiePath,
		"host", c.Hostname())

	// 在 Fiber 中，c.Cookie() 会立即将 cookie 添加到响应头
	// c.Redirect() 会发送 302 响应，cookie 会随响应头一起发送
	// 使用 StatusFound (302) 进行临时重定向
	return c.Redirect(baseURL, http.StatusFound)
}

// listProviders lists all providers (supports ?type=xxx filter)
func (rt *Router) listProviders(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	// support filtering by type through query parameter
	providerType := c.Query("type")

	var err error
	var integrations any

	if providerType != "" {
		integrations, err = identityService.GetProviderByType(providerType)
	} else {
		integrations, err = identityService.GetProviderList()
	}

	if err != nil {
		return httpx.WithRepErrMsg(c, httpx.Failed.Code, err.Error(), c.Path())
	}

	// build response without timestamps
	type ProviderResponse struct {
		ProviderId   string `json:"providerId"`
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
				ProviderId:   integration.ProviderId,
				Name:         integration.Name,
				ProviderType: integration.ProviderType,
				Description:  integration.Description,
				Priority:     integration.Priority,
				IsEnabled:    integration.IsEnabled,
			})
		}
	}

	c.Locals(middleware.DETAIL, response)
	return nil
}

// getProvider gets a specific provider
func (rt *Router) getProvider(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	name := c.Params("name")
	if name == "" {
		return httpx.WithRepErrMsg(c, httpx.ProviderIsRequired.Code, httpx.ProviderIsRequired.Msg, c.Path())
	}

	provider, err := identityService.GetProvider(name)
	if err != nil {
		return httpx.WithRepErrMsg(c, httpx.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, provider)
	return nil
}

// listProviderTypes lists all provider types
func (rt *Router) listProviderTypes(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	providerTypes, err := identityService.GetProviderTypeList()
	if err != nil {
		return httpx.WithRepErrMsg(c, httpx.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, providerTypes)
	return nil
}

// ldapLogin handles LDAP login (authentication methods requiring username and password)
func (rt *Router) ldapLogin(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	providerName := c.Params("provider")
	if providerName == "" {
		return httpx.WithRepErrMsg(c, httpx.ProviderIsRequired.Code, httpx.ProviderIsRequired.Msg, c.Path())
	}

	var req service.LDAPLoginRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.WithRepErrMsg(c, httpx.BadRequest.Code, httpx.BadRequest.Msg, c.Path())
	}

	if req.Username == "" || req.Password == "" {
		return httpx.WithRepErrMsg(c, httpx.UsernameArePasswordIsRequired.Code, httpx.UsernameArePasswordIsRequired.Msg, c.Path())
	}

	// Step 1: Verify LDAP identity and map/create Arcentra user
	userInfo, err := identityService.LDAPLogin(providerName, req.Username, req.Password)
	if err != nil {
		return httpx.WithRepErrMsg(c, httpx.Failed.Code, err.Error(), c.Path())
	}

	// Step 2 & 3: Generate Arcentra token using Login method (password empty for LDAP)
	// This follows the unified flow: verify identity → map/create user → generate Arcentra token
	userService := rt.Services.User
	loginReq := &model.Login{
		Username: userInfo.Username,
		Email:    userInfo.Email,
		Password: "", // LDAP login: password already verified, use empty for token generation
	}
	loginResp, err := userService.Login(loginReq, rt.Http.Auth)
	if err != nil {
		log.Errorw("LDAP auto login failed", "provider", providerName, "userId", userInfo.UserId, "error", err)
		return httpx.WithRepErrMsg(c, httpx.Failed.Code, fmt.Sprintf("failed to generate token: %v", err), c.Path())
	}

	// Step 4: Return LoginResp with Arcentra token (subsequent requests only use Arcentra token)
	c.Locals(middleware.DETAIL, loginResp)
	return nil
}

// createProvider creates an identity provider
func (rt *Router) createProvider(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	var provider model.Identity
	if err := c.BodyParser(&provider); err != nil {
		return httpx.WithRepErrMsg(c, httpx.BadRequest.Code, "invalid request parameters", c.Path())
	}

	// validate required fields
	if provider.Name == "" || provider.ProviderType == "" {
		return httpx.WithRepErrMsg(c, httpx.BadRequest.Code, "name and providerType are required fields", c.Path())
	}

	if err := identityService.CreateProvider(&provider); err != nil {
		return httpx.WithRepErrMsg(c, httpx.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, provider)
	c.Locals(middleware.OPERATION, "create identity provider")
	return nil
}

// updateProvider updates an identity provider
func (rt *Router) updateProvider(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	name := c.Params("name")
	if name == "" {
		return httpx.WithRepErrMsg(c, httpx.ProviderIsRequired.Code, httpx.ProviderIsRequired.Msg, c.Path())
	}

	var provider model.Identity
	if err := c.BodyParser(&provider); err != nil {
		return httpx.WithRepErrMsg(c, httpx.BadRequest.Code, "invalid request parameters", c.Path())
	}

	if err := identityService.UpdateProvider(name, &provider); err != nil {
		return httpx.WithRepErrMsg(c, httpx.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.OPERATION, "update identity provider")
	return nil
}

// toggleProvider toggles the enabled status of an identity provider
func (rt *Router) toggleProvider(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	name := c.Params("name")
	if name == "" {
		return httpx.WithRepErrMsg(c, httpx.ProviderIsRequired.Code, httpx.ProviderIsRequired.Msg, c.Path())
	}

	if err := identityService.ToggleProvider(name); err != nil {
		return httpx.WithRepErrMsg(c, httpx.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.OPERATION, "toggle identity provider status")
	return nil
}

// deleteProvider deletes an identity provider
func (rt *Router) deleteProvider(c *fiber.Ctx) error {
	identityService := rt.Services.Identity

	name := c.Params("name")
	if name == "" {
		return httpx.WithRepErrMsg(c, httpx.ProviderIsRequired.Code, httpx.ProviderIsRequired.Msg, c.Path())
	}

	if err := identityService.DeleteProvider(name); err != nil {
		return httpx.WithRepErrMsg(c, httpx.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.OPERATION, "delete identity provider")
	return nil
}

// getCookiePath gets cookie path from database configuration
// Returns the configured cookie path, or "/" as default if not found or error occurs
func (rt *Router) getCookiePath() string {
	const (
		defaultCookiePath = "/"
		category          = "system"
		name              = "base_path"
	)

	settings, err := rt.Services.GeneralSettings.GetGeneralSettingsByName(category, name)
	if err != nil {
		log.Debugw("failed to get cookie path from database, using default", "category", category, "name", name, "error", err)
		return defaultCookiePath
	}

	// Parse JSON data to extract cookie path
	if len(settings.Data) == 0 {
		log.Debugw("cookie path configuration data is empty, using default", "category", category, "name", name)
		return defaultCookiePath
	}

	var configData map[string]any
	if err = sonic.Unmarshal(settings.Data, &configData); err != nil {
		log.Warnw("failed to unmarshal cookie path configuration, using default", "category", category, "name", name, "error", err)
		return defaultCookiePath
	}

	// Extract base_path value from config data
	basePathValue, ok := configData["base_path"]
	if !ok {
		log.Debugw("base_path not found in configuration data, using default", "category", category, "name", name)
		return defaultCookiePath
	}

	basePathStr, ok := basePathValue.(string)
	if !ok || basePathStr == "" {
		log.Debugw("base_path is not a valid string, using default", "category", category, "name", name)
		return defaultCookiePath
	}

	// Parse URL to extract path component
	parsedURL, err := url.Parse(basePathStr)
	if err != nil {
		log.Warnw("failed to parse base_path as URL, using default", "base_path", basePathStr, "error", err)
		return defaultCookiePath
	}

	// Use the path from URL, or "/" if path is empty
	cookiePath := parsedURL.Path
	if cookiePath == "" {
		cookiePath = defaultCookiePath
	}

	log.Debugw("cookie path extracted from base_path", "base_path", basePathStr, "cookie_path", cookiePath)
	return cookiePath
}

// getBaseURL gets frontend base URL from database configuration
// Returns the configured frontend URL, or "http://localhost:5173" as default if not found or error occurs
func (rt *Router) getBaseURL() string {
	const (
		defaultBaseURL = "/"
		category       = "system"
		name           = "base_path"
	)

	settings, err := rt.Services.GeneralSettings.GetGeneralSettingsByName(category, name)
	if err != nil {
		log.Debugw("failed to get frontend base URL from database, using default", "category", category, "name", name, "error", err)
		return defaultBaseURL
	}

	// Parse JSON data to extract frontend URL
	if len(settings.Data) == 0 {
		log.Debugw("frontend base URL configuration data is empty, using default", "category", category, "name", name)
		return defaultBaseURL
	}

	var configData map[string]any
	if err = sonic.Unmarshal(settings.Data, &configData); err != nil {
		log.Warnw("failed to unmarshal frontend base URL configuration, using default", "category", category, "name", name, "error", err)
		return defaultBaseURL
	}

	// Extract base_path value from config data
	basePathValue, ok := configData["base_path"]
	if !ok {
		log.Debugw("base_path not found in configuration data, using default", "category", category, "name", name)
		return defaultBaseURL
	}

	basePathStr, ok := basePathValue.(string)
	if !ok || basePathStr == "" {
		log.Debugw("base_path is not a valid string, using default", "category", category, "name", name)
		return defaultBaseURL
	}

	// Validate URL format
	parsedURL, err := url.Parse(basePathStr)
	if err != nil {
		log.Warnw("failed to parse base_path as URL, using default", "base_path", basePathStr, "error", err)
		return defaultBaseURL
	}

	// Return the full URL (scheme + host + path)
	frontendURL := fmt.Sprintf("%s://%s%s", parsedURL.Scheme, parsedURL.Host, parsedURL.Path)
	if parsedURL.Path == "" {
		frontendURL = fmt.Sprintf("%s://%s/", parsedURL.Scheme, parsedURL.Host)
	}

	log.Debugw("base URL extracted from base_path", "base_path", basePathStr, "frontendURL", frontendURL)
	return frontendURL
}
