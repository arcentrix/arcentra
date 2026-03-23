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
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) projectRouter(r fiber.Router, authMW fiber.Handler) {
	projectGroup := r.Group("/project")
	{
		// 创建项目
		projectGroup.Post("/", authMW, rt.createProject)

		// 更新项目
		projectGroup.Put("/:projectID", authMW, rt.updateProject)

		// 删除项目
		projectGroup.Delete("/:projectID", authMW, rt.deleteProject)

		// 获取项目详情
		projectGroup.Get("/:projectID", authMW, rt.getProjectByID)

		// 查询项目列表
		projectGroup.Get("/", authMW, rt.listProjects)

		// 获取组织下的所有项目
		projectGroup.Get("/org/:orgID", authMW, rt.getProjectsByOrgID)

		// 获取用户的项目列表
		projectGroup.Get("/user/my-projects", authMW, rt.getUserProjects)

		// 启用/禁用项目
		projectGroup.Post("/:projectID/enable", authMW, rt.enableProject)
		projectGroup.Post("/:projectID/disable", authMW, rt.disableProject)

		// 更新项目统计信息
		projectGroup.Post("/:projectID/statistics", authMW, rt.updateProjectStatistics)

		// 项目成员管理
		projectGroup.Get("/:projectID/members", authMW, rt.getProjectMembers)
		projectGroup.Post("/:projectID/members", authMW, rt.addProjectMember)
		projectGroup.Put("/:projectID/members/:userID", authMW, rt.updateProjectMemberRole)
		projectGroup.Delete("/:projectID/members/:userID", authMW, rt.removeProjectMember)
	}
}

// createProject 创建项目
func (rt *Router) createProject(c *fiber.Ctx) error {
	var req model.CreateProjectReq
	if err := c.BodyParser(&req); err != nil {
		log.Errorw("create project failed", "error", err)
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	// 获取当前用户ID
	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		log.Errorw("authentication failed", "error", err)
		return http.Err(c, http.AuthenticationFailed.Code, http.AuthenticationFailed.Msg)
	}

	projectService := rt.Services.Project

	result, err := projectService.CreateProject(c.Context(), &req, claims.UserID)
	if err != nil {
		log.Errorw("create project failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

// updateProject 更新项目
func (rt *Router) updateProject(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	var req model.UpdateProjectReq
	if err := c.BodyParser(&req); err != nil {
		log.Errorw("update project failed", "error", err)
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	projectService := rt.Services.Project

	result, err := projectService.UpdateProject(c.Context(), projectID, &req)
	if err != nil {
		log.Errorw("update project failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

// deleteProject 删除项目
func (rt *Router) deleteProject(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	projectService := rt.Services.Project

	if err := projectService.DeleteProject(c.Context(), projectID); err != nil {
		log.Errorw("delete project failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

// getProjectByID 获取项目详情
func (rt *Router) getProjectByID(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	projectService := rt.Services.Project

	result, err := projectService.GetProjectByID(c.Context(), projectID)
	if err != nil {
		log.Errorw("get project by id failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

// listProjects 查询项目列表
func (rt *Router) listProjects(c *fiber.Ctx) error {
	var query model.ProjectQueryReq

	// 解析查询参数
	query.OrgID = c.Query("orgID")
	query.Name = c.Query("name")
	query.Language = c.Query("language")
	query.Tags = c.Query("tags")

	if statusStr := c.Query("status", ""); statusStr != "" {
		if status, err := strconv.Atoi(statusStr); err == nil {
			query.Status = &status
		}
	}

	if visibilityStr := c.Query("visibility", ""); visibilityStr != "" {
		if visibility, err := strconv.Atoi(visibilityStr); err == nil {
			query.Visibility = &visibility
		}
	}

	if pageNumStr := c.Query("pageNum", "1"); pageNumStr != "" {
		if pageNum, err := strconv.Atoi(pageNumStr); err == nil {
			query.PageNum = pageNum
		}
	}

	if pageSizeStr := c.Query("pageSize", "20"); pageSizeStr != "" {
		if pageSize, err := strconv.Atoi(pageSizeStr); err == nil {
			query.PageSize = pageSize
		}
	}

	projectService := rt.Services.Project

	projects, total, err := projectService.ListProjects(c.Context(), &query)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	// 构造响应
	response := map[string]any{
		"list":     projects,
		"total":    total,
		"pageNum":  query.PageNum,
		"pageSize": query.PageSize,
	}

	return http.Detail(c, response)
}

// getProjectsByOrgID 获取组织下的所有项目
func (rt *Router) getProjectsByOrgID(c *fiber.Ctx) error {
	orgID := c.Params("orgID")
	if orgID == "" {
		return http.Err(c, http.BadRequest.Code, "org id is required")
	}

	pageNum, _ := strconv.Atoi(c.Query("pageNum", "1"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize", "20"))

	var status *int
	if statusStr := c.Query("status", ""); statusStr != "" {
		if s, err := strconv.Atoi(statusStr); err == nil {
			status = &s
		}
	}

	projectService := rt.Services.Project

	projects, total, err := projectService.GetProjectsByOrgID(c.Context(), orgID, pageNum, pageSize, status)
	if err != nil {
		log.Errorw("get projects by org id failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	response := map[string]any{
		"list":     projects,
		"total":    total,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	}

	return http.Detail(c, response)
}

// getUserProjects 获取用户的项目列表
func (rt *Router) getUserProjects(c *fiber.Ctx) error {
	// 获取当前用户ID
	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		log.Errorw("authentication failed", "error", err)
		return http.Err(c, http.AuthenticationFailed.Code, http.AuthenticationFailed.Msg)
	}

	pageNum, _ := strconv.Atoi(c.Query("pageNum", "1"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize", "20"))
	orgID := c.Query("orgID", "")
	role := c.Query("role", "")

	projectService := rt.Services.Project

	projects, total, err := projectService.GetProjectsByUserId(c.Context(), claims.UserID, pageNum, pageSize, orgID, role)
	if err != nil {
		log.Errorw("get projects by user id failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	response := map[string]any{
		"list":     projects,
		"total":    total,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	}

	return http.Detail(c, response)
}

// enableProject 启用项目
func (rt *Router) enableProject(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	projectService := rt.Services.Project

	result, err := projectService.EnableProject(c.Context(), projectID)
	if err != nil {
		log.Errorw("enable project failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

// disableProject 禁用项目
func (rt *Router) disableProject(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	projectService := rt.Services.Project

	result, err := projectService.DisableProject(c.Context(), projectID)
	if err != nil {
		log.Errorw("disable project failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

// updateProjectStatistics 更新项目统计信息
func (rt *Router) updateProjectStatistics(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	var req model.ProjectStatisticsReq
	if err := c.BodyParser(&req); err != nil {
		log.Errorw("update project statistics failed", "error", err)
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	projectService := rt.Services.Project

	result, err := projectService.UpdateProjectStatistics(c.Context(), projectID, &req)
	if err != nil {
		log.Errorw("update project statistics failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

// getProjectMembers 获取项目成员列表
func (rt *Router) getProjectMembers(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	// 通过 repository 访问项目成员
	// 注意：这里需要通过 Services 获取 repository，或者创建一个 ProjectMemberService
	// 暂时直接使用 repository，后续可以优化
	members, err := rt.Services.ProjectMemberRepo.ListProjectMembers(c.Context(), projectID)
	if err != nil {
		log.Errorw("get project members failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	response := map[string]any{
		"list":  members,
		"total": len(members),
	}

	return http.Detail(c, response)
}

// addProjectMember 添加项目成员
func (rt *Router) addProjectMember(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	var req struct {
		UserID string `json:"userID" validate:"required"`
		RoleID string `json:"roleId" validate:"required"`
	}
	if err := c.BodyParser(&req); err != nil {
		log.Errorw("add project member failed", "error", err)
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	member := &model.ProjectMember{
		ProjectID: projectID,
		UserID:    req.UserID,
		RoleID:    req.RoleID,
	}

	if err := rt.Services.ProjectMemberRepo.AddProjectMember(c.Context(), member); err != nil {
		log.Errorw("add project member failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, member)
}

// updateProjectMemberRole 更新项目成员角色
func (rt *Router) updateProjectMemberRole(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	userID := c.Params("userID")
	if projectID == "" || userID == "" {
		return http.Err(c, http.BadRequest.Code, "project id and user id are required")
	}

	var req struct {
		RoleID string `json:"roleId" validate:"required"`
	}
	if err := c.BodyParser(&req); err != nil {
		log.Errorw("update project member role failed", "error", err)
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	if err := rt.Services.ProjectMemberRepo.UpdateProjectMemberRole(c.Context(), projectID, userID, req.RoleID); err != nil {
		log.Errorw("update project member role failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

// removeProjectMember 移除项目成员
func (rt *Router) removeProjectMember(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	userID := c.Params("userID")
	if projectID == "" || userID == "" {
		return http.Err(c, http.BadRequest.Code, "project id and user id are required")
	}

	if err := rt.Services.ProjectMemberRepo.RemoveProjectMember(c.Context(), projectID, userID); err != nil {
		log.Errorw("remove project member failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}
