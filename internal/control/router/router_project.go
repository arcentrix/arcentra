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
	"errors"
	"strconv"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/authz"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/auth"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) projectRouter(r fiber.Router, authMW fiber.Handler, subjectMW fiber.Handler) {
	projectGroup := r.Group("/project")
	{
		// 创建项目
		projectGroup.Post("/", sensitiveHTTPStatus, authMW, rt.firstBuildLiveUser(true),
			rt.liveCreateProjectAccess(),
			rt.createProject)

		// Versioned settings and lifecycle operations use live transaction-bound authorization.
		projectGroup.Get("/:projectID/settings", sensitiveHTTPStatus, authMW, rt.projectSettingsHandler("read"))
		projectGroup.Patch("/:projectID/settings", sensitiveHTTPStatus, authMW, rt.projectSettingsHandler("update"))
		projectGroup.Post("/:projectID/settings/archive", sensitiveHTTPStatus, authMW, rt.projectSettingsHandler("archive"))
		projectGroup.Post("/:projectID/settings/restore", sensitiveHTTPStatus, authMW, rt.projectSettingsHandler("restore"))

		projectGroup.Get("/:projectID/secrets", sensitiveHTTPStatus, authMW, rt.projectSecretsHandler("list"))
		projectGroup.Post("/:projectID/secrets", sensitiveHTTPStatus, authMW, rt.projectSecretsHandler("create"))
		projectGroup.Patch("/:projectID/secrets/:secretID", sensitiveHTTPStatus, authMW, rt.projectSecretsHandler("update"))
		projectGroup.Post("/:projectID/secrets/:secretID/rotate", sensitiveHTTPStatus, authMW, rt.projectSecretsHandler("rotate"))
		projectGroup.Post("/:projectID/secrets/:secretID/disable", sensitiveHTTPStatus, authMW, rt.projectSecretsHandler("disable"))

		// 更新项目
		projectGroup.Put("/:projectID", authMW, subjectMW,
			rt.permission("project:update", rt.resolveProjectScope()),
			rt.legacyProjectMutationUnavailable)

		// 删除项目
		projectGroup.Delete("/:projectID", authMW, subjectMW,
			rt.permission("project:update", rt.resolveProjectScope()),
			rt.legacyProjectMutationUnavailable)

		// 获取项目详情
		projectGroup.Get("/:projectID", authMW, subjectMW,
			rt.permission("project:read", rt.resolveProjectScope()),
			rt.getProjectByID)

		// 查询项目列表
		projectGroup.Get("/", authMW, subjectMW,
			rt.collectionAccess(),
			rt.listProjects)

		// 获取组织下的所有项目
		projectGroup.Get("/org/:orgID", authMW, subjectMW,
			rt.permission("org:read", resolveProjectOrganizationScope()),
			rt.getProjectsByOrgID)

		// 获取用户的项目列表
		projectGroup.Get("/user/my-projects", authMW, subjectMW,
			rt.collectionAccess(),
			rt.getUserProjects)

		// 启用/禁用项目
		projectGroup.Post("/:projectID/enable", authMW, subjectMW,
			rt.permission("project:update", rt.resolveProjectScope()),
			rt.legacyProjectMutationUnavailable)
		projectGroup.Post("/:projectID/disable", authMW, subjectMW,
			rt.permission("project:update", rt.resolveProjectScope()),
			rt.legacyProjectMutationUnavailable)

		// 更新项目统计信息
		projectGroup.Post("/:projectID/statistics", authMW, subjectMW,
			rt.permission("project:update", rt.resolveProjectScope()),
			rt.legacyProjectMutationUnavailable)

		// 项目成员管理
		projectGroup.Get("/:projectID/members", sensitiveHTTPStatus, authMW, rt.firstBuildLiveUser(false), rt.scopedProjectListMembers)
		projectGroup.Post("/:projectID/members", sensitiveHTTPStatus, authMW, rt.firstBuildLiveUser(false), rt.scopedProjectAddMember)
		projectGroup.Put("/:projectID/members/:userID", sensitiveHTTPStatus, authMW, rt.firstBuildLiveUser(false), rt.scopedProjectUpdateMember)
		projectGroup.Delete("/:projectID/members/:userID", sensitiveHTTPStatus, authMW, rt.firstBuildLiveUser(false), rt.scopedProjectRemoveMember)
	}
}

// Legacy project mutations cannot bypass versioned lifecycle or scope checks.
// Existing clients must read /settings and submit its current version explicitly.
func (rt *Router) legacyProjectMutationUnavailable(c *fiber.Ctx) error {
	return c.Status(fiber.StatusConflict).JSON(fiber.Map{"code": 409, "msg": "Use versioned project settings; legacy unversioned mutations are unavailable"})
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
		if errors.Is(err, repo.ErrOrganizationInvalid) {
			return sensitiveError(c, 400, "invalid project request")
		}
		if errors.Is(err, repo.ErrOrganizationForbidden) || errors.Is(err, repo.ErrOrganizationConflict) || errors.Is(err, repo.ErrOrganizationInvalid) || errors.Is(err, repo.ErrApprovalForbidden) {
			return organizationError(c, err)
		}
		return sensitiveError(c, 503, "project creation is unavailable")
	}

	return http.Detail(c, publicProject(result))
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
		return sensitiveError(c, 503, "project operation is unavailable")
	}

	return http.Detail(c, publicProject(result))
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
		return sensitiveError(c, 503, "project operation is unavailable")
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
		return sensitiveError(c, 503, "project operation is unavailable")
	}

	return http.Detail(c, publicProject(result))
}

// listProjects 查询项目列表
func (rt *Router) listProjects(c *fiber.Ctx) error {
	var query model.ProjectQueryReq

	// 解析查询参数
	query.OrgID = c.Query("orgId", c.Query("orgID"))
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

	query.PageNum, query.PageSize = normalizedCollectionPage(query.PageNum, query.PageSize)
	projects, total, err := rt.authorizedProjectList(c, &query)
	if err != nil {
		return sensitiveError(c, 503, "project operation is unavailable")
	}

	// 构造响应
	response := map[string]any{
		"list":     publicProjects(projects),
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

	pageNum, pageSize = normalizedCollectionPage(pageNum, pageSize)
	projects, total, err := rt.authorizedProjectList(c, &model.ProjectQueryReq{OrgID: orgID, PageNum: pageNum, PageSize: pageSize, Status: status})
	if err != nil {
		log.Errorw("get projects by org id failed", "error", err)
		return sensitiveError(c, 503, "project operation is unavailable")
	}

	response := map[string]any{
		"list":     publicProjects(projects),
		"total":    total,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	}

	return http.Detail(c, response)
}

// getUserProjects 获取用户的项目列表
func (rt *Router) getUserProjects(c *fiber.Ctx) error {
	// 当前用户及团队的角色授权决定可见项目，忽略任何用户 ID 查询参数
	return rt.listProjects(c)
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
		return sensitiveError(c, 503, "project operation is unavailable")
	}

	return http.Detail(c, publicProject(result))
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
		return sensitiveError(c, 503, "project operation is unavailable")
	}

	return http.Detail(c, publicProject(result))
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
		return sensitiveError(c, 503, "project operation is unavailable")
	}

	return http.Detail(c, publicProject(result))
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
		return sensitiveError(c, 503, "project operation is unavailable")
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
		UserID string `json:"userId" validate:"required"`
	}
	if err := c.BodyParser(&req); err != nil {
		log.Errorw("add project member failed", "error", err)
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	if strings.TrimSpace(req.UserID) == "" {
		return http.Err(c, http.BadRequest.Code, "user id is required")
	}
	member := &model.ProjectMember{
		ProjectID: projectID,
		UserID:    req.UserID,
	}

	if err := rt.Services.ProjectMemberRepo.AddProjectMember(c.Context(), member); err != nil {
		log.Errorw("add project member failed", "error", err)
		return sensitiveError(c, 503, "project operation is unavailable")
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
	_ = req
	// 角色已迁移到 role_grant，旧接口暂返回成功，实际角色调整由 role_grant API 负责
	return http.Detail(c, map[string]string{
		"projectId": projectID,
		"userId":    userID,
		"status":    "noop",
	})
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
		return sensitiveError(c, 503, "project operation is unavailable")
	}

	return http.Operation(c)
}

// resolveProjectScope 补齐项目所属组织，避免组织角色在项目接口丢失作用域
func (rt *Router) resolveProjectScope() middleware.ScopeResolver {
	return func(c *fiber.Ctx) (authz.ResourceRef, error) {
		if rt.Services == nil || rt.Services.Project == nil {
			return authz.ResourceRef{}, errors.New("project service is not configured")
		}
		projectID := strings.TrimSpace(c.Params("projectID"))
		if projectID == "" {
			return authz.ResourceRef{}, errors.New("project id is required")
		}
		project, err := rt.Services.Project.GetProjectByID(c.Context(), projectID)
		if err != nil {
			return authz.ResourceRef{}, err
		}
		if project == nil {
			return authz.ResourceRef{}, errors.New("project not found")
		}
		return authz.ResourceRef{ProjectID: project.ProjectID, OrgID: project.OrgID}, nil
	}
}

func resolveCreateProjectScope() middleware.ScopeResolver {
	return func(c *fiber.Ctx) (authz.ResourceRef, error) {
		var req model.CreateProjectReq
		if err := c.BodyParser(&req); err != nil {
			return authz.ResourceRef{}, err
		}
		orgID := strings.TrimSpace(req.OrgID)
		if orgID == "" {
			return authz.ResourceRef{}, errors.New("organization id is required")
		}
		return authz.ResourceRef{OrgID: orgID}, nil
	}
}

func resolveProjectOrganizationScope() middleware.ScopeResolver {
	return func(c *fiber.Ctx) (authz.ResourceRef, error) {
		orgID := strings.TrimSpace(c.Params("orgID"))
		if orgID == "" {
			return authz.ResourceRef{}, errors.New("organization id is required")
		}
		return authz.ResourceRef{OrgID: orgID}, nil
	}
}
