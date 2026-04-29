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

	"github.com/arcentrix/arcentra/internal/control/authz"
	httpx "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
)

type testAuthorizer struct {
	allowed bool
	err     error
	subject authz.Subject
}

func (a *testAuthorizer) Check(_ context.Context, subject authz.Subject, _ string, _ authz.ResourceRef) (bool, error) {
	a.subject = subject
	return a.allowed, a.err
}

func (a *testAuthorizer) CheckAny(_ context.Context, _ authz.Subject, _ []string, _ authz.ResourceRef) (bool, error) {
	return false, nil
}

func (a *testAuthorizer) EffectiveActions(_ context.Context, _ authz.Subject, _, _ string) ([]string, error) {
	return nil, nil
}

func (a *testAuthorizer) Reload(_ context.Context) error {
	return nil
}

func (a *testAuthorizer) InvalidateSubjectCache(_ context.Context, _ authz.Subject, _, _ string) error {
	return nil
}

func TestRequirePermissionAllow(t *testing.T) {
	t.Parallel()

	checker := &testAuthorizer{allowed: true}
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("claims", &jwt.AuthClaims{UserID: "user-1"})
		return c.Next()
	})
	app.Get("/secure", RequirePermission(checker, "pipeline:read", ResolvePlatformScope()), func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	resp, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/secure", nil))
	require.NoError(t, err)
	require.Equal(t, fiber.StatusNoContent, resp.StatusCode)
	require.Equal(t, "user-1", checker.subject.UserID)
}

func TestRequirePermissionDeny(t *testing.T) {
	t.Parallel()

	checker := &testAuthorizer{allowed: false}
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("claims", &jwt.AuthClaims{UserID: "user-2"})
		return c.Next()
	})
	app.Get("/secure", RequirePermission(checker, "pipeline:read", ResolvePlatformScope()), func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	resp, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/secure", nil))
	require.NoError(t, err)
	require.Equal(t, fiber.StatusOK, resp.StatusCode)

	var payload httpx.ResponseErr
	decodeErr := sonic.ConfigFastest.NewDecoder(resp.Body).Decode(&payload)
	require.NoError(t, decodeErr)
	require.Equal(t, httpx.Forbidden.Code, payload.ErrCode)
}
