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
	"strconv"

	"github.com/arcentrix/arcentra/internal/case/project"
	"github.com/arcentrix/arcentra/pkg/transport/auth"
	"github.com/arcentrix/arcentra/pkg/transport/http"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) secretRoutes(r fiber.Router, authMW fiber.Handler) {
	secretGroup := r.Group("/secrets")
	secretGroup.Post("/", authMW, rt.createSecret)
	secretGroup.Get("/", authMW, rt.getSecretList)
	secretGroup.Get("/:secretID", authMW, rt.getSecret)
	secretGroup.Get("/:secretID/value", authMW, rt.getSecretValue)
	secretGroup.Put("/:secretID", authMW, rt.updateSecret)
	secretGroup.Delete("/:secretID", authMW, rt.deleteSecret)
	secretGroup.Get("/scope/:scope/:scopeID", authMW, rt.getSecretsByScope)
}

func (rt *Router) createSecret(c *fiber.Ctx) error {
	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	var req struct {
		Name        string `json:"name"`
		SecretType  string `json:"secretType"`
		SecretValue string `json:"secretValue"`
		Description string `json:"description"`
		Scope       string `json:"scope"`
		ScopeID     string `json:"scopeId"`
	}
	if parseErr := c.BodyParser(&req); parseErr != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	secret, err := rt.ManageSecret.CreateSecret(c.Context(), project.CreateSecretInput{
		Name:        req.Name,
		SecretType:  req.SecretType,
		SecretValue: req.SecretValue,
		Description: req.Description,
		Scope:       req.Scope,
		ScopeID:     req.ScopeID,
		CreatedBy:   claims.UserID,
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	secret.SecretValue = "***MASKED***"
	return http.Detail(c, secret)
}

func (rt *Router) updateSecret(c *fiber.Ctx) error {
	secretID := c.Params("secretID")
	if secretID == "" {
		return http.Err(c, http.BadRequest.Code, "secretID is required")
	}

	var req map[string]any
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	if err := rt.ManageSecret.UpdateSecret(c.Context(), secretID, req); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	updatedSecret, err := rt.ManageSecret.GetSecret(c.Context(), secretID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, updatedSecret)
}

func (rt *Router) getSecret(c *fiber.Ctx) error {
	secretID := c.Params("secretID")
	if secretID == "" {
		return http.Err(c, http.BadRequest.Code, "secretID is required")
	}

	secret, err := rt.ManageSecret.GetSecret(c.Context(), secretID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, secret)
}

func (rt *Router) getSecretValue(c *fiber.Ctx) error {
	secretID := c.Params("secretID")
	if secretID == "" {
		return http.Err(c, http.BadRequest.Code, "secretID is required")
	}

	value, err := rt.ManageSecret.GetSecretValue(c.Context(), secretID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{
		"secretID": secretID,
		"value":    value,
	})
}

func (rt *Router) getSecretList(c *fiber.Ctx) error {
	pageNum, _ := strconv.Atoi(c.Query("pageNum", "1"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize", "20"))
	secretType := c.Query("secretType", "")
	scope := c.Query("scope", "")
	scopeID := c.Query("scopeID", "")
	createdBy := c.Query("createdBy", "")

	secrets, total, err := rt.ManageSecret.ListSecretsFiltered(c.Context(), pageNum, pageSize, secretType, scope, scopeID, createdBy)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{
		"list":     secrets,
		"total":    total,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	})
}

func (rt *Router) deleteSecret(c *fiber.Ctx) error {
	secretID := c.Params("secretID")
	if secretID == "" {
		return http.Err(c, http.BadRequest.Code, "secretID is required")
	}

	if err := rt.ManageSecret.DeleteSecret(c.Context(), secretID); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{"secretID": secretID})
}

func (rt *Router) getSecretsByScope(c *fiber.Ctx) error {
	scope := c.Params("scope")
	scopeID := c.Params("scopeID")
	if scope == "" || scopeID == "" {
		return http.Err(c, http.BadRequest.Code, "scope and scopeID are required")
	}

	secrets, err := rt.ManageSecret.GetSecretsByScope(c.Context(), scope, scopeID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{"secrets": secrets})
}
