package middleware

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// RequestMiddleware set request id
func RequestMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		requestID := c.Request().Header.Peek("X-Request-Id")
		if len(requestID) == 0 {
			requestID = []byte(uuid.New().String())
		}
		c.Request().Header.Set("X-Request-Id", string(requestID))
		c.Set("X-Request-Id", string(requestID))
		c.Locals("request_id", string(requestID))
		return c.Next()
	}
}
