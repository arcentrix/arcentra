// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/consts"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/gofiber/fiber/v2"
)

// Project creation rechecks the same permission against live user/team/grant
// state. The supplied orgId is only the requested target, never membership
// evidence. Other project routes retain their existing authorization behavior.
func (rt *Router) liveCreateProjectAccess() fiber.Handler {
	return func(c *fiber.Ctx) error {
		subject, ok := c.Locals("sensitiveSubject").(authz.Subject)
		if !ok || subject.UserID == "" || subject.IsAgent {
			return sensitiveError(c, fiber.StatusForbidden, "current user access denied")
		}
		resource, err := resolveCreateProjectScope()(c)
		if err != nil {
			return sensitiveError(c, fiber.StatusBadRequest, apphttp.BadRequest.Msg)
		}
		repositories := firstBuildRepositories(c)
		if repositories == nil {
			return sensitiveError(c, fiber.StatusServiceUnavailable, "authorization is unavailable")
		}
		checker, err := authz.NewLiveAuthorizer(c.UserContext(), repositories)
		if err != nil {
			return sensitiveError(c, fiber.StatusServiceUnavailable, "authorization is unavailable")
		}
		allowed, err := checker.Check(c.UserContext(), subject, consts.PermOrgCreateProject, resource)
		if err != nil {
			return sensitiveError(c, fiber.StatusServiceUnavailable, "authorization is unavailable")
		}
		if !allowed {
			return sensitiveError(c, fiber.StatusForbidden, apphttp.Forbidden.Msg)
		}
		return c.Next()
	}
}
