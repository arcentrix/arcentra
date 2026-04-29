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
	"github.com/arcentrix/arcentra/pkg/auth"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) meRouter(r fiber.Router, authMW fiber.Handler, subjectMW fiber.Handler) {
	g := r.Group("/me")
	{
		g.Get("/menus", authMW, subjectMW, rt.getMyMenus)
		g.Get("/routes", authMW, subjectMW, rt.getMyRoutes)
		g.Get("/effective-actions", authMW, subjectMW, rt.getMyEffectiveActions)
	}
}

func (rt *Router) getMyMenus(c *fiber.Ctx) error {
	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		return http.Err(c, http.Unauthorized.Code, http.Unauthorized.Msg)
	}
	scopeType := c.Query("scopeType")
	scopeID := c.Query("scopeId")
	menus, err := rt.Services.Me.GetMenus(c.Context(), claims.UserID, scopeType, scopeID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, menus)
}

func (rt *Router) getMyRoutes(c *fiber.Ctx) error {
	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		return http.Err(c, http.Unauthorized.Code, http.Unauthorized.Msg)
	}
	scopeType := c.Query("scopeType")
	scopeID := c.Query("scopeId")
	routes, err := rt.Services.Me.GetRoutes(c.Context(), claims.UserID, scopeType, scopeID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, routes)
}

func (rt *Router) getMyEffectiveActions(c *fiber.Ctx) error {
	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		return http.Err(c, http.Unauthorized.Code, http.Unauthorized.Msg)
	}
	scopeType := c.Query("scopeType")
	scopeID := c.Query("scopeId")
	actions, err := rt.Services.Me.GetEffectiveActions(c.Context(), claims.UserID, scopeType, scopeID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, actions)
}
