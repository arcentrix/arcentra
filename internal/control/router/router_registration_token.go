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
	"strconv"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/auth"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) registrationTokenRouter(r fiber.Router, authMiddleware fiber.Handler) {
	group := r.Group("/registration-tokens", authMiddleware)
	{
		group.Post("", rt.createRegistrationToken)       // POST /registration-tokens
		group.Get("", rt.listRegistrationTokens)         // GET /registration-tokens
		group.Delete("/:id", rt.revokeRegistrationToken) // DELETE /registration-tokens/:id
	}
}

// createRegistrationToken POST /registration-tokens - create a new registration token
func (rt *Router) createRegistrationToken(c *fiber.Ctx) error {
	var req model.CreateRegistrationTokenReq
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	// Auto-populate createdBy from authenticated user
	if req.CreatedBy == "" {
		req.CreatedBy = auth.CurrentUserName(c, rt.HTTP.Auth.SecretKey, func(userID string) string {
			info, err := rt.Services.User.FetchUserInfo(userID)
			if err != nil || info == nil {
				return ""
			}
			return info.Username
		})
	}

	resp, err := rt.Services.RegistrationToken.GenerateToken(c.Context(), &req)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Detail(c, resp)
}

// listRegistrationTokens GET /registration-tokens - list registration tokens
func (rt *Router) listRegistrationTokens(c *fiber.Ctx) error {
	pageNum := rt.HTTP.QueryInt(c, "pageNum")
	if pageNum <= 0 {
		pageNum = 1
	}
	pageSize := rt.HTTP.QueryInt(c, "pageSize")
	if pageSize <= 0 {
		pageSize = 10
	}

	tokens, count, err := rt.Services.RegistrationToken.ListTokens(c.Context(), pageNum, pageSize)
	if err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	result := make(map[string]any)
	result["tokens"] = tokens
	result["count"] = count
	result["pageNum"] = pageNum
	result["pageSize"] = pageSize
	return http.Detail(c, result)
}

// revokeRegistrationToken DELETE /registration-tokens/:id - revoke a registration token
func (rt *Router) revokeRegistrationToken(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid token id")
	}

	if err := rt.Services.RegistrationToken.RevokeToken(c.Context(), id); err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.Operation(c)
}
