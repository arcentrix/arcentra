package router

import (
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/consts"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/gofiber/fiber/v2"
)

// Sensitive endpoints retain the existing session verifier, then reconstruct
// the user and permissions from primary storage. Cached menus or request-supplied
// user, team, scope and attribution values never grant administrative authority.
func sensitiveHTTPStatus(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	err := c.Next()
	if err == nil && c.Response().StatusCode() == 200 && c.Locals("sensitiveAuthorized") != true {
		c.Status(fiber.StatusUnauthorized)
	}
	return err
}

func sensitiveError(c *fiber.Ctx, status int, message string) error {
	return apphttp.Err(c.Status(status), status, message)
}

func (rt *Router) liveSensitiveUser() fiber.Handler {
	return func(c *fiber.Ctx) error {
		claims, ok := c.Locals("claims").(*jwt.AuthClaims)
		if !ok || claims == nil || claims.UserID == "" {
			return sensitiveError(c, 401, "authentication is required")
		}
		if rt.Services == nil || rt.Services.Repos() == nil {
			return sensitiveError(c, 503, "authorization is unavailable")
		}
		subject, err := currentApprovalSubject(c.UserContext(), rt.Services.Repos(), claims.UserID)
		if err != nil {
			return sensitiveError(c, 403, "current user access denied")
		}
		c.Locals("sensitiveSubject", subject)
		c.Locals("sensitiveAuthorized", true)
		return c.Next()
	}
}

func (rt *Router) requirePlatform(c *fiber.Ctx, actions ...string) error {
	subject, ok := c.Locals("sensitiveSubject").(authz.Subject)
	if !ok || subject.UserID == "" || subject.IsAgent || rt.Services == nil {
		return sensitiveError(c, 403, "platform permission is required")
	}
	checker, err := authz.NewLiveAuthorizer(c.UserContext(), rt.Services.Repos())
	if err != nil {
		return sensitiveError(c, 503, "authorization is unavailable")
	}
	for _, action := range actions {
		allowed, err := checker.Check(c.UserContext(), subject, action, authz.ResourceRef{})
		if err != nil {
			return sensitiveError(c, 503, "authorization is unavailable")
		}
		if !allowed {
			return sensitiveError(c, 403, "platform permission is required")
		}
	}
	return nil
}

func (rt *Router) platformAccess(actions ...string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if err := rt.requirePlatform(c, actions...); err != nil || c.Response().StatusCode() != 200 {
			return err
		}
		return c.Next()
	}
}

func (rt *Router) selfOrUserAdmin() fiber.Handler {
	return func(c *fiber.Ctx) error {
		subject, ok := c.Locals("sensitiveSubject").(authz.Subject)
		if !ok || subject.UserID != sensitiveTargetUserID(c) {
			if err := rt.requirePlatform(c, consts.PermOrgManageMember); err != nil || c.Response().StatusCode() != 200 {
				return err
			}
		}
		return c.Next()
	}
}

func sensitiveTargetUserID(c *fiber.Ctx) string {
	id := c.Params("userID")
	if id == "me" {
		if subject, ok := c.Locals("sensitiveSubject").(authz.Subject); ok {
			return subject.UserID
		}
		return ""
	}
	return id
}
