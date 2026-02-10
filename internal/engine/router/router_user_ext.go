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
	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) userExtRouter(r fiber.Router, auth fiber.Handler) {
	userExtGroup := r.Group("/users/:userId/ext", auth)
	{
		userExtGroup.Get("/", rt.getUserExt)                       // GET /users/:userId/ext - get user ext info
		userExtGroup.Put("/", rt.updateUserExt)                    // PUT /users/:userId/ext - update user ext info
		userExtGroup.Put("/timezone", rt.updateTimezone)           // PUT /users/:userId/ext/timezone - update timezone
		userExtGroup.Put("/invitation", rt.updateInvitationStatus) // PUT /users/:userId/ext/invitation - update invitation status
	}
}

// getUserExt gets user ext information
func (rt *Router) getUserExt(c *fiber.Ctx) error {
	userId := c.Params("userId")
	if userId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "user id is required", c.Path())
	}

	userExtService := rt.Services.UserExt

	ext, err := userExtService.GetUserExt(userId)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, ext)
	return nil
}

// updateUserExt updates user ext information
func (rt *Router) updateUserExt(c *fiber.Ctx) error {
	userId := c.Params("userId")
	if userId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "user id is required", c.Path())
	}

	var ext model.UserExt
	if err := c.BodyParser(&ext); err != nil {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "invalid request parameters", c.Path())
	}

	userExtService := rt.Services.UserExt

	if err := userExtService.UpdateUserExt(userId, &ext); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.OPERATION, "update user ext")
	return nil
}

// updateTimezone updates user timezone
func (rt *Router) updateTimezone(c *fiber.Ctx) error {
	userId := c.Params("userId")
	if userId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "user id is required", c.Path())
	}

	type TimezoneReq struct {
		Timezone string `json:"timezone"`
	}

	var req TimezoneReq
	if err := c.BodyParser(&req); err != nil {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "invalid request parameters", c.Path())
	}

	if req.Timezone == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "timezone is required", c.Path())
	}

	userExtService := rt.Services.UserExt

	if err := userExtService.UpdateTimezone(userId, req.Timezone); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.OPERATION, "update user timezone")
	return nil
}

// updateInvitationStatus updates invitation status
func (rt *Router) updateInvitationStatus(c *fiber.Ctx) error {
	userId := c.Params("userId")
	if userId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "user id is required", c.Path())
	}

	type InvitationStatusReq struct {
		Status string `json:"status"` // pending, accepted, expired, revoked
	}

	var req InvitationStatusReq
	if err := c.BodyParser(&req); err != nil {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "invalid request parameters", c.Path())
	}

	if req.Status == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "status is required", c.Path())
	}

	userExtService := rt.Services.UserExt

	if err := userExtService.UpdateInvitationStatus(userId, req.Status); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.OPERATION, "update invitation status")
	return nil
}
