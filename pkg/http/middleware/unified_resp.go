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

// UnifiedResponse 统一响应
const (
	// DETAIL Detail 用于设置响应数据，例如查询，分页等，需要返回数据
	// e.g: c.Set(DETAIL, value)
	DETAIL = "detail"

	// OPERATION Operation 用于设置响应数据，例如新增，修改，删除等，不需要返回数据，只返回操作结果
	// e.g: c.Set(OPERATION, "")
	OPERATION = "operation"
)

// UnifiedResponseMiddleware 统一响应拦截器
// c.Locals("detail", value) 用于设置响应数据
func UnifiedResponseMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		err := c.Next()
		if err != nil {
			return err
		}

		// 业务逻辑错误
		if c.Response().StatusCode() != fiber.StatusOK {
			return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
		}

		// 如果未设置响应状态码，默认将状态码设置为200（OK）
		if c.Response().StatusCode() == 0 {
			c.Status(fiber.StatusOK)
		}

		// 业务逻辑正确, 设置响应数据
		if c.Response().StatusCode() >= fiber.StatusOK && c.Response().StatusCode() < fiber.StatusMultipleChoices {
			if detail := c.Locals(DETAIL); detail != nil {
				return http.WithRepJSON(c, detail)
			}

			// 业务逻辑正确, 无响应数据, 只返回结果
			if c.Locals(OPERATION) != nil {
				return http.WithRepNotDetail(c)
			}
		}

		return nil
	}
}
