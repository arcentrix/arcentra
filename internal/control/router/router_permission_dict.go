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

package router

import (
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

// permissionRouter 注册权限字典路由
func (rt *Router) permissionRouter(r fiber.Router, authMW fiber.Handler, subject fiber.Handler) {
	g := r.Group("/permissions", authMW, subject)
	{
		g.Get("/", rt.permission("org:manage_member", middleware.ResolvePlatformScope()), rt.listPermissions)
		g.Post("/", rt.permission("org:manage_member", middleware.ResolvePlatformScope()), rt.createPermission)
		g.Get("/:id", rt.permission("org:manage_member", middleware.ResolvePlatformScope()), rt.getPermission)
		g.Put("/:id", rt.permission("org:manage_member", middleware.ResolvePlatformScope()), rt.updatePermission)
		g.Delete("/:id", rt.permission("org:manage_member", middleware.ResolvePlatformScope()), rt.deletePermission)
	}
}

type permissionDictItem struct {
	PermissionID string `json:"permissionId"`
	ResourceType string `json:"resourceType"`
	Action       string `json:"action"`
	ScopeType    string `json:"scopeType"`
	Description  string `json:"description"`
	IsSystem     int    `json:"isSystem"`
}

// listPermissions GET /api/v1/permissions - 返回权限字典并按 resourceType 分组
func (rt *Router) listPermissions(c *fiber.Ctx) error {
	permissions, err := rt.Services.Repos().Permission.ListPermissions(c.Context())
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	items := make([]permissionDictItem, 0, len(permissions))
	groupMap := make(map[string][]permissionDictItem)
	for _, p := range permissions {
		item := permissionDictItem{
			PermissionID: p.PermissionID,
			ResourceType: p.ResourceType,
			Action:       p.Action,
			ScopeType:    p.ScopeType,
			Description:  p.Description,
			IsSystem:     p.IsSystem,
		}
		items = append(items, item)
		groupMap[p.ResourceType] = append(groupMap[p.ResourceType], item)
	}

	groups := make([]fiber.Map, 0, len(groupMap))
	for resourceType, list := range groupMap {
		groups = append(groups, fiber.Map{
			"resourceType": resourceType,
			"permissions":  list,
		})
	}

	return http.Detail(c, fiber.Map{
		"permissions": items,
		"groups":      groups,
		"count":       len(items),
	})
}

type permissionRequest struct {
	PermissionID string `json:"permissionId"`
	ResourceType string `json:"resourceType"`
	Action       string `json:"action"`
	ScopeType    string `json:"scopeType"`
	Description  string `json:"description"`
}

// createPermission POST /api/v1/permissions
func (rt *Router) createPermission(c *fiber.Ctx) error {
	var req permissionRequest
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}
	resourceType := strings.TrimSpace(req.ResourceType)
	action := strings.TrimSpace(req.Action)
	if resourceType == "" || action == "" {
		return http.Err(c, http.BadRequest.Code, "resourceType and action are required")
	}
	permissionID := strings.TrimSpace(req.PermissionID)
	if permissionID == "" {
		permissionID = resourceType + ":" + action
	}
	scopeType := strings.TrimSpace(req.ScopeType)
	if scopeType == "" {
		scopeType = "project"
	}
	permission := &model.Permission{
		PermissionID: permissionID,
		ResourceType: resourceType,
		Action:       action,
		ScopeType:    scopeType,
		Description:  req.Description,
		IsSystem:     0,
	}
	if err := rt.Services.Repos().Permission.CreatePermission(c.Context(), permission); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if rt.Services.Authorizer != nil {
		_ = rt.Services.Authorizer.Reload(c.Context())
	}
	return http.Detail(c, permission)
}

// getPermission GET /api/v1/permissions/:id
func (rt *Router) getPermission(c *fiber.Ctx) error {
	permissionID := c.Params("id")
	if permissionID == "" {
		return http.Err(c, http.BadRequest.Code, "permission id is required")
	}
	permission, err := rt.Services.Repos().Permission.GetByPermissionID(c.Context(), permissionID)
	if err != nil {
		return http.Err(c, http.NotFound.Code, "permission not found")
	}
	return http.Detail(c, permission)
}

// updatePermission PUT /api/v1/permissions/:id
func (rt *Router) updatePermission(c *fiber.Ctx) error {
	permissionID := c.Params("id")
	if permissionID == "" {
		return http.Err(c, http.BadRequest.Code, "permission id is required")
	}
	existing, err := rt.Services.Repos().Permission.GetByPermissionID(c.Context(), permissionID)
	if err != nil {
		return http.Err(c, http.NotFound.Code, "permission not found")
	}
	if existing.IsSystem == 1 {
		return http.Err(c, http.Forbidden.Code, "system permission is read-only")
	}
	var req permissionRequest
	if err = c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}
	updates := map[string]any{}
	if req.ResourceType != "" {
		updates["resource_type"] = req.ResourceType
	}
	if req.Action != "" {
		updates["action"] = req.Action
	}
	if req.ScopeType != "" {
		updates["scope_type"] = req.ScopeType
	}
	if req.Description != "" {
		updates["description"] = req.Description
	}
	if len(updates) == 0 {
		return http.Detail(c, existing)
	}
	if err = rt.Services.Repos().Permission.UpdatePermission(c.Context(), permissionID, updates); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if rt.Services.Authorizer != nil {
		_ = rt.Services.Authorizer.Reload(c.Context())
	}
	updated, err := rt.Services.Repos().Permission.GetByPermissionID(c.Context(), permissionID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, updated)
}

// deletePermission DELETE /api/v1/permissions/:id
func (rt *Router) deletePermission(c *fiber.Ctx) error {
	permissionID := c.Params("id")
	if permissionID == "" {
		return http.Err(c, http.BadRequest.Code, "permission id is required")
	}
	existing, err := rt.Services.Repos().Permission.GetByPermissionID(c.Context(), permissionID)
	if err != nil {
		return http.Err(c, http.NotFound.Code, "permission not found")
	}
	if existing.IsSystem == 1 {
		return http.Err(c, http.Forbidden.Code, "system permission cannot be deleted")
	}
	if err := rt.Services.Repos().Permission.DeleteRoleBindingsByPermission(c.Context(), permissionID); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if err := rt.Services.Repos().Permission.DeletePermission(c.Context(), permissionID); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if rt.Services.Authorizer != nil {
		_ = rt.Services.Authorizer.Reload(c.Context())
	}
	return http.Operation(c)
}
