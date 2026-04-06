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
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) userExtRouter(r fiber.Router, auth fiber.Handler) {
	userExtGroup := r.Group("/users/:userID/ext", auth)
	{
		userExtGroup.Get("/", rt.getUserExt)                       // GET /users/:userID/ext - get user ext info
		userExtGroup.Put("/", rt.updateUserExt)                    // PUT /users/:userID/ext - update user ext info
		userExtGroup.Put("/timezone", rt.updateTimezone)           // PUT /users/:userID/ext/timezone - update timezone
		userExtGroup.Put("/invitation", rt.updateInvitationStatus) // PUT /users/:userID/ext/invitation - update invitation status
	}
}

// getUserExt gets user ext information
func (rt *Router) getUserExt(c *fiber.Ctx) error {
	userID := c.Params("userID")
	if userID == "" {
		return http.Err(c, http.BadRequest.Code, "user id is required")
	}

	userExtService := rt.Services.UserExt

	ext, err := userExtService.GetUserExt(c.Context(), userID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, ext)
}

// updateUserExt updates user ext information
func (rt *Router) updateUserExt(c *fiber.Ctx) error {
	userID := c.Params("userID")
	if userID == "" {
		return http.Err(c, http.BadRequest.Code, "user id is required")
	}

	var ext model.UserExt
	if err := c.BodyParser(&ext); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request parameters")
	}

	userExtService := rt.Services.UserExt

	if err := userExtService.UpdateUserExt(c.Context(), userID, &ext); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

// updateTimezone updates user timezone
func (rt *Router) updateTimezone(c *fiber.Ctx) error {
	userID := c.Params("userID")
	if userID == "" {
		return http.Err(c, http.BadRequest.Code, "user id is required")
	}

	type TimezoneReq struct {
		Timezone string `json:"timezone"`
	}

	var req TimezoneReq
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request parameters")
	}

	if req.Timezone == "" {
		return http.Err(c, http.BadRequest.Code, "timezone is required")
	}

	userExtService := rt.Services.UserExt

	if err := userExtService.UpdateTimezone(c.Context(), userID, req.Timezone); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

// updateInvitationStatus updates invitation status
func (rt *Router) updateInvitationStatus(c *fiber.Ctx) error {
	userID := c.Params("userID")
	if userID == "" {
		return http.Err(c, http.BadRequest.Code, "user id is required")
	}

	type InvitationStatusReq struct {
		Status string `json:"status"` // pending, accepted, expired, revoked
	}

	var req InvitationStatusReq
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request parameters")
	}

	if req.Status == "" {
		return http.Err(c, http.BadRequest.Code, "status is required")
	}

	userExtService := rt.Services.UserExt

	if err := userExtService.UpdateInvitationStatus(c.Context(), userID, req.Status); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}
