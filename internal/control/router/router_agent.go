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

func (rt *Router) agentRouter(r fiber.Router, auth fiber.Handler, subject fiber.Handler) {
	agentGroup := r.Group("/agent", auth)
	{
		// RESTful API
		agentGroup.Get("", subject, rt.permission("agent:read", middleware.ResolvePlatformScope()), rt.listAgent)
		agentGroup.Get("/statistics", subject, rt.permission("agent:read", middleware.ResolvePlatformScope()), rt.getAgentStatistics)
		agentGroup.Get("/:agentId", subject, rt.permission("agent:read", middleware.ResolvePlatformScope()), rt.getAgent)
		agentGroup.Put("/:agentId", subject, rt.permission("agent:update_config", middleware.ResolvePlatformScope()), rt.updateAgent)
		agentGroup.Delete("/:agentId", subject, rt.permission("agent:delete", middleware.ResolvePlatformScope()), rt.deleteAgent)
		agentGroup.Put("/:agentId/approve", subject, rt.permission("agent:approve", middleware.ResolvePlatformScope()), rt.approveAgent)
	}
}

// approveAgent PUT /agent/:agentId/approve - approve an agent
func (rt *Router) approveAgent(c *fiber.Ctx) error {
	agentID := c.Params("agentId")
	if agentID == "" {
		return http.Err(c, http.BadRequest.Code, "agent id is required")
	}

	if err := rt.Services.Agent.ApproveAgent(c.Context(), agentID); err != nil {
		return http.Err(c, http.Failed.Code, "failed to approve agent")
	}

	return http.Operation(c)
}

// listAgent GET /agent - list agents with pagination
func (rt *Router) listAgent(c *fiber.Ctx) error {
	agentLogic := rt.Services.Agent

	pageNum := rt.HTTP.QueryInt(c, "pageNum")
	if pageNum <= 0 {
		pageNum = 1
	}
	pageSize := rt.HTTP.QueryInt(c, "pageSize")
	if pageSize <= 0 {
		pageSize = 10
	}

	agents, count, err := agentLogic.ListAgent(c.Context(), pageNum, pageSize)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	result := make(map[string]any)
	result["agents"] = agents
	result["count"] = count
	result["pageNum"] = pageNum
	result["pageSize"] = pageSize
	return http.Detail(c, result)
}

// getAgentStatistics GET /agent/statistics - get agent statistics
func (rt *Router) getAgentStatistics(c *fiber.Ctx) error {
	agentLogic := rt.Services.Agent

	total, online, offline, err := agentLogic.GetAgentStatistics(c.Context())
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	result := make(map[string]any)
	result["total"] = total
	result["online"] = online
	result["offline"] = offline

	return http.Detail(c, result)
}

// getAgent GET /agent/:agentId - get agent by agentId
func (rt *Router) getAgent(c *fiber.Ctx) error {
	agentID := c.Params("agentId")
	if agentID == "" {
		return http.Err(c, http.BadRequest.Code, "agent id is required")
	}

	agentLogic := rt.Services.Agent
	agent, err := agentLogic.GetAgentByagentID(c.Context(), agentID)
	if err != nil {
		return http.Err(c, http.NotFound.Code, "agent not found")
	}

	return http.Detail(c, agent)
}

// updateAgent PUT /agent/:agentId - update agent
func (rt *Router) updateAgent(c *fiber.Ctx) error {
	agentID := c.Params("agentId")
	if agentID == "" {
		return http.Err(c, http.BadRequest.Code, "agent id is required")
	}

	var updateReq *model.UpdateAgentReq
	if err := c.BodyParser(&updateReq); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	agentLogic := rt.Services.Agent
	if err := agentLogic.UpdateAgentByagentID(c.Context(), agentID, updateReq); err != nil {
		return http.Err(c, http.NotFound.Code, "agent not found")
	}

	// Get updated agent
	updatedAgent, err := agentLogic.GetAgentByagentID(c.Context(), agentID)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, updatedAgent)
}

// deleteAgent DELETE /agent/:agentId - delete agent
func (rt *Router) deleteAgent(c *fiber.Ctx) error {
	agentID := c.Params("agentId")
	if agentID == "" {
		return http.Err(c, http.BadRequest.Code, "agent id is required")
	}

	agentLogic := rt.Services.Agent
	if err := agentLogic.DeleteAgentByagentID(c.Context(), agentID); err != nil {
		return http.Err(c, http.NotFound.Code, "agent not found")
	}

	return http.Operation(c)
}
