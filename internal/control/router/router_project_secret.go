// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

func (rt *Router) projectSecretsHandler(action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Set("Cache-Control", "no-store")
		claims, ok := c.Locals("claims").(*jwt.AuthClaims)
		if !ok || claims == nil || claims.UserID == "" {
			return sensitiveError(c, 401, "authentication is required")
		}
		if rt.Services == nil || rt.Services.Repos() == nil || rt.Services.Secret == nil {
			return sensitiveError(c, 503, "project secrets unavailable")
		}
		var input service.ProjectSecretRequest
		if action != "list" {
			if len(c.Body()) > 400*1024 || !validProjectSecretObject(c.Body(), action) {
				return sensitiveError(c, 400, "invalid project secret request")
			}
			decoder := json.NewDecoder(bytes.NewReader(c.Body()))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
				return sensitiveError(c, 400, "invalid project secret request")
			}
		}
		authorize := func(ctx context.Context, r *repo.Repositories, p *model.Project) (repo.ProjectSecretCapabilities, error) {
			caps := repo.ProjectSecretCapabilities{}
			subject, err := currentApprovalSubject(ctx, r, claims.UserID)
			if err != nil {
				return caps, repo.ErrProjectSecretForbidden
			}
			checker, err := authz.NewLiveAuthorizer(ctx, r)
			if err != nil {
				return caps, err
			}
			resource := authz.ResourceRef{ProjectID: p.ProjectID, OrgID: p.OrgID}
			for _, permission := range []struct {
				action string
				target *bool
			}{{"secret:read", &caps.Read}, {"secret:write", &caps.Write}, {"secret:delete", &caps.Delete}} {
				allowed, err := checker.Check(ctx, subject, permission.action, resource)
				if err != nil {
					return caps, err
				}
				*permission.target = allowed
			}
			return caps, nil
		}
		list, detail, err := rt.Services.Secret.ProjectSecrets(c.UserContext(), rt.Services.Repos(), c.Params("projectID"), claims.UserID, action, c.Params("secretID"), input, authorize)
		if err != nil {
			switch {
			case errors.Is(err, repo.ErrProjectSecretInvalid):
				return sensitiveError(c, 400, "invalid project secret request")
			case errors.Is(err, repo.ErrProjectSecretForbidden), errors.Is(err, repo.ErrApprovalForbidden):
				return sensitiveError(c, 403, "current project secret access denied")
			case errors.Is(err, repo.ErrProjectSecretConflict):
				return sensitiveError(c, 409, "project secret changed or operation unavailable; reload before retrying")
			case errors.Is(err, gorm.ErrRecordNotFound):
				return sensitiveError(c, 404, "project secret not found")
			default:
				return sensitiveError(c, 503, "project secrets unavailable")
			}
		}
		c.Locals("sensitiveAuthorized", true)
		if action == "list" {
			return apphttp.Detail(c, list)
		}
		return apphttp.Detail(c, detail)
	}
}

func validProjectSecretObject(body []byte, action string) bool {
	if !utf8.Valid(body) {
		return false
	}
	allowed := map[string]bool{}
	switch action {
	case "create":
		allowed = map[string]bool{"name": true, "secretType": true, "description": true, "value": true}
	case "update":
		allowed = map[string]bool{"version": true, "description": true}
	case "rotate":
		allowed = map[string]bool{"version": true, "value": true}
	case "disable":
		allowed = map[string]bool{"version": true}
	default:
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return false
		}
		key, ok := token.(string)
		if !ok || !allowed[key] || seen[key] {
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	token, err = decoder.Token()
	return err == nil && token == json.Delim('}') && decoder.Decode(new(any)) == io.EOF
}
