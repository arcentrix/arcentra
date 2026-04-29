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
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) auditLogRouter(r fiber.Router, authMiddleware fiber.Handler, subjectMiddleware fiber.Handler) {
	g := r.Group("/audit-logs")
	{
		g.Get("/", authMiddleware, subjectMiddleware, rt.permission("audit:read", middleware.ResolvePlatformScope()), rt.listAuditLogs)
	}
}

func (rt *Router) listAuditLogs(c *fiber.Ctx) error {
	pageNum := rt.HTTP.QueryInt(c, "pageNum")
	pageSize := rt.HTTP.QueryInt(c, "pageSize")
	action := c.Query("action")
	actorUserID := c.Query("actorUserId")

	list, total, err := rt.Services.AuditLog.List(c.Context(), action, actorUserID, pageNum, pageSize)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, map[string]any{
		"list":     list,
		"total":    total,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	})
}
