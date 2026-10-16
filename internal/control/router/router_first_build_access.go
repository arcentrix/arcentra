// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/gofiber/fiber/v2"
)

// This narrow live-user path pins both subject reconstruction and subsequent
// authorization snapshots to primary storage. It intentionally does not change
// existing administrative, approval, or effective-actions middleware behavior.
func (rt *Router) firstBuildLiveUser(includeProjectCreateAudit bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		claims, ok := c.Locals("claims").(*jwt.AuthClaims)
		if !ok || claims == nil || claims.UserID == "" {
			return sensitiveError(c, fiber.StatusUnauthorized, "authentication is required")
		}
		if rt.Services == nil || rt.Services.Repos() == nil {
			return sensitiveError(c, fiber.StatusServiceUnavailable, "authorization is unavailable")
		}
		repositories, err := rt.Services.Repos().FirstBuildPrimaryRepositories(includeProjectCreateAudit)
		if err != nil {
			return sensitiveError(c, fiber.StatusServiceUnavailable, "authorization is unavailable")
		}
		subject, err := currentApprovalSubject(c.UserContext(), repositories, claims.UserID)
		if err != nil {
			return sensitiveError(c, fiber.StatusForbidden, "current user access denied")
		}
		c.Locals("firstBuildRepositories", repositories)
		c.Locals("sensitiveSubject", subject)
		c.Locals("sensitiveAuthorized", true)
		return c.Next()
	}
}

func firstBuildRepositories(c *fiber.Ctx) *repo.Repositories {
	repositories, _ := c.Locals("firstBuildRepositories").(*repo.Repositories)
	return repositories
}
