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

func (rt *Router) teamRoutes(r fiber.Router, authMW fiber.Handler) {
	teamGroup := r.Group("/team")
	teamGroup.Post("/create", authMW, rt.createTeam)
	teamGroup.Put("/:teamID", authMW, rt.updateTeam)
	teamGroup.Delete("/:teamID", authMW, rt.deleteTeam)
	teamGroup.Get("/:teamID", authMW, rt.getTeamByID)
	teamGroup.Get("/list", authMW, rt.listTeams)
	teamGroup.Get("/org/:orgId", authMW, rt.getTeamsByOrgID)
	teamGroup.Get("/:teamID/subteams", authMW, rt.getSubTeams)
	teamGroup.Get("/user/myteams", authMW, rt.getUserTeams)
	teamGroup.Post("/:teamID/enable", authMW, rt.enableTeam)
	teamGroup.Post("/:teamID/disable", authMW, rt.disableTeam)
	teamGroup.Post("/:teamID/statistics", authMW, rt.updateTeamStatistics)
}

func (rt *Router) createTeam(c *fiber.Ctx) error {
	var req struct {
		OrgID       string `json:"orgId"`
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
		Visibility  int    `json:"visibility"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		return http.Err(c, http.AuthenticationFailed.Code, http.AuthenticationFailed.Msg)
	}

	result, err := rt.ManageTeam.CreateTeamFull(
		c.Context(),
		req.OrgID, req.Name, req.DisplayName, req.Description, req.Visibility, claims.UserID,
	)
	if err != nil {
		log.Errorw("create team failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, result)
}

func (rt *Router) updateTeam(c *fiber.Ctx) error {
	teamID := c.Params("teamID")
	if teamID == "" {
		return http.Err(c, http.TeamIDIsEmpty.Code, http.TeamIDIsEmpty.Msg)
	}

	var req map[string]any
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	if err := rt.ManageTeam.UpdateTeam(c.Context(), teamID, req); err != nil {
		log.Errorw("update team failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	result, err := rt.ManageTeam.GetTeam(c.Context(), teamID)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, result)
}

func (rt *Router) deleteTeam(c *fiber.Ctx) error {
	teamID := c.Params("teamID")
	if teamID == "" {
		return http.Err(c, http.TeamIDIsEmpty.Code, http.TeamIDIsEmpty.Msg)
	}

	if err := rt.ManageTeam.DeleteTeam(c.Context(), teamID); err != nil {
		log.Errorw("delete team failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Operation(c)
}

func (rt *Router) getTeamByID(c *fiber.Ctx) error {
	teamID := c.Params("teamID")
	if teamID == "" {
		return http.Err(c, http.TeamIDIsEmpty.Code, http.TeamIDIsEmpty.Msg)
	}

	result, err := rt.ManageTeam.GetTeam(c.Context(), teamID)
	if err != nil {
		log.Errorw("get team by id failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, result)
}

func (rt *Router) listTeams(c *fiber.Ctx) error {
	orgID := c.Query("orgId")
	name := c.Query("name")
	parentTeamID := c.Query("parentTeamId")

	var visibility, isEnabled *int
	if v := c.Query("visibility"); v != "" {
		if vi, err := strconv.Atoi(v); err == nil {
			visibility = &vi
		}
	}
	if v := c.Query("isEnabled"); v != "" {
		if ei, err := strconv.Atoi(v); err == nil {
			isEnabled = &ei
		}
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize", "20"))

	result, err := rt.ManageTeam.ListTeams(c.Context(), orgID, name, parentTeamID, visibility, isEnabled, page, pageSize)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

func (rt *Router) getTeamsByOrgID(c *fiber.Ctx) error {
	orgID := c.Params("orgId")
	if orgID == "" {
		return http.Err(c, http.OrgIDIsEmpty.Code, http.OrgIDIsEmpty.Msg)
	}

	result, err := rt.ManageTeam.GetTeamsByOrgID(c.Context(), orgID)
	if err != nil {
		log.Errorw("get teams by org id failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, result)
}

func (rt *Router) getSubTeams(c *fiber.Ctx) error {
	teamID := c.Params("teamID")
	if teamID == "" {
		return http.Err(c, http.TeamIDIsEmpty.Code, http.TeamIDIsEmpty.Msg)
	}

	result, err := rt.ManageTeam.GetSubTeams(c.Context(), teamID)
	if err != nil {
		log.Errorw("get sub teams failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, result)
}

func (rt *Router) getUserTeams(c *fiber.Ctx) error {
	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		return http.Err(c, http.AuthenticationFailed.Code, http.AuthenticationFailed.Msg)
	}

	result, err := rt.ManageTeam.GetTeamsByUserID(c.Context(), claims.UserID)
	if err != nil {
		log.Errorw("get teams by user id failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, result)
}

func (rt *Router) enableTeam(c *fiber.Ctx) error {
	teamID := c.Params("teamID")
	if teamID == "" {
		return http.Err(c, http.TeamIDIsEmpty.Code, http.TeamIDIsEmpty.Msg)
	}

	if err := rt.ManageTeam.EnableTeam(c.Context(), teamID); err != nil {
		log.Errorw("enable team failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Operation(c)
}

func (rt *Router) disableTeam(c *fiber.Ctx) error {
	teamID := c.Params("teamID")
	if teamID == "" {
		return http.Err(c, http.TeamIDIsEmpty.Code, http.TeamIDIsEmpty.Msg)
	}

	if err := rt.ManageTeam.DisableTeam(c.Context(), teamID); err != nil {
		log.Errorw("disable team failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Operation(c)
}

func (rt *Router) updateTeamStatistics(c *fiber.Ctx) error {
	teamID := c.Params("teamID")
	if teamID == "" {
		return http.Err(c, http.TeamIDIsEmpty.Code, http.TeamIDIsEmpty.Msg)
	}

	if err := rt.ManageTeam.UpdateTeamStatistics(c.Context(), teamID); err != nil {
		log.Errorw("update team statistics failed", "error", err)
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Operation(c)
}
