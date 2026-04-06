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

	"github.com/arcentrix/arcentra/pkg/telemetry/log"
	"github.com/arcentrix/arcentra/pkg/transport/auth"
	"github.com/arcentrix/arcentra/pkg/transport/http"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) projectRoutes(r fiber.Router, authMW fiber.Handler) {
	projectGroup := r.Group("/project")
	projectGroup.Post("/", authMW, rt.createProject)
	projectGroup.Put("/:projectID", authMW, rt.updateProject)
	projectGroup.Delete("/:projectID", authMW, rt.deleteProject)
	projectGroup.Get("/:projectID", authMW, rt.getProjectByID)
	projectGroup.Get("/", authMW, rt.listProjects)
	projectGroup.Get("/org/:orgID", authMW, rt.getProjectsByOrgID)
	projectGroup.Get("/user/my-projects", authMW, rt.getUserProjects)
	projectGroup.Post("/:projectID/enable", authMW, rt.enableProject)
	projectGroup.Post("/:projectID/disable", authMW, rt.disableProject)
	projectGroup.Post("/:projectID/statistics", authMW, rt.updateProjectStatistics)
	projectGroup.Get("/:projectID/members", authMW, rt.getProjectMembers)
	projectGroup.Post("/:projectID/members", authMW, rt.addProjectMember)
	projectGroup.Put("/:projectID/members/:userID", authMW, rt.updateProjectMemberRole)
	projectGroup.Delete("/:projectID/members/:userID", authMW, rt.removeProjectMember)
}

func (rt *Router) createProject(c *fiber.Ctx) error {
	var req struct {
		OrgID         string `json:"orgId"`
		Name          string `json:"name"`
		DisplayName   string `json:"displayName"`
		Description   string `json:"description"`
		RepoURL       string `json:"repoUrl"`
		RepoType      string `json:"repoType"`
		DefaultBranch string `json:"defaultBranch"`
		AuthType      int    `json:"authType"`
		Credential    string `json:"credential"`
		Visibility    int    `json:"visibility"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		return http.Err(c, http.AuthenticationFailed.Code, http.AuthenticationFailed.Msg)
	}

	result, err := rt.ManageProject.CreateProjectFull(
		c.Context(),
		req.OrgID, req.Name, req.DisplayName, req.Description,
		req.RepoURL, req.RepoType, req.DefaultBranch,
		req.AuthType, req.Credential, req.Visibility, claims.UserID,
	)
	if err != nil {
		log.Errorw("create project failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

func (rt *Router) updateProject(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	var req map[string]any
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	if err := rt.ManageProject.UpdateProject(c.Context(), projectID, req); err != nil {
		log.Errorw("update project failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	result, err := rt.ManageProject.GetProject(c.Context(), projectID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

func (rt *Router) deleteProject(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	if err := rt.ManageProject.DeleteProject(c.Context(), projectID); err != nil {
		log.Errorw("delete project failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

func (rt *Router) getProjectByID(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	result, err := rt.ManageProject.GetProject(c.Context(), projectID)
	if err != nil {
		log.Errorw("get project by id failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

func (rt *Router) listProjects(c *fiber.Ctx) error {
	orgID := c.Query("orgID")
	pageNum, _ := strconv.Atoi(c.Query("pageNum", "1"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize", "20"))

	projects, total, err := rt.ManageProject.ListProjects(c.Context(), orgID, pageNum, pageSize)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{
		"list":     projects,
		"total":    total,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	})
}

func (rt *Router) getProjectsByOrgID(c *fiber.Ctx) error {
	orgID := c.Params("orgID")
	if orgID == "" {
		return http.Err(c, http.BadRequest.Code, "org id is required")
	}

	pageNum, _ := strconv.Atoi(c.Query("pageNum", "1"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize", "20"))

	projects, total, err := rt.ManageProject.ListProjects(c.Context(), orgID, pageNum, pageSize)
	if err != nil {
		log.Errorw("get projects by org id failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{
		"list":     projects,
		"total":    total,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	})
}

func (rt *Router) getUserProjects(c *fiber.Ctx) error {
	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		return http.Err(c, http.AuthenticationFailed.Code, http.AuthenticationFailed.Msg)
	}

	pageNum, _ := strconv.Atoi(c.Query("pageNum", "1"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize", "20"))

	projects, total, err := rt.ManageProject.GetProjectsByUserID(c.Context(), claims.UserID, pageNum, pageSize)
	if err != nil {
		log.Errorw("get projects by user id failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{
		"list":     projects,
		"total":    total,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	})
}

func (rt *Router) enableProject(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	if err := rt.ManageProject.UpdateProject(c.Context(), projectID, map[string]any{"is_enabled": true}); err != nil {
		log.Errorw("enable project failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	result, err := rt.ManageProject.GetProject(c.Context(), projectID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

func (rt *Router) disableProject(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	if err := rt.ManageProject.UpdateProject(c.Context(), projectID, map[string]any{"is_enabled": false}); err != nil {
		log.Errorw("disable project failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	result, err := rt.ManageProject.GetProject(c.Context(), projectID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

func (rt *Router) updateProjectStatistics(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	var req map[string]any
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	if err := rt.ManageProject.UpdateProject(c.Context(), projectID, req); err != nil {
		log.Errorw("update project statistics failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	result, err := rt.ManageProject.GetProject(c.Context(), projectID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

func (rt *Router) getProjectMembers(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	members, err := rt.ManageProject.GetProjectMembers(c.Context(), projectID)
	if err != nil {
		log.Errorw("get project members failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{
		"list":  members,
		"total": len(members),
	})
}

func (rt *Router) addProjectMember(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	if projectID == "" {
		return http.Err(c, http.BadRequest.Code, "project id is required")
	}

	var req struct {
		UserID string `json:"userID"`
		RoleID string `json:"roleId"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	if err := rt.ManageProject.AddProjectMember(c.Context(), projectID, req.UserID, req.RoleID); err != nil {
		log.Errorw("add project member failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{
		"projectID": projectID,
		"userID":    req.UserID,
		"roleId":    req.RoleID,
	})
}

func (rt *Router) updateProjectMemberRole(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	userID := c.Params("userID")
	if projectID == "" || userID == "" {
		return http.Err(c, http.BadRequest.Code, "project id and user id are required")
	}

	var req struct {
		RoleID string `json:"roleId"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	if err := rt.ManageProject.UpdateProjectMemberRole(c.Context(), projectID, userID, req.RoleID); err != nil {
		log.Errorw("update project member role failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

func (rt *Router) removeProjectMember(c *fiber.Ctx) error {
	projectID := c.Params("projectID")
	userID := c.Params("userID")
	if projectID == "" || userID == "" {
		return http.Err(c, http.BadRequest.Code, "project id and user id are required")
	}

	if err := rt.ManageProject.RemoveProjectMember(c.Context(), projectID, userID); err != nil {
		log.Errorw("remove project member failed", "error", err)
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}
