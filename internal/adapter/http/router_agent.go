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

package http

import (
	"github.com/arcentrix/arcentra/internal/case/agent"
	"github.com/arcentrix/arcentra/pkg/transport/http"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) agentRoutes(r fiber.Router, auth fiber.Handler) {
	g := r.Group("/agent", auth)
	g.Post("", rt.createAgent)
	g.Get("", rt.listAgent)
	g.Get("/statistics", rt.getAgentStatistics)
	g.Get("/:agentId", rt.getAgent)
	g.Put("/:agentId", rt.updateAgent)
	g.Delete("/:agentId", rt.deleteAgent)
}

func (rt *Router) createAgent(c *fiber.Ctx) error {
	var req struct {
		AgentName string            `json:"agentName"`
		Labels    map[string]string `json:"labels"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}
	out, err := rt.RegisterAgent.Execute(c.Context(), agent.RegisterAgentInput{
		AgentName: req.AgentName,
		Labels:    req.Labels,
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}
	return http.Detail(c, out)
}

func (rt *Router) listAgent(c *fiber.Ctx) error {
	pageNum := rt.HTTP.QueryInt(c, "pageNum")
	if pageNum <= 0 {
		pageNum = 1
	}
	pageSize := rt.HTTP.QueryInt(c, "pageSize")
	if pageSize <= 0 {
		pageSize = 10
	}

	out, err := rt.ListAgents.Execute(c.Context(), agent.ListAgentsInput{Page: pageNum, Size: pageSize})
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}
	return http.Detail(c, map[string]any{
		"agents": out.Agents, "count": out.Total, "pageNum": pageNum, "pageSize": pageSize,
	})
}

func (rt *Router) getAgentStatistics(c *fiber.Ctx) error {
	out, err := rt.AgentStatistics.Execute(c.Context())
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}
	return http.Detail(c, map[string]any{"total": out.Total, "online": out.Online, "offline": out.Offline})
}

func (rt *Router) getAgent(c *fiber.Ctx) error {
	agentID := c.Params("agentId")
	if agentID == "" {
		return http.Err(c, http.BadRequest.Code, "agent id is required")
	}
	agent, err := rt.GetAgent.Execute(c.Context(), agentID)
	if err != nil {
		return http.Err(c, http.NotFound.Code, "agent not found")
	}
	return http.Detail(c, agent)
}

func (rt *Router) updateAgent(c *fiber.Ctx) error {
	agentID := c.Params("agentId")
	if agentID == "" {
		return http.Err(c, http.BadRequest.Code, "agent id is required")
	}
	var req struct {
		AgentName *string           `json:"agentName,omitempty"`
		Labels    map[string]string `json:"labels,omitempty"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}
	if err := rt.UpdateAgent.Execute(c.Context(), agentID, agent.UpdateAgentInput{
		AgentName: req.AgentName, Labels: req.Labels,
	}); err != nil {
		return http.Err(c, http.NotFound.Code, "agent not found")
	}
	updated, err := rt.GetAgent.Execute(c.Context(), agentID)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}
	return http.Detail(c, updated)
}

func (rt *Router) deleteAgent(c *fiber.Ctx) error {
	agentID := c.Params("agentId")
	if agentID == "" {
		return http.Err(c, http.BadRequest.Code, "agent id is required")
	}
	if err := rt.DeleteAgent.Execute(c.Context(), agentID); err != nil {
		return http.Err(c, http.NotFound.Code, "agent not found")
	}
	return http.Operation(c)
}
