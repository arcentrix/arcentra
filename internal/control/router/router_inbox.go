// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/arcentrix/arcentra/internal/control/repo"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"strconv"
	"time"
)

func (rt *Router) inboxRouter(r fiber.Router, auth fiber.Handler) {
	g := r.Group("/inbox", sensitiveHTTPStatus, auth)
	g.Get("/", rt.inboxHandler("list"))
	g.Get("/unread-count", rt.inboxHandler("count"))
	g.Post("/:id/read", rt.inboxHandler("read"))
}
func (rt *Router) inboxHandler(action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Set("Cache-Control", "no-store")
		claims, ok := c.Locals("claims").(*jwt.AuthClaims)
		if !ok || claims == nil || claims.UserID == "" {
			return sensitiveError(c, 401, "authentication is required")
		}
		if rt.Services == nil || rt.Services.Inbox == nil {
			return sensitiveError(c, 503, "inbox unavailable")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 5*time.Second)
		defer cancel()
		var result any
		var err error
		switch action {
		case "list":
			invalid := false
			seen := map[string]bool{}
			c.Context().QueryArgs().VisitAll(func(key, value []byte) {
				if seen[string(key)] {
					invalid = true
				}
				seen[string(key)] = true
				if string(key) != "filter" && string(key) != "limit" && string(key) != "cursor" {
					invalid = true
				}
			})
			if invalid {
				return sensitiveError(c, 400, "invalid inbox request")
			}
			filter := c.Query("filter", "all")
			limit := 20
			if v := c.Query("limit"); v != "" {
				limit, err = strconv.Atoi(v)
				if err != nil {
					return sensitiveError(c, 400, "invalid inbox request")
				}
			}
			result, err = rt.Services.Inbox.List(ctx, claims.UserID, filter, c.Query("cursor"), limit)
		case "count":
			if len(c.Context().QueryArgs().String()) > 0 {
				return sensitiveError(c, 400, "invalid inbox request")
			}
			result, err = rt.Services.Inbox.UnreadCount(ctx, claims.UserID)
		case "read":
			if !emptyInboxReadBody(c.Body()) || len(c.Context().QueryArgs().String()) > 0 {
				return sensitiveError(c, 400, "invalid inbox request")
			}
			result, err = rt.Services.Inbox.MarkRead(ctx, claims.UserID, c.Params("id"))
		}
		if err != nil {
			switch {
			case errors.Is(err, repo.ErrInboxInvalid):
				return sensitiveError(c, 400, "invalid inbox request")
			case errors.Is(err, repo.ErrInboxCursor):
				return sensitiveError(c, 400, "invalid inbox cursor")
			case errors.Is(err, repo.ErrInboxForbidden):
				return sensitiveError(c, 403, "current inbox access denied")
			case errors.Is(err, gorm.ErrRecordNotFound):
				return sensitiveError(c, 404, "inbox entry not found")
			default:
				return sensitiveError(c, 503, "inbox unavailable")
			}
		}
		c.Locals("sensitiveAuthorized", true)
		return apphttp.Detail(c, result)
	}
}

func emptyInboxReadBody(body []byte) bool {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return true
	}
	if len(body) > 128 || body[0] != '{' {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(body, &object) == nil && object != nil && len(object) == 0
}
