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

func (rt *Router) teamRouter(r fiber.Router, authMW fiber.Handler) {
	teamGroup := r.Group("/team")
	{
		// 创建团队
		teamGroup.Post("/create", authMW, rt.createTeam)

		// 更新团队
		teamGroup.Put("/:teamID", authMW, rt.updateTeam)

		// 删除团队
		teamGroup.Delete("/:teamID", authMW, rt.deleteTeam)

		// 获取团队详情
		teamGroup.Get("/:teamID", authMW, rt.getTeamByID)

		// 查询团队列表
		teamGroup.Get("/list", authMW, rt.listTeams)

		// 获取组织下的所有团队
		teamGroup.Get("/org/:orgId", authMW, rt.getTeamsByOrgID)

		// 获取子团队
		teamGroup.Get("/:teamID/subteams", authMW, rt.getSubTeams)

		// 获取用户所属团队
		teamGroup.Get("/user/myteams", authMW, rt.getUserTeams)

		// 启用/禁用团队
		teamGroup.Post("/:teamID/enable", authMW, rt.enableTeam)
		teamGroup.Post("/:teamID/disable", authMW, rt.disableTeam)

		// 更新团队统计信息
		teamGroup.Post("/:teamID/statistics", authMW, rt.updateTeamStatistics)
	}
}

// createTeam 创建团队
func (rt *Router) createTeam(c *fiber.Ctx) error {
	var req model.CreateTeamReq
	if err := c.BodyParser(&req); err != nil {
		log.Errorw("create team failed", "error", err)
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	// 获取当前用户ID
	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		log.Errorw("authentication failed", "error", err)
		return http.Err(c, http.AuthenticationFailed.Code, http.AuthenticationFailed.Msg)
	}

	teamService := rt.Services.Team

	result, err := teamService.CreateTeam(c.Context(), &req, claims.UserID)
	if err != nil {
		log.Errorw("create team failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, result)
}

// updateTeam 更新团队
func (rt *Router) updateTeam(c *fiber.Ctx) error {
	teamID := c.Params("teamID")
	if teamID == "" {
		return http.Err(c, http.TeamIDIsEmpty.Code, http.TeamIDIsEmpty.Msg)
	}

	var req model.UpdateTeamReq
	if err := c.BodyParser(&req); err != nil {
		log.Errorw("update team failed", "error", err)
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	teamService := rt.Services.Team

	result, err := teamService.UpdateTeam(c.Context(), teamID, &req)
	if err != nil {
		log.Errorw("update team failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, result)
}

// deleteTeam 删除团队
func (rt *Router) deleteTeam(c *fiber.Ctx) error {
	teamID := c.Params("teamID")
	if teamID == "" {
		return http.Err(c, http.TeamIDIsEmpty.Code, http.TeamIDIsEmpty.Msg)
	}

	teamService := rt.Services.Team

	if err := teamService.DeleteTeam(c.Context(), teamID); err != nil {
		log.Errorw("delete team failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Operation(c)
}

// getTeamByID 获取团队详情
func (rt *Router) getTeamByID(c *fiber.Ctx) error {
	teamID := c.Params("teamID")
	if teamID == "" {
		return http.Err(c, http.TeamIDIsEmpty.Code, http.TeamIDIsEmpty.Msg)
	}

	teamService := rt.Services.Team

	result, err := teamService.GetTeamByID(c.Context(), teamID)
	if err != nil {
		log.Errorw("get team by id failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, result)
}

// listTeams 查询团队列表
func (rt *Router) listTeams(c *fiber.Ctx) error {
	var query model.TeamQueryReq

	// 解析查询参数
	query.OrgID = c.Query("orgId")
	query.Name = c.Query("name")
	query.ParentTeamID = c.Query("parentTeamId")

	if visibilityStr := c.Query("visibility", ""); visibilityStr != "" {
		if visibility, err := strconv.Atoi(visibilityStr); err == nil {
			query.Visibility = &visibility
		}
	}

	if isEnabledStr := c.Query("isEnabled", ""); isEnabledStr != "" {
		if isEnabled, err := strconv.Atoi(isEnabledStr); err == nil {
			query.IsEnabled = &isEnabled
		}
	}

	if pageStr := c.Query("page", "1"); pageStr != "" {
		if page, err := strconv.Atoi(pageStr); err == nil {
			query.Page = page
		}
	}

	if pageSizeStr := c.Query("pageSize", "20"); pageSizeStr != "" {
		if pageSize, err := strconv.Atoi(pageSizeStr); err == nil {
			query.PageSize = pageSize
		}
	}

	teamService := rt.Services.Team

	result, err := teamService.ListTeams(c.Context(), &query)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

// getTeamsByOrgID 获取组织下的所有团队
func (rt *Router) getTeamsByOrgID(c *fiber.Ctx) error {
	orgID := c.Params("orgId")
	if orgID == "" {
		return http.Err(c, http.OrgIDIsEmpty.Code, http.OrgIDIsEmpty.Msg)
	}

	teamService := rt.Services.Team

	result, err := teamService.GetTeamsByOrgID(c.Context(), orgID)
	if err != nil {
		log.Errorw("get teams by org id failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, result)
}

// getSubTeams 获取子团队
func (rt *Router) getSubTeams(c *fiber.Ctx) error {
	teamID := c.Params("teamID")
	if teamID == "" {
		return http.Err(c, http.TeamIDIsEmpty.Code, http.TeamIDIsEmpty.Msg)
	}

	teamService := rt.Services.Team

	result, err := teamService.GetSubTeams(c.Context(), teamID)
	if err != nil {
		log.Errorw("get sub teams failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, result)
}

// getUserTeams 获取用户所属团队
func (rt *Router) getUserTeams(c *fiber.Ctx) error {
	// 获取当前用户ID
	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		log.Errorw("authentication failed", "error", err)
		return http.Err(c, http.AuthenticationFailed.Code, http.AuthenticationFailed.Msg)
	}

	teamService := rt.Services.Team

	result, err := teamService.GetTeamsByUserID(c.Context(), claims.UserID)
	if err != nil {
		log.Errorw("get teams by user id failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, result)
}

// enableTeam 启用团队
func (rt *Router) enableTeam(c *fiber.Ctx) error {
	teamID := c.Params("teamID")
	if teamID == "" {
		return http.Err(c, http.TeamIDIsEmpty.Code, http.TeamIDIsEmpty.Msg)
	}

	teamService := rt.Services.Team

	if err := teamService.EnableTeam(c.Context(), teamID); err != nil {
		log.Errorw("enable team failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Operation(c)
}

// disableTeam 禁用团队
func (rt *Router) disableTeam(c *fiber.Ctx) error {
	teamID := c.Params("teamID")
	if teamID == "" {
		return http.Err(c, http.TeamIDIsEmpty.Code, http.TeamIDIsEmpty.Msg)
	}

	teamService := rt.Services.Team

	if err := teamService.DisableTeam(c.Context(), teamID); err != nil {
		log.Errorw("disable team failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Operation(c)
}

// updateTeamStatistics 更新团队统计信息
func (rt *Router) updateTeamStatistics(c *fiber.Ctx) error {
	teamID := c.Params("teamID")
	if teamID == "" {
		return http.Err(c, http.TeamIDIsEmpty.Code, http.TeamIDIsEmpty.Msg)
	}

	teamService := rt.Services.Team

	if err := teamService.UpdateTeamStatistics(c.Context(), teamID); err != nil {
		log.Errorw("update team statistics failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Operation(c)
}
