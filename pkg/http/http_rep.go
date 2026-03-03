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

// WithRepErr return operation result, return struct with path field
func WithRepErr(c *fiber.Ctx, code int, errMsg string, path string) error {
	return c.JSON(ResponseErr{
		ErrCode:   code,
		ErrMsg:    errMsg,
		Path:      path,
		Timestamp: time.Now().Unix(),
	})
}

// WithRepErrMsg return error json data
func WithRepErrMsg(c *fiber.Ctx, code int, errMsg string, path string) error {
	return c.JSON(ResponseErr{
		ErrCode:   code,
		ErrMsg:    errMsg,
		Path:      path,
		Timestamp: time.Now().Unix(),
	})
}

// WithRepErrNotData return error json data, return struct without path field
func WithRepErrNotData(c *fiber.Ctx, errMsg string) error {
	return c.JSON(ResponseErr{
		ErrCode:   Success.Code,
		ErrMsg:    errMsg,
		Timestamp: time.Now().Unix(),
	})
}

// WithRepJSON return success json data
func WithRepJSON(c *fiber.Ctx, detail any) error {
	return c.JSON(Response{
		Code:      Success.Code,
		Detail:    detail,
		Msg:       Success.Msg,
		Timestamp: time.Now().Unix(),
	})
}

// WithRepMsg return custom code, msg
func WithRepMsg(c *fiber.Ctx, code int, msg string) error {
	return c.JSON(Response{
		Code:      code,
		Msg:       msg,
		Timestamp: time.Now().Unix(),
	})
}

// WithRepDetail return custom code, msg, detail
func WithRepDetail(c *fiber.Ctx, code int, msg string, detail any) error {
	return c.JSON(Response{
		Code:      code,
		Detail:    detail,
		Msg:       msg,
		Timestamp: time.Now().Unix(),
	})
}

// WithRepNotDetail return success json data, return struct without detail field
func WithRepNotDetail(c *fiber.Ctx) error {
	return c.JSON(Response{
		Code:      Success.Code,
		Msg:       Success.Msg,
		Timestamp: time.Now().Unix(),
	})
}
