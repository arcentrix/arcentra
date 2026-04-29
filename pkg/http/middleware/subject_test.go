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
	"net/http/httptest"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
)

type testTeamReader struct {
	teams []model.TeamMember
	err   error
}

func (r *testTeamReader) ListUserTeams(_ context.Context, _ string) ([]model.TeamMember, error) {
	return r.teams, r.err
}

func TestSubjectMiddlewareLoadTeamIDs(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("claims", &jwt.AuthClaims{UserID: "user-1"})
		return c.Next()
	})
	app.Use(SubjectMiddleware(&testTeamReader{
		teams: []model.TeamMember{
			{TeamID: "team-1"},
			{TeamID: ""},
			{TeamID: "team-2"},
		},
	}))
	app.Get("/subject", func(c *fiber.Ctx) error {
		subject, ok := SubjectFromLocals(c)
		if !ok {
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		if subject.UserID != "user-1" {
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		if len(subject.TeamIDs) != 2 {
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		if subject.TeamIDs[0] != "team-1" || subject.TeamIDs[1] != "team-2" {
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		return c.SendStatus(fiber.StatusNoContent)
	})

	resp, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/subject", nil))
	require.NoError(t, err)
	require.Equal(t, fiber.StatusNoContent, resp.StatusCode)
}
