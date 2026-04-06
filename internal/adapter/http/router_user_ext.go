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
	"github.com/arcentrix/arcentra/pkg/transport/http"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) userExtRoutes(r fiber.Router, auth fiber.Handler) {
	userExtGroup := r.Group("/users/:userID/ext", auth)
	userExtGroup.Get("/", rt.getUserExt)
	userExtGroup.Put("/", rt.updateUserExt)
	userExtGroup.Put("/timezone", rt.updateTimezone)
	userExtGroup.Put("/invitation", rt.updateInvitationStatus)
}

func (rt *Router) getUserExt(c *fiber.Ctx) error {
	userID := c.Params("userID")
	if userID == "" {
		return http.Err(c, http.BadRequest.Code, "user id is required")
	}

	ext, err := rt.ManageUser.GetUserExt(c.Context(), userID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, ext)
}

func (rt *Router) updateUserExt(c *fiber.Ctx) error {
	userID := c.Params("userID")
	if userID == "" {
		return http.Err(c, http.BadRequest.Code, "user id is required")
	}

	var req map[string]any
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request parameters")
	}

	if err := rt.ManageUser.UpdateUserExt(c.Context(), userID, req); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

func (rt *Router) updateTimezone(c *fiber.Ctx) error {
	userID := c.Params("userID")
	if userID == "" {
		return http.Err(c, http.BadRequest.Code, "user id is required")
	}

	var req struct {
		Timezone string `json:"timezone"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request parameters")
	}
	if req.Timezone == "" {
		return http.Err(c, http.BadRequest.Code, "timezone is required")
	}

	if err := rt.ManageUser.UpdateTimezone(c.Context(), userID, req.Timezone); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

func (rt *Router) updateInvitationStatus(c *fiber.Ctx) error {
	userID := c.Params("userID")
	if userID == "" {
		return http.Err(c, http.BadRequest.Code, "user id is required")
	}

	var req struct {
		Status string `json:"status"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request parameters")
	}
	if req.Status == "" {
		return http.Err(c, http.BadRequest.Code, "status is required")
	}

	if err := rt.ManageUser.UpdateInvitationStatus(c.Context(), userID, req.Status); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}
