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

package http

import (
	"time"

	"github.com/gofiber/fiber/v2"
)

type Response struct {
	Code      int    `json:"code"`
	Detail    any    `json:"detail,omitempty"`
	Msg       string `json:"msg"`
	Timestamp int64  `json:"timestamp"`
}

type ResponseErr struct {
	ErrCode   int    `json:"code"`
	ErrMsg    any    `json:"errMsg"`
	Path      string `json:"path,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

const (
	// DETAIL Detail 用于设置响应数据，例如查询，分页等，需要返回数据
	// e.g: c.Set(DETAIL, value)
	DETAIL = "detail"

	// OPERATION Operation 用于设置响应数据，例如新增，修改，删除等，不需要返回数据，只返回操作结果
	// e.g: c.Set(OPERATION, "")
	OPERATION = "operation"
)

// Msg return custom code, msg
func Msg(c *fiber.Ctx, code int, msg string) error {
	return c.JSON(Response{
		Code:      code,
		Msg:       msg,
		Timestamp: now(),
	})
}

// JSON 成功且带数据时统一用此方法写出响应（由统一响应中间件调用）。
func JSON(c *fiber.Ctx, detail any) error {
	return c.JSON(Response{
		Code:      Success.Code,
		Detail:    detail,
		Msg:       Success.Msg,
		Timestamp: now(),
	})
}

// Detail 在 handler 中设置待返回数据并 return nil，由统一响应中间件用 JSON 写出。
func Detail(c *fiber.Ctx, detail any) error {
	c.Locals(DETAIL, detail)
	return nil
}

// Operation return success json data, return struct without detail field
func Operation(c *fiber.Ctx) error {
	c.Locals(OPERATION, "")
	return nil
}

// NotDetail return success json data, return struct without detail field
func NotDetail(c *fiber.Ctx) error {
	return c.JSON(Response{
		Code:      Success.Code,
		Msg:       Success.Msg,
		Timestamp: now(),
	})
}

// Err return error json data
func Err(c *fiber.Ctx, code int, errMsg string) error {
	return c.JSON(ResponseErr{
		ErrCode:   code,
		ErrMsg:    errMsg,
		Path:      c.Path(),
		Timestamp: now(),
	})
}

// ErrNotData return error json data, return struct without path field
func ErrNotData(c *fiber.Ctx, errMsg string) error {
	return c.JSON(ResponseErr{
		ErrCode:   Success.Code,
		ErrMsg:    errMsg,
		Timestamp: now(),
	})
}

func now() int64 {
	return time.Now().Unix()
}
