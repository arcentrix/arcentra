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
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/gofiber/fiber/v2"
)

func AccessLogMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()

		err := c.Next()

		latency := time.Since(start)
		status := c.Response().StatusCode()

		if status < 400 && latency < 300*time.Millisecond {
			return err
		}

		log.Warnw("http access", "ip", c.IP(), "method", c.Method(), "path", c.Path(), "status", status, "latency", latency, "error", err)

		return err
	}
}
