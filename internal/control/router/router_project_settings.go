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
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

func (rt *Router) projectSettingsHandler(action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		claims, ok := c.Locals("claims").(*jwt.AuthClaims)
		if !ok || claims == nil || claims.UserID == "" {
			return sensitiveError(c, 401, "authentication is required")
		}
		if rt.Services == nil || rt.Services.Repos() == nil {
			return sensitiveError(c, 503, "project settings unavailable")
		}
		var patch repo.ProjectSettingsPatch
		if action != "read" {
			if len(c.Body()) > 16384 || !validProjectSettingsObject(c.Body()) {
				return sensitiveError(c, 400, "invalid project settings request")
			}
			decoder := json.NewDecoder(bytes.NewReader(c.Body()))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&patch); err != nil {
				return sensitiveError(c, 400, "invalid project settings request")
			}
			if decoder.Decode(new(any)) != io.EOF {
				return sensitiveError(c, 400, "invalid project settings request")
			}
		}
		authorize := func(ctx context.Context, repositories *repo.Repositories, project *model.Project) (bool, error) {
			subject, err := currentApprovalSubject(ctx, repositories, claims.UserID)
			if err != nil {
				return false, repo.ErrProjectSettingsForbidden
			}
			checker, err := authz.NewLiveAuthorizer(ctx, repositories)
			if err != nil {
				return false, err
			}
			resource := authz.ResourceRef{ProjectID: project.ProjectID, OrgID: project.OrgID}
			read, err := checker.Check(ctx, subject, "project:read", resource)
			if err != nil {
				return false, err
			}
			if !read {
				return false, repo.ErrProjectSettingsForbidden
			}
			return checker.Check(ctx, subject, "project:update", resource)
		}
		detail, err := rt.Services.Repos().ProjectSettings(c.UserContext(), c.Params("projectID"), claims.UserID, action, patch, authorize)
		if err != nil {
			switch {
			case errors.Is(err, repo.ErrProjectSettingsForbidden), errors.Is(err, repo.ErrApprovalForbidden):
				return sensitiveError(c, 403, "current project access denied")
			case errors.Is(err, repo.ErrProjectSettingsInvalid):
				return sensitiveError(c, 400, "invalid project settings request")
			case errors.Is(err, repo.ErrProjectSettingsConflict):
				return sensitiveError(c, 409, "project settings changed or lifecycle transition is unavailable; reload before retrying")
			case errors.Is(err, gorm.ErrRecordNotFound):
				return sensitiveError(c, 404, "project not found")
			default:
				return sensitiveError(c, 503, "project settings unavailable")
			}
		}
		c.Locals("sensitiveAuthorized", true)
		return apphttp.Detail(c, detail)
	}
}

// A settings edit has exact case-sensitive keys and no duplicate or null values.
// The typed decoder below separately validates string/integer value types.
func validProjectSettingsObject(body []byte) bool {
	if !utf8.Valid(body) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	allowed := map[string]bool{"version": true, "displayName": true, "description": true, "defaultBranch": true, "tags": true, "language": true, "framework": true, "visibility": true}
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
