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
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/gofiber/fiber/v2"
)

// approvalRouter registers approval-related routes.
func (rt *Router) approvalRouter(r fiber.Router, auth fiber.Handler) {
	g := r.Group("/approvals")
	{
		g.Get("/", auth, rt.listApprovals)
		g.Get("/:id", rt.getApproval)
		g.Post("/:id/approve", rt.approveRequest)
		g.Post("/:id/reject", rt.rejectRequest)
	}
}

// getApproval returns a single approval request by ID.
func (rt *Router) getApproval(c *fiber.Ctx) error {
	approvalID := c.Params("id")
	if approvalID == "" {
		return http.Err(c, http.BadRequest.Code, "approval ID is required")
	}
	req, err := rt.Services.Approval.GetApproval(c.Context(), approvalID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, req)
}

type approvalActionRequest struct {
	ApprovedBy string `json:"approvedBy"`
	Reason     string `json:"reason"`
}

// approveRequest approves a pending approval request.
func (rt *Router) approveRequest(c *fiber.Ctx) error {
	approvalID := c.Params("id")
	if approvalID == "" {
		return http.Err(c, http.BadRequest.Code, "approval ID is required")
	}

	var body approvalActionRequest
	if err := c.BodyParser(&body); err != nil {
		// Callback URLs from IM may not have a body — allow empty.
		body = approvalActionRequest{}
	}

	approvedBy := body.ApprovedBy
	if approvedBy == "" {
		approvedBy = c.Query("user", "system")
	}

	if err := rt.Services.Approval.Approve(c.Context(), approvalID, approvedBy, body.Reason); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, map[string]string{"status": "approved"})
}

// rejectRequest rejects a pending approval request.
func (rt *Router) rejectRequest(c *fiber.Ctx) error {
	approvalID := c.Params("id")
	if approvalID == "" {
		return http.Err(c, http.BadRequest.Code, "approval ID is required")
	}

	var body approvalActionRequest
	if err := c.BodyParser(&body); err != nil {
		body = approvalActionRequest{}
	}

	rejectedBy := body.ApprovedBy
	if rejectedBy == "" {
		rejectedBy = c.Query("user", "system")
	}

	if err := rt.Services.Approval.Reject(c.Context(), approvalID, rejectedBy, body.Reason); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, map[string]string{"status": "rejected"})
}

// listApprovals lists approval requests, optionally filtered by pipeline_run_id.
func (rt *Router) listApprovals(c *fiber.Ctx) error {
	pipelineRunID := c.Query("pipeline_run_id")
	if pipelineRunID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline_run_id query parameter is required")
	}
	reqs, err := rt.Services.Approval.ListByPipelineRun(c.Context(), pipelineRunID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, reqs)
}
