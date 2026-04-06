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
	"github.com/arcentrix/arcentra/internal/case/identity"
	"github.com/arcentrix/arcentra/pkg/transport/http"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) roleRoutes(r fiber.Router, auth fiber.Handler) {
	roleGroup := r.Group("/role", auth)
	roleGroup.Post("", rt.createRole)
	roleGroup.Get("", rt.listRole)
	roleGroup.Get("/:roleId", rt.getRole)
	roleGroup.Put("/:roleId", rt.updateRole)
	roleGroup.Delete("/:roleId", rt.deleteRole)
}

func (rt *Router) createRole(c *fiber.Ctx) error {
	var req struct {
		RoleID      string `json:"roleId"`
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	role, err := rt.ManageRole.CreateRole(c.Context(), identity.CreateRoleInput{
		RoleID:      req.RoleID,
		Name:        req.Name,
		DisplayName: req.DisplayName,
		Description: req.Description,
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, role)
}

func (rt *Router) listRole(c *fiber.Ctx) error {
	pageNum := rt.HTTP.QueryInt(c, "pageNum")
	if pageNum <= 0 {
		pageNum = 1
	}
	pageSize := rt.HTTP.QueryInt(c, "pageSize")
	if pageSize <= 0 {
		pageSize = 10
	}

	roles, count, err := rt.ManageRole.ListRoles(c.Context(), pageNum, pageSize)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, map[string]any{
		"roles":    roles,
		"count":    count,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	})
}

func (rt *Router) getRole(c *fiber.Ctx) error {
	roleID := c.Params("roleId")
	if roleID == "" {
		return http.Err(c, http.BadRequest.Code, "role id is required")
	}

	role, err := rt.ManageRole.GetRole(c.Context(), roleID)
	if err != nil {
		return http.Err(c, http.NotFound.Code, "role not found")
	}

	return http.Detail(c, role)
}

func (rt *Router) updateRole(c *fiber.Ctx) error {
	roleID := c.Params("roleId")
	if roleID == "" {
		return http.Err(c, http.BadRequest.Code, "role id is required")
	}

	var req map[string]any
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	if err := rt.ManageRole.UpdateRole(c.Context(), roleID, req); err != nil {
		return http.Err(c, http.NotFound.Code, "role not found")
	}

	updatedRole, err := rt.ManageRole.GetRole(c.Context(), roleID)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, updatedRole)
}

func (rt *Router) deleteRole(c *fiber.Ctx) error {
	roleID := c.Params("roleId")
	if roleID == "" {
		return http.Err(c, http.BadRequest.Code, "role id is required")
	}

	if err := rt.ManageRole.DeleteRole(c.Context(), roleID); err != nil {
		return http.Err(c, http.NotFound.Code, "role not found")
	}

	return http.Operation(c)
}
