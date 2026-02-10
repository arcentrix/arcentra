// Copyright 2025 Arcentra Authors.
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
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// TestRequestMiddleware_WithExistingRequestId tests that existing X-Request-Id header is preserved
func TestRequestMiddleware_WithExistingRequestId(t *testing.T) {
	app := fiber.New()
	app.Use(RequestMiddleware())
	app.Get("/test", func(c *fiber.Ctx) error {
		requestId := c.Get("X-Request-Id")
		if requestId == "" {
			t.Error("X-Request-Id header should be set")
		}
		if requestId != "existing-request-id-12345" {
			t.Errorf("X-Request-Id should be preserved, got: %s", requestId)
		}
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Request-Id", "existing-request-id-12345")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("failed to test request: %v", err)
	}

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

// TestRequestMiddleware_WithoutRequestId tests that new UUID is generated when X-Request-Id is missing
func TestRequestMiddleware_WithoutRequestId(t *testing.T) {
	app := fiber.New()
	app.Use(RequestMiddleware())
	app.Get("/test", func(c *fiber.Ctx) error {
		requestId := c.Get("X-Request-Id")
		if requestId == "" {
			t.Error("X-Request-Id header should be generated")
		}
		// Validate UUID format
		_, err := uuid.Parse(requestId)
		if err != nil {
			t.Errorf("X-Request-Id should be a valid UUID, got: %s, error: %v", requestId, err)
		}
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	// Explicitly ensure X-Request-Id is not set
	req.Header.Del("X-Request-Id")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("failed to test request: %v", err)
	}

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

// TestRequestMiddleware_EmptyRequestId tests that new UUID is generated when X-Request-Id is empty
func TestRequestMiddleware_EmptyRequestId(t *testing.T) {
	app := fiber.New()
	app.Use(RequestMiddleware())
	app.Get("/test", func(c *fiber.Ctx) error {
		requestId := c.Get("X-Request-Id")
		if requestId == "" {
			t.Error("X-Request-Id header should be generated")
		}
		// Validate UUID format
		_, err := uuid.Parse(requestId)
		if err != nil {
			t.Errorf("X-Request-Id should be a valid UUID, got: %s, error: %v", requestId, err)
		}
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Request-Id", "")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("failed to test request: %v", err)
	}

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

// TestRequestMiddleware_UUIDFormat tests that generated UUID follows correct format
func TestRequestMiddleware_UUIDFormat(t *testing.T) {
	uuidRegex := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

	app := fiber.New()
	app.Use(RequestMiddleware())
	app.Get("/test", func(c *fiber.Ctx) error {
		requestId := c.Get("X-Request-Id")
		if !uuidRegex.MatchString(requestId) {
			t.Errorf("X-Request-Id should match UUID format, got: %s", requestId)
		}
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("failed to test request: %v", err)
	}

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

// TestRequestMiddleware_UniqueUUIDs tests that different requests get different UUIDs
func TestRequestMiddleware_UniqueUUIDs(t *testing.T) {
	requestIds := make(map[string]bool)

	app := fiber.New()
	app.Use(RequestMiddleware())
	app.Get("/test", func(c *fiber.Ctx) error {
		requestId := c.Get("X-Request-Id")
		// Store request ID in response header for verification
		c.Set("X-Test-Request-Id", requestId)
		return c.SendString("ok")
	})

	// Make multiple requests
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("failed to test request %d: %v", i, err)
		}

		if resp.StatusCode != fiber.StatusOK {
			t.Errorf("expected status 200, got %d", resp.StatusCode)
		}

		// Get request ID from response header (set by handler)
		requestId := resp.Header.Get("X-Test-Request-Id")
		if requestId == "" {
			t.Errorf("request %d: X-Request-Id should be set", i)
			continue
		}

		if requestIds[requestId] {
			t.Errorf("duplicate request ID generated: %s", requestId)
		}
		requestIds[requestId] = true
	}

	if len(requestIds) != 10 {
		t.Errorf("expected 10 unique request IDs, got %d", len(requestIds))
	}
}

// TestRequestMiddleware_NextCalled tests that Next() is called correctly
func TestRequestMiddleware_NextCalled(t *testing.T) {
	nextCalled := false

	app := fiber.New()
	app.Use(RequestMiddleware())
	app.Get("/test", func(c *fiber.Ctx) error {
		nextCalled = true
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("failed to test request: %v", err)
	}

	if !nextCalled {
		t.Error("Next() should be called")
	}

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}
