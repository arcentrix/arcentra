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
	"strconv"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/auth"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/gofiber/fiber/v2"
)

// secretRouter registers secret related routes
func (rt *Router) secretRouter(r fiber.Router, auth fiber.Handler) {
	secretGroup := r.Group("/secrets")
	{
		// Secret routes (authentication required)
		secretGroup.Post("/", auth, rt.createSecret)                          // POST /secrets - create secret
		secretGroup.Get("/", auth, rt.getSecretList)                          // GET /secrets - list secrets
		secretGroup.Get("/:secretId", auth, rt.getSecret)                     // GET /secrets/:secretId - get secret (masked)
		secretGroup.Get("/:secretId/value", auth, rt.getSecretValue)          // GET /secrets/:secretId/value - get secret value (decrypted)
		secretGroup.Put("/:secretId", auth, rt.updateSecret)                  // PUT /secrets/:secretId - update secret
		secretGroup.Delete("/:secretId", auth, rt.deleteSecret)               // DELETE /secrets/:secretId - delete secret
		secretGroup.Get("/scope/:scope/:scopeId", auth, rt.getSecretsByScope) // GET /secrets/scope/:scope/:scopeId - get secrets by scope
	}
}

// createSecret creates a new secret
func (rt *Router) createSecret(c *fiber.Ctx) error {
	secretService := rt.Services.Secret

	// get user ID from token
	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	var secret model.Secret
	if err := c.BodyParser(&secret); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	if err := secretService.CreateSecret(c.Context(), &secret, claims.UserId); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	// mask secret value in response
	secret.SecretValue = "***MASKED***"

	return http.Detail(c, secret)
}

// updateSecret updates a secret
func (rt *Router) updateSecret(c *fiber.Ctx) error {
	secretService := rt.Services.Secret

	secretId := c.Params("secretId")
	if secretId == "" {
		return http.Err(c, http.BadRequest.Code, "secretId is required")
	}

	var secret model.Secret
	if err := c.BodyParser(&secret); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	if err := secretService.UpdateSecret(c.Context(), secretId, &secret); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	// get updated secret (masked)
	updatedSecret, err := secretService.GetSecretByID(c.Context(), secretId)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, updatedSecret)
}

// getSecret gets a secret by ID (masked value)
func (rt *Router) getSecret(c *fiber.Ctx) error {
	secretService := rt.Services.Secret

	secretId := c.Params("secretId")
	if secretId == "" {
		return http.Err(c, http.BadRequest.Code, "secretId is required")
	}

	secret, err := secretService.GetSecretByID(c.Context(), secretId)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, secret)
}

// getSecretValue gets the decrypted secret value (use with caution)
func (rt *Router) getSecretValue(c *fiber.Ctx) error {
	secretService := rt.Services.Secret

	secretId := c.Params("secretId")
	if secretId == "" {
		return http.Err(c, http.BadRequest.Code, "secretId is required")
	}

	// TODO: Add additional permission check here
	// Only users with specific permissions should be able to get decrypted values

	value, err := secretService.GetSecretValue(c.Context(), secretId)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{
		"secretId": secretId,
		"value":    value,
	})
}

// getSecretList gets secret list with pagination and filters
func (rt *Router) getSecretList(c *fiber.Ctx) error {
	secretService := rt.Services.Secret

	// get query parameters
	pageNum, _ := strconv.Atoi(c.Query("pageNum", "1"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize", "20"))
	secretType := c.Query("secretType", "")
	scope := c.Query("scope", "")
	scopeId := c.Query("scopeId", "")
	createdBy := c.Query("createdBy", "")

	secrets, total, err := secretService.GetSecretList(c.Context(), pageNum, pageSize, secretType, scope, scopeId, createdBy)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	// construct response
	response := map[string]interface{}{
		"list":     secrets,
		"total":    total,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	}

	return http.Detail(c, response)
}

// deleteSecret deletes a secret
func (rt *Router) deleteSecret(c *fiber.Ctx) error {
	secretService := rt.Services.Secret

	secretId := c.Params("secretId")
	if secretId == "" {
		return http.Err(c, http.BadRequest.Code, "secretId is required")
	}

	if err := secretService.DeleteSecret(c.Context(), secretId); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{"secretId": secretId})
}

// getSecretsByScope gets secrets by scope and scope_id
func (rt *Router) getSecretsByScope(c *fiber.Ctx) error {
	secretService := rt.Services.Secret

	scope := c.Params("scope")
	scopeId := c.Params("scopeId")

	if scope == "" || scopeId == "" {
		return http.Err(c, http.BadRequest.Code, "scope and scopeId are required")
	}

	secrets, err := secretService.GetSecretsByScope(c.Context(), scope, scopeId)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{"secrets": secrets})
}
