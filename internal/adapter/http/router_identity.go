// Copyright 2026 Arcentra Authors.
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

package http

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/pkg/telemetry/log"
	"github.com/arcentrix/arcentra/pkg/transport/http"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) identityRoutes(r fiber.Router, auth fiber.Handler) {
	identityGroup := r.Group("/identity")

	// Public (unauthenticated) endpoints for the login page.
	identityGroup.Get("/providers/available", rt.listAvailableProviders)
	identityGroup.Get("/authorize/:provider", rt.authorize)
	identityGroup.Get("/callback/:provider", rt.callback)
	identityGroup.Post("/ldap/login/:provider", rt.ldapLogin)

	// Authenticated management endpoints.
	identityGroup.Get("/providers", auth, rt.listProviders)
	identityGroup.Post("/providers", auth, rt.createProvider)
	identityGroup.Get("/providers/types", auth, rt.listProviderTypes)
	identityGroup.Get("/providers/:name", auth, rt.getProvider)
	identityGroup.Put("/providers/:name", auth, rt.updateProvider)
	identityGroup.Put("/providers/:name/toggle", auth, rt.toggleProvider)
	identityGroup.Delete("/providers/:name", auth, rt.deleteProvider)
}

// listAvailableProviders returns enabled third-party login providers (no auth
// required). Sensitive fields (client secrets, bind passwords, etc.) are
// stripped so the response is safe for unauthenticated frontend consumption.
func (rt *Router) listAvailableProviders(c *fiber.Ctx) error {
	providers, err := rt.ManageUser.ListAvailableProviders(c.Context())
	if err != nil {
		log.Errorw("failed to list available providers", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, providers)
}

func (rt *Router) authorize(c *fiber.Ctx) error {
	providerName := c.Params("provider")
	if providerName == "" {
		return http.Err(c, http.ProviderIsRequired.Code, http.ProviderIsRequired.Msg)
	}

	authorizeURL, err := rt.ManageUser.Authorize(c.Context(), providerName)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return c.Redirect(authorizeURL, fiber.StatusTemporaryRedirect)
}

func (rt *Router) callback(c *fiber.Ctx) error {
	providerName := c.Params("provider")
	stateRaw := c.Query("state")
	codeRaw := c.Query("code")

	if stateRaw == "" || codeRaw == "" || providerName == "" {
		return http.Err(c, http.InvalidStatusParameter.Code, http.InvalidStatusParameter.Msg)
	}

	state, err := url.QueryUnescape(stateRaw)
	if err != nil {
		log.Warnw("failed to decode state parameter", "stateRaw", stateRaw, "error", err)
		state = stateRaw
	}
	code, err := url.QueryUnescape(codeRaw)
	if err != nil {
		log.Warnw("failed to decode code parameter", "codeRaw", codeRaw, "error", err)
		code = codeRaw
	}

	userInfo, err := rt.ManageUser.Callback(c.Context(), providerName, state, code)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	loginResult, err := rt.ManageUser.Login(c.Context(), userInfo.Username, userInfo.Email, "", rt.HTTP.Auth)
	if err != nil {
		log.Errorw("OAuth auto login failed", "provider", providerName, "error", err)
		return http.Err(c, http.Failed.Code, fmt.Sprintf("OAuth login failed: %v", err))
	}

	rt.storeTokenInCache(loginResult.UserInfo.UserID, loginResult.Token, rt.HTTP.Auth.AccessExpire)

	expireAt := time.Unix(loginResult.Token.ExpireAt, 0)

	cookiePath := rt.getCookiePath()

	c.Cookie(&fiber.Cookie{
		Name:     "accessToken",
		Value:    loginResult.Token.AccessToken,
		Path:     cookiePath,
		Expires:  expireAt,
		HTTPOnly: true,
		Secure:   false,
		SameSite: fiber.CookieSameSiteLaxMode,
	})

	refreshExpireAt := time.Now().Add(rt.HTTP.Auth.RefreshExpire)
	c.Cookie(&fiber.Cookie{
		Name:     "refreshToken",
		Value:    loginResult.Token.RefreshToken,
		Path:     cookiePath,
		Expires:  refreshExpireAt,
		HTTPOnly: true,
		Secure:   false,
		SameSite: fiber.CookieSameSiteLaxMode,
	})

	return c.Redirect(rt.getBaseURL(), fiber.StatusFound)
}

func (rt *Router) listProviders(c *fiber.Ctx) error {
	providerType := c.Query("type")

	providers, err := rt.ManageUser.ListProviders(c.Context(), providerType)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, providers)
}

func (rt *Router) getProvider(c *fiber.Ctx) error {
	name := c.Params("name")
	if name == "" {
		return http.Err(c, http.ProviderIsRequired.Code, http.ProviderIsRequired.Msg)
	}

	provider, err := rt.ManageUser.GetProvider(c.Context(), name)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, provider)
}

func (rt *Router) listProviderTypes(c *fiber.Ctx) error {
	types, err := rt.ManageUser.GetProviderTypes(c.Context())
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, types)
}

func (rt *Router) ldapLogin(c *fiber.Ctx) error {
	providerName := c.Params("provider")
	if providerName == "" {
		return http.Err(c, http.ProviderIsRequired.Code, http.ProviderIsRequired.Msg)
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, http.BadRequest.Msg)
	}
	if req.Username == "" || req.Password == "" {
		return http.Err(c, http.UsernameArePasswordIsRequired.Code, http.UsernameArePasswordIsRequired.Msg)
	}

	password, err := decodeBase64Password(req.Password)
	if err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid password encoding")
	}

	userInfo, err := rt.ManageUser.LDAPLogin(c.Context(), providerName, req.Username, password)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	loginResp, err := rt.ManageUser.Login(c.Context(), userInfo.Username, userInfo.Email, "", rt.HTTP.Auth)
	if err != nil {
		log.Errorw("LDAP auto login failed", "provider", providerName, "error", err)
		return http.Err(c, http.Failed.Code, fmt.Sprintf("failed to generate token: %v", err))
	}

	rt.storeTokenInCache(loginResp.UserInfo.UserID, loginResp.Token, rt.HTTP.Auth.AccessExpire)

	return http.Detail(c, loginResp)
}

func (rt *Router) createProvider(c *fiber.Ctx) error {
	var req map[string]any
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request parameters")
	}

	name, _ := req["name"].(string)
	providerType, _ := req["providerType"].(string)
	if name == "" || providerType == "" {
		return http.Err(c, http.BadRequest.Code, "name and providerType are required fields")
	}

	if err := rt.ManageUser.CreateProvider(c.Context(), req); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, req)
}

func (rt *Router) updateProvider(c *fiber.Ctx) error {
	name := c.Params("name")
	if name == "" {
		return http.Err(c, http.ProviderIsRequired.Code, http.ProviderIsRequired.Msg)
	}

	var req map[string]any
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request parameters")
	}

	if err := rt.ManageUser.UpdateProvider(c.Context(), name, req); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

func (rt *Router) toggleProvider(c *fiber.Ctx) error {
	name := c.Params("name")
	if name == "" {
		return http.Err(c, http.ProviderIsRequired.Code, http.ProviderIsRequired.Msg)
	}

	if err := rt.ManageUser.ToggleProvider(c.Context(), name); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

func (rt *Router) deleteProvider(c *fiber.Ctx) error {
	name := c.Params("name")
	if name == "" {
		return http.Err(c, http.ProviderIsRequired.Code, http.ProviderIsRequired.Msg)
	}

	if err := rt.ManageUser.DeleteProvider(c.Context(), name); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

func (rt *Router) getCookiePath() string {
	const defaultCookiePath = "/"
	settings, err := rt.ManageSettings.GetSettingsByName(context.Background(), "system", "base_path")
	if err != nil {
		return defaultCookiePath
	}
	if len(settings.Data) == 0 {
		return defaultCookiePath
	}
	var configData map[string]any
	if err = sonic.Unmarshal(settings.Data, &configData); err != nil {
		return defaultCookiePath
	}
	basePathValue, ok := configData["base_path"]
	if !ok {
		return defaultCookiePath
	}
	basePathStr, ok := basePathValue.(string)
	if !ok || basePathStr == "" {
		return defaultCookiePath
	}
	parsedURL, err := url.Parse(basePathStr)
	if err != nil {
		return defaultCookiePath
	}
	cookiePath := parsedURL.Path
	if cookiePath == "" {
		cookiePath = defaultCookiePath
	}
	return cookiePath
}

func (rt *Router) getBaseURL() string {
	const defaultBaseURL = "/"
	settings, err := rt.ManageSettings.GetSettingsByName(context.Background(), "system", "base_path")
	if err != nil {
		return defaultBaseURL
	}
	if len(settings.Data) == 0 {
		return defaultBaseURL
	}
	var configData map[string]any
	if err = sonic.Unmarshal(settings.Data, &configData); err != nil {
		return defaultBaseURL
	}
	basePathValue, ok := configData["base_path"]
	if !ok {
		return defaultBaseURL
	}
	basePathStr, ok := basePathValue.(string)
	if !ok || basePathStr == "" {
		return defaultBaseURL
	}
	parsedURL, err := url.Parse(basePathStr)
	if err != nil {
		return defaultBaseURL
	}
	frontendURL := fmt.Sprintf("%s://%s%s", parsedURL.Scheme, parsedURL.Host, parsedURL.Path)
	if parsedURL.Path == "" {
		frontendURL = fmt.Sprintf("%s://%s/", parsedURL.Scheme, parsedURL.Host)
	}
	return frontendURL
}

// suppress unused import warning
var _ = strings.TrimSpace
