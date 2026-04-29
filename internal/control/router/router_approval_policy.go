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

package router

import (
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/auth"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
	"gorm.io/datatypes"
)

// approvalPolicyRouter 注册审批策略路由
func (rt *Router) approvalPolicyRouter(r fiber.Router, authMW fiber.Handler, subject fiber.Handler) {
	g := r.Group("/approval-policies", authMW, subject)
	{
		g.Get("/", rt.permission("approval:read", middleware.ResolvePlatformScope()), rt.listApprovalPolicies)
		g.Post("/", rt.permission("approval:configure_policy", middleware.ResolvePlatformScope()), rt.createApprovalPolicy)
		g.Get("/:id", rt.permission("approval:read", middleware.ResolvePlatformScope()), rt.getApprovalPolicy)
		g.Put("/:id", rt.permission("approval:configure_policy", middleware.ResolvePlatformScope()), rt.updateApprovalPolicy)
		g.Delete("/:id", rt.permission("approval:configure_policy", middleware.ResolvePlatformScope()), rt.deleteApprovalPolicy)
	}
}

type approvalPolicyRequest struct {
	PolicyID       string         `json:"policyId,omitempty"`
	ScopeType      string         `json:"scopeType"`
	ScopeID        string         `json:"scopeId"`
	Name           string         `json:"name"`
	MatchRule      datatypes.JSON `json:"matchRule"`
	ApproverRule   datatypes.JSON `json:"approverRule"`
	MinApprovals   int            `json:"minApprovals"`
	Mode           string         `json:"mode"`
	TimeoutSeconds int            `json:"timeoutSeconds"`
	Enabled        *int           `json:"enabled,omitempty"`
	Description    string         `json:"description"`
}

func (rt *Router) listApprovalPolicies(c *fiber.Ctx) error {
	scopeType := c.Query("scopeType")
	scopeID := c.Query("scopeId")
	if scopeType == "" {
		return http.Err(c, http.BadRequest.Code, "scopeType is required")
	}
	policies, err := rt.Services.ApprovalPolicy.ListByScope(c.Context(), scopeType, scopeID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, fiber.Map{"policies": policies, "count": len(policies)})
}

func (rt *Router) createApprovalPolicy(c *fiber.Ctx) error {
	var req approvalPolicyRequest
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}
	if req.Name == "" || req.ScopeType == "" {
		return http.Err(c, http.BadRequest.Code, "name and scopeType are required")
	}

	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		return http.Err(c, http.Unauthorized.Code, http.Unauthorized.Msg)
	}

	policy := &model.ApprovalPolicy{
		PolicyID:       req.PolicyID,
		ScopeType:      req.ScopeType,
		ScopeID:        req.ScopeID,
		Name:           req.Name,
		MatchRule:      defaultJSON(req.MatchRule),
		ApproverRule:   defaultJSON(req.ApproverRule),
		MinApprovals:   req.MinApprovals,
		Mode:           req.Mode,
		TimeoutSeconds: req.TimeoutSeconds,
		Description:    req.Description,
		CreatedBy:      claims.UserID,
	}
	if req.Enabled != nil {
		policy.Enabled = *req.Enabled
	}
	if err := rt.Services.ApprovalPolicy.Create(c.Context(), policy); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, policy)
}

func (rt *Router) getApprovalPolicy(c *fiber.Ctx) error {
	policyID := c.Params("id")
	if policyID == "" {
		return http.Err(c, http.BadRequest.Code, "policy id is required")
	}
	policy, err := rt.Services.ApprovalPolicy.Get(c.Context(), policyID)
	if err != nil {
		return http.Err(c, http.NotFound.Code, "approval policy not found")
	}
	return http.Detail(c, policy)
}

func (rt *Router) updateApprovalPolicy(c *fiber.Ctx) error {
	policyID := c.Params("id")
	if policyID == "" {
		return http.Err(c, http.BadRequest.Code, "policy id is required")
	}
	var req approvalPolicyRequest
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}
	updates := map[string]any{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.ScopeType != "" {
		updates["scope_type"] = req.ScopeType
	}
	if req.ScopeID != "" {
		updates["scope_id"] = req.ScopeID
	}
	if len(req.MatchRule) > 0 {
		updates["match_rule"] = req.MatchRule
	}
	if len(req.ApproverRule) > 0 {
		updates["approver_rule"] = req.ApproverRule
	}
	if req.MinApprovals > 0 {
		updates["min_approvals"] = req.MinApprovals
	}
	if req.Mode != "" {
		updates["mode"] = req.Mode
	}
	if req.TimeoutSeconds > 0 {
		updates["timeout_seconds"] = req.TimeoutSeconds
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.Description != "" {
		updates["description"] = req.Description
	}
	if err := rt.Services.ApprovalPolicy.Update(c.Context(), policyID, updates); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	policy, err := rt.Services.ApprovalPolicy.Get(c.Context(), policyID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, policy)
}

func (rt *Router) deleteApprovalPolicy(c *fiber.Ctx) error {
	policyID := c.Params("id")
	if policyID == "" {
		return http.Err(c, http.BadRequest.Code, "policy id is required")
	}
	if err := rt.Services.ApprovalPolicy.Delete(c.Context(), policyID); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Operation(c)
}

func defaultJSON(in datatypes.JSON) datatypes.JSON {
	if len(in) > 0 {
		return in
	}
	return datatypes.JSON("{}")
}
