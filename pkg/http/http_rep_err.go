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

type ResponseErr struct {
	ErrCode int    `json:"code"`
	ErrMsg  any    `json:"errMsg"`
	Path    string `json:"path,omitempty"`
}

// WithRepErr 返回操作结果，返回结构体有path字段
func WithRepErr(c *fiber.Ctx, code int, errMsg string, path string) error {
	return c.JSON(ResponseErr{
		ErrCode: code,
		ErrMsg:  errMsg,
		Path:    path,
	})
}

// WithRepErrMsg 只返回json数据
func WithRepErrMsg(c *fiber.Ctx, code int, errMsg string, path string) error {
	return c.JSON(ResponseErr{
		ErrCode: code,
		ErrMsg:  errMsg,
		Path:    path,
	})
}

// WithRepErrNotData 只失败的返回操作结果，返回结构体没有path字段
func WithRepErrNotData(c *fiber.Ctx, errMsg string) error {
	return c.JSON(ResponseErr{
		ErrCode: Success.Code,
		ErrMsg:  errMsg,
	})
}
