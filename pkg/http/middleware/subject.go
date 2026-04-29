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

package middleware

import (
	"context"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/gofiber/fiber/v2"
)

const subjectContextKey = "subject"

// TeamMemberReader 定义团队成员读取接口
type TeamMemberReader interface {
	ListUserTeams(ctx context.Context, userID string) ([]model.TeamMember, error)
}

// SubjectMiddleware 预加载鉴权主体并写入上下文
func SubjectMiddleware(reader TeamMemberReader) fiber.Handler {
	return func(c *fiber.Ctx) error {
		claimsValue := c.Locals("claims")
		claims, ok := claimsValue.(*jwt.AuthClaims)
		if !ok || claims == nil || claims.UserID == "" {
			return c.Next()
		}

		subject := authz.Subject{
			UserID: claims.UserID,
		}
		if reader != nil {
			teams, err := reader.ListUserTeams(c.Context(), claims.UserID)
			if err != nil {
				log.Errorw("load user teams failed", "userId", claims.UserID, "error", err)
				return http.Err(c, http.InternalError.Code, http.InternalError.Msg)
			}
			teamIDs := make([]string, 0, len(teams))
			for _, team := range teams {
				if team.TeamID != "" {
					teamIDs = append(teamIDs, team.TeamID)
				}
			}
			subject.TeamIDs = teamIDs
		}
		c.Locals(subjectContextKey, subject)
		return c.Next()
	}
}

// SubjectFromLocals 从上下文中提取主体
func SubjectFromLocals(c *fiber.Ctx) (authz.Subject, bool) {
	subjectValue := c.Locals(subjectContextKey)
	if subjectValue == nil {
		return authz.Subject{}, false
	}
	subject, ok := subjectValue.(authz.Subject)
	return subject, ok
}
