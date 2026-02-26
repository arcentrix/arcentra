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
	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
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
		return http.WithRepErrMsg(c, http.BadRequest.Code, "invalid request body", c.Path())
	}

	role, err := roleLogic.CreateRole(c.Context(), createRoleReq)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.DETAIL, role)
	return nil
}

// listRole GET /role - list roles with pagination
func (rt *Router) listRole(c *fiber.Ctx) error {
	roleLogic := rt.Services.Role

	pageNum := rt.Http.QueryInt(c, "pageNum")
	if pageNum <= 0 {
		pageNum = 1
	}
	pageSize := rt.Http.QueryInt(c, "pageSize")
	if pageSize <= 0 {
		pageSize = 10
	}

	roles, count, err := roleLogic.ListRoles(c.Context(), pageNum, pageSize)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	result := make(map[string]any)
	result["roles"] = roles
	result["count"] = count
	result["pageNum"] = pageNum
	result["pageSize"] = pageSize
	c.Locals(middleware.DETAIL, result)
	return nil
}

// getRole GET /role/:roleId - get role by roleId
func (rt *Router) getRole(c *fiber.Ctx) error {
	roleId := c.Params("roleId")
	if roleId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "role id is required", c.Path())
	}

	roleLogic := rt.Services.Role
	role, err := roleLogic.GetRoleByRoleId(c.Context(), roleId)
	if err != nil {
		return http.WithRepErrMsg(c, http.NotFound.Code, "role not found", c.Path())
	}

	c.Locals(middleware.DETAIL, role)
	return nil
}

// updateRole PUT /role/:roleId - update role
func (rt *Router) updateRole(c *fiber.Ctx) error {
	roleId := c.Params("roleId")
	if roleId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "role id is required", c.Path())
	}

	var updateReq *model.UpdateRoleReq
	if err := c.BodyParser(&updateReq); err != nil {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "invalid request body", c.Path())
	}

	roleLogic := rt.Services.Role
	if err := roleLogic.UpdateRoleByRoleId(c.Context(), roleId, updateReq); err != nil {
		return http.WithRepErrMsg(c, http.NotFound.Code, "role not found", c.Path())
	}

	// Get updated role
	updatedRole, err := roleLogic.GetRoleByRoleId(c.Context(), roleId)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.DETAIL, updatedRole)
	return nil
}

// deleteRole DELETE /role/:roleId - delete role
func (rt *Router) deleteRole(c *fiber.Ctx) error {
	roleId := c.Params("roleId")
	if roleId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "role id is required", c.Path())
	}

	roleLogic := rt.Services.Role
	if err := roleLogic.DeleteRoleByRoleId(c.Context(), roleId); err != nil {
		return http.WithRepErrMsg(c, http.NotFound.Code, "role not found", c.Path())
	}

	c.Locals(middleware.OPERATION, "delete role")
	return nil
}
