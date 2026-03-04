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
	"strings"

	"github.com/gofiber/fiber/v2"
)

// RealIPMiddleware 获取真实 IP 中间件
func RealIPMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if xff := c.Get("X-Forwarded-For"); xff != "" {
			// XFF: client, proxy1, proxy2
			parts := strings.Split(xff, ",")
			if len(parts) > 0 {
				ip := strings.TrimSpace(parts[0])
				if ip != "" {
					c.Locals("ip", ip)
					return c.Next()
				}
			}
		}
		if ip := c.Get("X-Real-IP"); ip != "" {
			ip = strings.TrimSpace(ip)
			if ip != "" {
				c.Locals("ip", ip)
			}
		}
		return c.Next()
	}
}
