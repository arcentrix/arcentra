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
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) roleRouter(r fiber.Router, auth fiber.Handler, subject fiber.Handler) {
	roleGroup := r.Group("/role", auth)
	{
		// RESTful API
		roleGroup.Post("", subject, rt.permission("org:manage_member", middleware.ResolvePlatformScope()), rt.createRole)
		roleGroup.Get("", subject, rt.permission("org:manage_member", middleware.ResolvePlatformScope()), rt.listRole)
		roleGroup.Get("/:roleId", subject, rt.permission("org:manage_member", middleware.ResolvePlatformScope()), rt.getRole)
		roleGroup.Put("/:roleId", subject, rt.permission("org:manage_member", middleware.ResolvePlatformScope()), rt.updateRole)
		roleGroup.Put("/:roleId/toggle", subject, rt.permission("org:manage_member", middleware.ResolvePlatformScope()), rt.toggleRole)
		roleGroup.Get("/:roleId/permissions", subject,
			rt.permission("org:manage_member", middleware.ResolvePlatformScope()),
			rt.getRolePermissions)
		roleGroup.Put("/:roleId/permissions", subject,
			rt.permission("org:manage_member", middleware.ResolvePlatformScope()),
			rt.updateRolePermissions)
		roleGroup.Delete("/:roleId", subject, rt.permission("org:manage_member", middleware.ResolvePlatformScope()), rt.deleteRole)
	}
}

// roleResponse 适配前端 Role 类型
type roleResponse struct {
	ID          uint64   `json:"id"`
	RoleID      string   `json:"roleId"`
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName"`
	Description string   `json:"description"`
	Scope       string   `json:"scope"`
	OrgID       string   `json:"orgId,omitempty"`
	IsBuiltin   int      `json:"isBuiltin"`
	IsEnabled   int      `json:"isEnabled"`
	Priority    int      `json:"priority"`
	Permissions []string `json:"permissions"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

func toRoleResponse(role *model.Role, permissions []string) roleResponse {
	if permissions == nil {
		permissions = []string{}
	}
	return roleResponse{
		ID:          role.ID,
		RoleID:      role.RoleID,
		Name:        role.Name,
		DisplayName: role.DisplayName,
		Description: role.Description,
		Scope:       role.ScopeType,
		OrgID:       role.OrgID,
		IsBuiltin:   role.IsSystem,
		IsEnabled:   role.IsEnabled,
		Priority:    rolePriority(role.IsSystem, role.ScopeType),
		Permissions: permissions,
		CreatedAt:   role.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:   role.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// rolePriority 根据角色作用域和是否内置生成排序优先级（仅供前端展示，无业务语义）
func rolePriority(isSystem int, scopeType string) int {
	base := 0
	switch scopeType {
	case "platform":
		base = 50
	case "org":
		base = 40
	case "project":
		base = 30
	case "team":
		base = 20
	default:
		base = 10
	}
	if isSystem == 1 {
		base += 5
	}
	return base
}

// roleCreateRequest 表示创建角色请求（兼容前端字段命名）
type roleCreateRequest struct {
	RoleID      string   `json:"roleId"`
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName"`
	Description string   `json:"description"`
	Scope       string   `json:"scope"`
	OrgID       string   `json:"orgId"`
	IsEnabled   *int     `json:"isEnabled"`
	Permissions []string `json:"permissions"`
}

// roleUpdateRequest 表示更新角色请求（兼容前端字段命名）
type roleUpdateRequest struct {
	Name        *string   `json:"name,omitempty"`
	DisplayName *string   `json:"displayName,omitempty"`
	Description *string   `json:"description,omitempty"`
	Scope       *string   `json:"scope,omitempty"`
	OrgID       *string   `json:"orgId,omitempty"`
	IsEnabled   *int      `json:"isEnabled,omitempty"`
	Permissions *[]string `json:"permissions,omitempty"`
}

// createRole POST /role - create a new role
func (rt *Router) createRole(c *fiber.Ctx) error {
	var req roleCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}
	if req.RoleID == "" || req.Name == "" {
		return http.Err(c, http.BadRequest.Code, "roleId and name are required")
	}

	createReq := &model.CreateRoleReq{
		RoleID:      req.RoleID,
		Name:        req.Name,
		DisplayName: req.DisplayName,
		Description: req.Description,
		ScopeType:   req.Scope,
		OrgID:       req.OrgID,
		IsEnabled:   req.IsEnabled,
	}
	role, err := rt.Services.Role.CreateRole(c.Context(), createReq)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}
	if len(req.Permissions) > 0 {
		if err := rt.replaceRolePermissions(c, role.RoleID, req.Permissions); err != nil {
			return http.Err(c, http.Failed.Code, err.Error())
		}
	}
	permissions := req.Permissions
	if permissions == nil {
		permissions = []string{}
	}
	return http.Detail(c, toRoleResponse(role, permissions))
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

	roleIDs := make([]string, 0, len(roles))
	for i := range roles {
		roleIDs = append(roleIDs, roles[i].RoleID)
	}
	bindings, err := rt.Services.Repos().Permission.ListRoleBindings(c.Context(), roleIDs)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}
	permMap := make(map[string][]string, len(roleIDs))
	for _, binding := range bindings {
		permMap[binding.RoleID] = append(permMap[binding.RoleID], binding.PermissionID)
	}

	resp := make([]roleResponse, 0, len(roles))
	for i := range roles {
		resp = append(resp, toRoleResponse(&roles[i], permMap[roles[i].RoleID]))
	}

	return http.Detail(c, fiber.Map{
		"roles":    resp,
		"count":    count,
		"total":    count,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	})
}

// getRole GET /role/:roleId - get role by roleId
func (rt *Router) getRole(c *fiber.Ctx) error {
	roleID := c.Params("roleId")
	if roleID == "" {
		return http.Err(c, http.BadRequest.Code, "role id is required")
	}
	role, err := rt.Services.Role.GetRoleByRoleID(c.Context(), roleID)
	if err != nil {
		return http.Err(c, http.NotFound.Code, "role not found")
	}
	permissions, err := rt.loadRolePermissions(c, roleID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, toRoleResponse(role, permissions))
}

// updateRole PUT /role/:roleId - update role
func (rt *Router) updateRole(c *fiber.Ctx) error {
	roleID := c.Params("roleId")
	if roleID == "" {
		return http.Err(c, http.BadRequest.Code, "role id is required")
	}

	var req roleUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	updateReq := &model.UpdateRoleReq{
		Name:        req.Name,
		DisplayName: req.DisplayName,
		Description: req.Description,
		ScopeType:   req.Scope,
		OrgID:       req.OrgID,
		IsEnabled:   req.IsEnabled,
	}
	if err := rt.Services.Role.UpdateRoleByRoleID(c.Context(), roleID, updateReq); err != nil {
		return http.Err(c, http.NotFound.Code, "role not found")
	}
	if req.Permissions != nil {
		if err := rt.replaceRolePermissions(c, roleID, *req.Permissions); err != nil {
			return http.Err(c, http.Failed.Code, err.Error())
		}
	}
	role, err := rt.Services.Role.GetRoleByRoleID(c.Context(), roleID)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}
	permissions, err := rt.loadRolePermissions(c, roleID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, toRoleResponse(role, permissions))
}

// toggleRole PUT /role/:roleId/toggle - flip role enabled state
func (rt *Router) toggleRole(c *fiber.Ctx) error {
	roleID := c.Params("roleId")
	if roleID == "" {
		return http.Err(c, http.BadRequest.Code, "role id is required")
	}
	role, err := rt.Services.Role.GetRoleByRoleID(c.Context(), roleID)
	if err != nil {
		return http.Err(c, http.NotFound.Code, "role not found")
	}
	next := 0
	if role.IsEnabled == 0 {
		next = 1
	}
	updateReq := &model.UpdateRoleReq{IsEnabled: &next}
	if err = rt.Services.Role.UpdateRoleByRoleID(c.Context(), roleID, updateReq); err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}
	updated, err := rt.Services.Role.GetRoleByRoleID(c.Context(), roleID)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}
	permissions, err := rt.loadRolePermissions(c, roleID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, toRoleResponse(updated, permissions))
}

// getRolePermissions GET /role/:roleId/permissions - role permissions list
func (rt *Router) getRolePermissions(c *fiber.Ctx) error {
	roleID := c.Params("roleId")
	if roleID == "" {
		return http.Err(c, http.BadRequest.Code, "role id is required")
	}
	permissions, err := rt.loadRolePermissions(c, roleID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, permissions)
}

type rolePermissionsRequest struct {
	Permissions []string `json:"permissions"`
}

// updateRolePermissions PUT /role/:roleId/permissions - replace role permissions
func (rt *Router) updateRolePermissions(c *fiber.Ctx) error {
	roleID := c.Params("roleId")
	if roleID == "" {
		return http.Err(c, http.BadRequest.Code, "role id is required")
	}
	var req rolePermissionsRequest
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}
	if err := rt.replaceRolePermissions(c, roleID, req.Permissions); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	role, err := rt.Services.Role.GetRoleByRoleID(c.Context(), roleID)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}
	return http.Detail(c, toRoleResponse(role, req.Permissions))
}

// deleteRole DELETE /role/:roleId - delete role
func (rt *Router) deleteRole(c *fiber.Ctx) error {
	roleID := c.Params("roleId")
	if roleID == "" {
		return http.Err(c, http.BadRequest.Code, "role id is required")
	}
	if err := rt.Services.Role.DeleteRoleByRoleID(c.Context(), roleID); err != nil {
		return http.Err(c, http.NotFound.Code, "role not found")
	}
	return http.Operation(c)
}

// loadRolePermissions 查询角色权限ID列表
func (rt *Router) loadRolePermissions(c *fiber.Ctx, roleID string) ([]string, error) {
	bindings, err := rt.Services.Repos().Permission.ListRoleBindings(c.Context(), []string{roleID})
	if err != nil {
		return nil, err
	}
	permissions := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		permissions = append(permissions, binding.PermissionID)
	}
	return permissions, nil
}

// replaceRolePermissions 用入参覆盖角色的权限绑定
func (rt *Router) replaceRolePermissions(c *fiber.Ctx, roleID string, permissions []string) error {
	repos := rt.Services.Repos()
	if err := repos.Permission.DeleteRoleBindingsByRole(c.Context(), roleID); err != nil {
		return err
	}
	if len(permissions) == 0 {
		return nil
	}
	bindings := make([]model.RolePermissionBinding, 0, len(permissions))
	for _, permID := range permissions {
		if permID == "" {
			continue
		}
		bindings = append(bindings, model.RolePermissionBinding{
			RoleID:       roleID,
			PermissionID: permID,
		})
	}
	if err := repos.Permission.BatchUpsertRoleBindings(c.Context(), bindings); err != nil {
		return err
	}
	if rt.Services.Authorizer != nil {
		_ = rt.Services.Authorizer.Reload(c.Context())
	}
	return nil
}
