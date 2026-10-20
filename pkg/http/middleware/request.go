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

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// RequestMiddleware 设置请求标识并管理独立的请求上下文生命周期
func RequestMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// fasthttp.RequestCtx 虽然实现 context.Context，但会在请求结束后复用，
		// 不能传给可能异步读取上下文的数据库驱动或其他下游组件
		// UserContext 默认使用 background，并保留上游设置的值、截止时间及取消信号
		ctx, cancel := context.WithCancel(c.UserContext())
		defer cancel()
		c.SetUserContext(ctx)

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
