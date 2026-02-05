// Copyright 2025 Arcentra Team
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
	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) agentRouter(r fiber.Router, auth fiber.Handler) {
	agentGroup := r.Group("/agent", auth)
	{
		// RESTful API
		agentGroup.Post("", rt.createAgent)                  // POST /agent - create agent
		agentGroup.Get("", rt.listAgent)                     // GET /agent - list agents
		agentGroup.Get("/statistics", rt.getAgentStatistics) // GET /agent/statistics - get agent statistics
		agentGroup.Get("/:agentId", rt.getAgent)             // GET /agent/:agentId - get agent by agentId
		agentGroup.Put("/:agentId", rt.updateAgent)          // PUT /agent/:agentId - update agent
		agentGroup.Delete("/:agentId", rt.deleteAgent)       // DELETE /agent/:agentId - delete agent
	}
}

// createAgent POST /agent - create a new agent
func (rt *Router) createAgent(c *fiber.Ctx) error {
	var createAgentReq *model.CreateAgentReq
	agentLogic := rt.Services.Agent

	if err := c.BodyParser(&createAgentReq); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	agent, err := agentLogic.CreateAgent(createAgentReq)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.DETAIL, agent)
	return nil
}

// listAgent GET /agent - list agents with pagination
func (rt *Router) listAgent(c *fiber.Ctx) error {
	agentLogic := rt.Services.Agent

	pageNum := queryInt(c, "pageNum")
	if pageNum <= 0 {
		pageNum = 1
	}
	pageSize := queryInt(c, "pageSize")
	if pageSize <= 0 {
		pageSize = 10
	}

	agents, count, err := agentLogic.ListAgent(pageNum, pageSize)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	result := make(map[string]any)
	result["agents"] = agents
	result["count"] = count
	result["pageNum"] = pageNum
	result["pageSize"] = pageSize
	c.Locals(middleware.DETAIL, result)
	return nil
}

// getAgentStatistics GET /agent/statistics - get agent statistics
func (rt *Router) getAgentStatistics(c *fiber.Ctx) error {
	agentLogic := rt.Services.Agent

	total, online, offline, err := agentLogic.GetAgentStatistics()
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	result := make(map[string]any)
	result["total"] = total
	result["online"] = online
	result["offline"] = offline

	c.Locals(middleware.DETAIL, result)
	return nil
}

// getAgent GET /agent/:agentId - get agent by agentId
func (rt *Router) getAgent(c *fiber.Ctx) error {
	agentId := c.Params("agentId")
	if agentId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "agent id is required", c.Path())
	}

	agentLogic := rt.Services.Agent
	agent, err := agentLogic.GetAgentByAgentId(agentId)
	if err != nil {
		return http.WithRepErrMsg(c, http.NotFound.Code, "agent not found", c.Path())
	}

	c.Locals(middleware.DETAIL, agent)
	return nil
}

// updateAgent PUT /agent/:agentId - update agent
func (rt *Router) updateAgent(c *fiber.Ctx) error {
	agentId := c.Params("agentId")
	if agentId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "agent id is required", c.Path())
	}

	var updateReq *model.UpdateAgentReq
	if err := c.BodyParser(&updateReq); err != nil {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "invalid request body", c.Path())
	}

	agentLogic := rt.Services.Agent
	if err := agentLogic.UpdateAgentByAgentId(agentId, updateReq); err != nil {
		return http.WithRepErrMsg(c, http.NotFound.Code, "agent not found", c.Path())
	}

	// Get updated agent
	updatedAgent, err := agentLogic.GetAgentByAgentId(agentId)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.DETAIL, updatedAgent)
	return nil
}

// deleteAgent DELETE /agent/:agentId - delete agent
func (rt *Router) deleteAgent(c *fiber.Ctx) error {
	agentId := c.Params("agentId")
	if agentId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "agent id is required", c.Path())
	}

	agentLogic := rt.Services.Agent
	if err := agentLogic.DeleteAgentByAgentId(agentId); err != nil {
		return http.WithRepErrMsg(c, http.NotFound.Code, "agent not found", c.Path())
	}

	c.Locals(middleware.OPERATION, "delete agent")
	return nil
}
