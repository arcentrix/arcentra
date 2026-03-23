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
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) roleRouter(r fiber.Router, auth fiber.Handler) {
	roleGroup := r.Group("/role", auth)
	{
		// RESTful API
		roleGroup.Post("", rt.createRole)           // POST /role - create role
		roleGroup.Get("", rt.listRole)              // GET /role - list roles
		roleGroup.Get("/:roleId", rt.getRole)       // GET /role/:roleId - get role by roleId
		roleGroup.Put("/:roleId", rt.updateRole)    // PUT /role/:roleId - update role
		roleGroup.Delete("/:roleId", rt.deleteRole) // DELETE /role/:roleId - delete role
	}
}

// createRole POST /role - create a new role
func (rt *Router) createRole(c *fiber.Ctx) error {
	var createRoleReq *model.CreateRoleReq
	roleLogic := rt.Services.Role

	if err := c.BodyParser(&createRoleReq); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	role, err := roleLogic.CreateRole(c.Context(), createRoleReq)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, role)
}

// listRole GET /role - list roles with pagination
func (rt *Router) listRole(c *fiber.Ctx) error {
	roleLogic := rt.Services.Role

	pageNum := rt.HTTP.QueryInt(c, "pageNum")
	if pageNum <= 0 {
		pageNum = 1
	}
	pageSize := rt.HTTP.QueryInt(c, "pageSize")
	if pageSize <= 0 {
		pageSize = 10
	}

	roles, count, err := roleLogic.ListRoles(c.Context(), pageNum, pageSize)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	result := make(map[string]any)
	result["roles"] = roles
	result["count"] = count
	result["pageNum"] = pageNum
	result["pageSize"] = pageSize
	return http.Detail(c, result)
}

// getRole GET /role/:roleId - get role by roleId
func (rt *Router) getRole(c *fiber.Ctx) error {
	roleID := c.Params("roleId")
	if roleID == "" {
		return http.Err(c, http.BadRequest.Code, "role id is required")
	}

	roleLogic := rt.Services.Role
	role, err := roleLogic.GetRoleByRoleID(c.Context(), roleID)
	if err != nil {
		return http.Err(c, http.NotFound.Code, "role not found")
	}

	return http.Detail(c, role)
}

// updateRole PUT /role/:roleId - update role
func (rt *Router) updateRole(c *fiber.Ctx) error {
	roleID := c.Params("roleId")
	if roleID == "" {
		return http.Err(c, http.BadRequest.Code, "role id is required")
	}

	var updateReq *model.UpdateRoleReq
	if err := c.BodyParser(&updateReq); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	roleLogic := rt.Services.Role
	if err := roleLogic.UpdateRoleByRoleID(c.Context(), roleID, updateReq); err != nil {
		return http.Err(c, http.NotFound.Code, "role not found")
	}

	// Get updated role
	updatedRole, err := roleLogic.GetRoleByRoleID(c.Context(), roleID)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, updatedRole)
}

// deleteRole DELETE /role/:roleId - delete role
func (rt *Router) deleteRole(c *fiber.Ctx) error {
	roleID := c.Params("roleId")
	if roleID == "" {
		return http.Err(c, http.BadRequest.Code, "role id is required")
	}

	roleLogic := rt.Services.Role
	if err := roleLogic.DeleteRoleByRoleID(c.Context(), roleID); err != nil {
		return http.Err(c, http.NotFound.Code, "role not found")
	}

	return http.Operation(c)
}
