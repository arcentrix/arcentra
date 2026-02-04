// Copyright 2025 Arcade Team
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

	"github.com/arcentrix/arcade/internal/engine/model"
	"github.com/arcentrix/arcade/pkg/auth"
	"github.com/arcentrix/arcade/pkg/http"
	"github.com/arcentrix/arcade/pkg/http/middleware"
	"github.com/arcentrix/arcade/pkg/log"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) teamRouter(r fiber.Router, auth fiber.Handler) {
	teamGroup := r.Group("/team")
	{
		// 创建团队
		teamGroup.Post("/create", auth, rt.createTeam)

		// 更新团队
		teamGroup.Put("/:teamId", auth, rt.updateTeam)

		// 删除团队
		teamGroup.Delete("/:teamId", auth, rt.deleteTeam)

		// 获取团队详情
		teamGroup.Get("/:teamId", auth, rt.getTeamById)

		// 查询团队列表
		teamGroup.Get("/list", auth, rt.listTeams)

		// 获取组织下的所有团队
		teamGroup.Get("/org/:orgId", auth, rt.getTeamsByOrgId)

		// 获取子团队
		teamGroup.Get("/:teamId/subteams", auth, rt.getSubTeams)

		// 获取用户所属团队
		teamGroup.Get("/user/myteams", auth, rt.getUserTeams)

		// 启用/禁用团队
		teamGroup.Post("/:teamId/enable", auth, rt.enableTeam)
		teamGroup.Post("/:teamId/disable", auth, rt.disableTeam)

		// 更新团队统计信息
		teamGroup.Post("/:teamId/statistics", auth, rt.updateTeamStatistics)
	}
}

// createTeam 创建团队
func (rt *Router) createTeam(c *fiber.Ctx) error {
	var req model.CreateTeamReq
	if err := c.BodyParser(&req); err != nil {
		log.Errorw("create team failed", "error", err)
		return http.WithRepErrMsg(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg, c.Path())
	}

	// 获取当前用户ID
	claims, err := auth.ParseAuthorizationToken(c, rt.Http.Auth.SecretKey)
	if err != nil {
		log.Errorw("authentication failed", "error", err)
		return http.WithRepErrMsg(c, http.AuthenticationFailed.Code, http.AuthenticationFailed.Msg, c.Path())
	}

	teamService := rt.Services.Team

	result, err := teamService.CreateTeam(&req, claims.UserId)
	if err != nil {
		log.Errorw("create team failed", "error", err)
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.DETAIL, result)
	return nil
}

// updateTeam 更新团队
func (rt *Router) updateTeam(c *fiber.Ctx) error {
	teamId := c.Params("teamId")
	if teamId == "" {
		return http.WithRepErrMsg(c, http.TeamIdIsEmpty.Code, http.TeamIdIsEmpty.Msg, c.Path())
	}

	var req model.UpdateTeamReq
	if err := c.BodyParser(&req); err != nil {
		log.Errorw("update team failed", "error", err)
		return http.WithRepErrMsg(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg, c.Path())
	}

	teamService := rt.Services.Team

	result, err := teamService.UpdateTeam(teamId, &req)
	if err != nil {
		log.Errorw("update team failed", "error", err)
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.DETAIL, result)
	return nil
}

// deleteTeam 删除团队
func (rt *Router) deleteTeam(c *fiber.Ctx) error {
	teamId := c.Params("teamId")
	if teamId == "" {
		return http.WithRepErrMsg(c, http.TeamIdIsEmpty.Code, http.TeamIdIsEmpty.Msg, c.Path())
	}

	teamService := rt.Services.Team

	if err := teamService.DeleteTeam(teamId); err != nil {
		log.Errorw("delete team failed", "error", err)
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.OPERATION, teamId)
	return nil
}

// getTeamById 获取团队详情
func (rt *Router) getTeamById(c *fiber.Ctx) error {
	teamId := c.Params("teamId")
	if teamId == "" {
		return http.WithRepErrMsg(c, http.TeamIdIsEmpty.Code, http.TeamIdIsEmpty.Msg, c.Path())
	}

	teamService := rt.Services.Team

	result, err := teamService.GetTeamById(teamId)
	if err != nil {
		log.Errorw("get team by id failed", "error", err)
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.DETAIL, result)
	return nil
}

// listTeams 查询团队列表
func (rt *Router) listTeams(c *fiber.Ctx) error {
	var query model.TeamQueryReq

	// 解析查询参数
	query.OrgId = c.Query("orgId")
	query.Name = c.Query("name")
	query.ParentTeamId = c.Query("parentTeamId")

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

	result, err := teamService.ListTeams(&query)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, result)
	return nil
}

// getTeamsByOrgId 获取组织下的所有团队
func (rt *Router) getTeamsByOrgId(c *fiber.Ctx) error {
	orgId := c.Params("orgId")
	if orgId == "" {
		return http.WithRepErrMsg(c, http.OrgIdIsEmpty.Code, http.OrgIdIsEmpty.Msg, c.Path())
	}

	teamService := rt.Services.Team

	result, err := teamService.GetTeamsByOrgId(orgId)
	if err != nil {
		log.Errorw("get teams by org id failed", "error", err)
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.DETAIL, result)
	return nil
}

// getSubTeams 获取子团队
func (rt *Router) getSubTeams(c *fiber.Ctx) error {
	teamId := c.Params("teamId")
	if teamId == "" {
		return http.WithRepErrMsg(c, http.TeamIdIsEmpty.Code, http.TeamIdIsEmpty.Msg, c.Path())
	}

	teamService := rt.Services.Team

	result, err := teamService.GetSubTeams(teamId)
	if err != nil {
		log.Errorw("get sub teams failed", "error", err)
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.DETAIL, result)
	return nil
}

// getUserTeams 获取用户所属团队
func (rt *Router) getUserTeams(c *fiber.Ctx) error {
	// 获取当前用户ID
	claims, err := auth.ParseAuthorizationToken(c, rt.Http.Auth.SecretKey)
	if err != nil {
		log.Errorw("authentication failed", "error", err)
		return http.WithRepErrMsg(c, http.AuthenticationFailed.Code, http.AuthenticationFailed.Msg, c.Path())
	}

	teamService := rt.Services.Team

	result, err := teamService.GetTeamsByUserId(claims.UserId)
	if err != nil {
		log.Errorw("get teams by user id failed", "error", err)
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.DETAIL, result)
	return nil
}

// enableTeam 启用团队
func (rt *Router) enableTeam(c *fiber.Ctx) error {
	teamId := c.Params("teamId")
	if teamId == "" {
		return http.WithRepErrMsg(c, http.TeamIdIsEmpty.Code, http.TeamIdIsEmpty.Msg, c.Path())
	}

	teamService := rt.Services.Team

	if err := teamService.EnableTeam(teamId); err != nil {
		log.Errorw("enable team failed", "error", err)
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.OPERATION, teamId)
	return nil
}

// disableTeam 禁用团队
func (rt *Router) disableTeam(c *fiber.Ctx) error {
	teamId := c.Params("teamId")
	if teamId == "" {
		return http.WithRepErrMsg(c, http.TeamIdIsEmpty.Code, http.TeamIdIsEmpty.Msg, c.Path())
	}

	teamService := rt.Services.Team

	if err := teamService.DisableTeam(teamId); err != nil {
		log.Errorw("disable team failed", "error", err)
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.OPERATION, teamId)
	return nil
}

// updateTeamStatistics 更新团队统计信息
func (rt *Router) updateTeamStatistics(c *fiber.Ctx) error {
	teamId := c.Params("teamId")
	if teamId == "" {
		return http.WithRepErrMsg(c, http.TeamIdIsEmpty.Code, http.TeamIdIsEmpty.Msg, c.Path())
	}

	teamService := rt.Services.Team

	if err := teamService.UpdateTeamStatistics(teamId); err != nil {
		log.Errorw("update team statistics failed", "error", err)
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.OPERATION, teamId)
	return nil
}
