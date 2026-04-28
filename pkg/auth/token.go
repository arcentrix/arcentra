// Copyright 2025 Infraflows Team
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

package auth

import (
	"errors"
	"strings"

	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/gofiber/fiber/v2"
)

// ParseAuthorizationToken 解析 Authorization 头中的 Bearer token
func ParseAuthorizationToken(f *fiber.Ctx, secretKey string) (*jwt.AuthClaims, error) {
	token := f.Get("Authorization")
	if token == "" {
		return nil, errors.New(http.TokenBeEmpty.Msg)
	}

	if t, ok := strings.CutPrefix(token, "Bearer "); ok {
		token = t
	} else {
		return nil, errors.New(http.TokenFormatIncorrect.Msg)
	}

	claims, err := jwt.ParseToken(token, secretKey)
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// CurrentUserID 从当前请求中提取用户 ID，公共便捷函数
func CurrentUserID(c *fiber.Ctx, secretKey string) string {
	claims, err := ParseAuthorizationToken(c, secretKey)
	if err != nil || claims == nil {
		return ""
	}
	return strings.TrimSpace(claims.UserID)
}

// UsernameResolver 查询用户名的函数签名
// 由调用方注入具体实现（如 UserService.FetchUserInfo 的适配）
type UsernameResolver func(userID string) string

// CurrentUserName 从当前请求中提取用户 ID，再通过 resolver 查询用户名
// 优先返回 Username；查询失败或 Username 为空时回退为 userID
func CurrentUserName(c *fiber.Ctx, secretKey string, resolve UsernameResolver) string {
	userID := CurrentUserID(c, secretKey)
	if userID == "" {
		return ""
	}
	if resolve == nil {
		return userID
	}
	if name := resolve(userID); name != "" {
		return name
	}
	return userID
}
