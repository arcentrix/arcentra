// Copyright 2025 Arcade Team
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

	"github.com/arcentrix/arcade/internal/engine/model"
	"github.com/arcentrix/arcade/pkg/auth"
	"github.com/arcentrix/arcade/pkg/http"
	"github.com/arcentrix/arcade/pkg/http/middleware"
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
	claims, err := auth.ParseAuthorizationToken(c, rt.Http.Auth.SecretKey)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	var secret model.Secret
	if err := c.BodyParser(&secret); err != nil {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "invalid request body", c.Path())
	}

	if err := secretService.CreateSecret(&secret, claims.UserId); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	// mask secret value in response
	secret.SecretValue = "***MASKED***"

	c.Locals(middleware.DETAIL, secret)
	c.Locals(middleware.OPERATION, "create secret")
	return nil
}

// updateSecret updates a secret
func (rt *Router) updateSecret(c *fiber.Ctx) error {
	secretService := rt.Services.Secret

	secretId := c.Params("secretId")
	if secretId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "secretId is required", c.Path())
	}

	var secret model.Secret
	if err := c.BodyParser(&secret); err != nil {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "invalid request body", c.Path())
	}

	if err := secretService.UpdateSecret(secretId, &secret); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	// get updated secret (masked)
	updatedSecret, err := secretService.GetSecretByID(secretId)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, updatedSecret)
	c.Locals(middleware.OPERATION, "update secret")
	return nil
}

// getSecret gets a secret by ID (masked value)
func (rt *Router) getSecret(c *fiber.Ctx) error {
	secretService := rt.Services.Secret

	secretId := c.Params("secretId")
	if secretId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "secretId is required", c.Path())
	}

	secret, err := secretService.GetSecretByID(secretId)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, secret)
	c.Locals(middleware.OPERATION, "get secret")
	return nil
}

// getSecretValue gets the decrypted secret value (use with caution)
func (rt *Router) getSecretValue(c *fiber.Ctx) error {
	secretService := rt.Services.Secret

	secretId := c.Params("secretId")
	if secretId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "secretId is required", c.Path())
	}

	// TODO: Add additional permission check here
	// Only users with specific permissions should be able to get decrypted values

	value, err := secretService.GetSecretValue(secretId)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, map[string]interface{}{
		"secretId": secretId,
		"value":    value,
	})
	c.Locals(middleware.OPERATION, "get secret value")
	return nil
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

	secrets, total, err := secretService.GetSecretList(pageNum, pageSize, secretType, scope, scopeId, createdBy)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	// construct response
	response := map[string]interface{}{
		"list":     secrets,
		"total":    total,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	}

	c.Locals(middleware.DETAIL, response)
	c.Locals(middleware.OPERATION, "get secret list")
	return nil
}

// deleteSecret deletes a secret
func (rt *Router) deleteSecret(c *fiber.Ctx) error {
	secretService := rt.Services.Secret

	secretId := c.Params("secretId")
	if secretId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "secretId is required", c.Path())
	}

	if err := secretService.DeleteSecret(secretId); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, map[string]interface{}{"secretId": secretId})
	c.Locals(middleware.OPERATION, "delete secret")
	return nil
}

// getSecretsByScope gets secrets by scope and scope_id
func (rt *Router) getSecretsByScope(c *fiber.Ctx) error {
	secretService := rt.Services.Secret

	scope := c.Params("scope")
	scopeId := c.Params("scopeId")

	if scope == "" || scopeId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "scope and scopeId are required", c.Path())
	}

	secrets, err := secretService.GetSecretsByScope(scope, scopeId)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, map[string]interface{}{"secrets": secrets})
	c.Locals(middleware.OPERATION, "get secrets by scope")
	return nil
}
