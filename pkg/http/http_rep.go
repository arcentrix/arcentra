// Copyright 2025 Arcentra Team
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

package http

import (
	"github.com/gofiber/fiber/v2"
)

type Response struct {
	Code   int    `json:"code"`
	Detail any    `json:"detail,omitempty"`
	Msg    string `json:"msg"`
}

// WithRepJSON 只返回json数据
func WithRepJSON(c *fiber.Ctx, detail any) error {
	return c.JSON(Response{
		Code:   Success.Code,
		Detail: detail,
		Msg:    Success.Msg,
	})
}

// WithRepMsg 返回自定义code, msg
func WithRepMsg(c *fiber.Ctx, code int, msg string) error {
	return c.JSON(Response{
		Code: code,
		Msg:  msg,
	})
}

// WithRepDetail 返回自定义code, msg, detail
func WithRepDetail(c *fiber.Ctx, code int, msg string, detail any) error {
	return c.JSON(Response{
		Code:   code,
		Detail: detail,
		Msg:    msg,
	})
}

// WithRepNotDetail 只成功的返回操作结果，返回结构体没有detail字段
func WithRepNotDetail(c *fiber.Ctx) error {
	return c.JSON(Response{
		Code: Success.Code,
		Msg:  Success.Msg,
	})
}
