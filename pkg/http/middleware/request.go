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
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type requestContextLifetimeKey struct{}

type requestContextLifetime struct {
	cancel      context.CancelFunc
	streamTimer *time.Timer
}

func (l *requestContextLifetime) Close() error {
	if l.streamTimer != nil {
		l.streamTimer.Stop()
	}
	l.cancel()
	return nil
}

// RequestMiddleware 设置请求标识并管理独立的请求上下文生命周期
func RequestMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// fasthttp.RequestCtx 虽然实现 context.Context，但会在请求结束后复用，
		// 不能传给可能异步读取上下文的数据库驱动或其他下游组件
		// UserContext 默认使用 background，并保留上游设置的值、截止时间及取消信号
		ctx, cancel := context.WithCancel(c.UserContext())
		c.SetUserContext(ctx)
		// fasthttp 在响应发送结束或失败后的请求清理阶段、复用 Request 之前关闭请求局部值
		// 这里只保存独立取消函数，不让下游上下文引用池化的 Fiber/fasthttp 对象
		lifetime := &requestContextLifetime{cancel: cancel}
		c.Locals(requestContextLifetimeKey{}, lifetime)
		defer func() {
			// SendStream/SetBodyStreamWriter 在 handler 返回后才继续读取数据，
			// 流式响应的取消由上述请求清理边界负责；普通响应仍在 handler 返回时取消
			if !c.Response().IsBodyStream() {
				_ = lifetime.Close()
				return
			}
			// fasthttp 也在 handler 返回后开始计算 WriteTimeout，但套接字写入超时
			// 无法中断阻塞的上游读取；使用同一配置约束流上下文，完成时停止计时器
			// 保留零值的无限时策略及更早的上游取消；这不是即时客户端断开通知
			if timeout := c.App().Config().WriteTimeout; timeout > 0 && ctx.Err() == nil {
				lifetime.streamTimer = time.AfterFunc(timeout, cancel)
			}
		}()

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
