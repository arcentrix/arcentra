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
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/gofiber/fiber/v2"
)

// UnifiedResponseMiddleware 统一响应拦截器
// c.Locals("detail", value) 用于设置响应数据
//
// 如果 handler 已经显式写过响应体（例如调用 http.Err / c.JSON / c.Send 等），
// 中间件保留原响应不覆盖，避免真实错误被静默重写为 "Request Success"
func UnifiedResponseMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		err := c.Next()
		if err != nil {
			return err
		}

		// 默认状态码
		if c.Response().StatusCode() == 0 {
			c.Status(fiber.StatusOK)
		}

		// 非 2xx 统一错误
		if c.Response().StatusCode() < fiber.StatusOK || c.Response().StatusCode() >= fiber.StatusMultipleChoices {
			return http.Err(
				c,
				http.Failed.Code,
				http.Failed.Msg,
			)
		}

		// handler 已写出响应体（如 http.Err 写出的 ResponseErr）：保留原样
		if len(c.Response().Body()) > 0 {
			return nil
		}

		// 有数据返回：统一用 http.JSON 写出 { code, msg, timestamp, detail }
		if v := c.Locals(http.DETAIL); v != nil {
			return http.JSON(c, v)
		}

		// operation 返回（仅操作成功，无数据）
		if c.Locals(http.OPERATION) != nil {
			return http.NotDetail(c)
		}

		// 默认 success
		return http.NotDetail(c)
	}
}
