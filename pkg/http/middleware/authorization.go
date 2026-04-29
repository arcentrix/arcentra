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
	"context"
	"errors"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
	goJwt "github.com/golang-jwt/jwt/v5"
)

// AuthorizationMiddleware 认证中间件
// secretKey: 用于验证 JWT 的密钥
// client: Redis 客户端
// This function is used as the middleware of fiber.
func AuthorizationMiddleware(secretKey string, store cache.ICache) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var tokenString string

		// 优先从 Authorization header 中获取 token
		aToken := c.Get("Authorization")
		if aToken != "" {
			// 按空格分割
			parts := strings.SplitN(aToken, " ", 2)
			if len(parts) == 2 && parts[0] == "Bearer" {
				tokenString = parts[1]
			}
		}

		// 如果 header 中没有 token，尝试从 cookie 中获取
		if tokenString == "" {
			tokenString = c.Cookies("accessToken")
		}

		// 如果两种方式都没有获取到 token，返回错误
		if tokenString == "" {
			return http.Err(c, http.TokenBeEmpty.Code, http.TokenBeEmpty.Msg)
		}

		claims, err := jwt.ParseToken(tokenString, secretKey)
		if err != nil {
			// 检查是否是令牌过期错误
			if errors.Is(err, goJwt.ErrTokenExpired) {
				return http.Err(c, http.TokenExpired.Code, http.TokenExpired.Msg)
			}
			log.Errorw("parse token failed: ", "error", err)
			// 其他令牌无效的情况
			return http.Err(c, http.InvalidToken.Code, http.InvalidToken.Msg)
		}

		// 从 Redis 中获取 Token 信息
		tokenKey := consts.UserTokenKey + claims.UserID
		tokenInfoStr, err := store.Get(context.Background(), tokenKey).Result()
		if err != nil {
			log.Errorw("cache get token failed: ", "error", err, "tokenKey", tokenKey)
			return http.Err(c, http.TokenExpired.Code, http.TokenExpired.Msg)
		}

		// 解析 Token 信息
		var tokenInfo http.TokenInfo
		if err := sonic.UnmarshalString(tokenInfoStr, &tokenInfo); err != nil {
			log.Errorw("failed to unmarshal token info: ", "error", err)
			return http.Err(c, http.InvalidToken.Code, http.InvalidToken.Msg)
		}

		// 验证请求中的 Token 是否与 Redis 中存储的 Token 匹配
		if tokenInfo.AccessToken != tokenString {
			log.Errorw("token mismatch for user: ", "user_id", claims.UserID)
			return http.Err(c, http.InvalidToken.Code, http.InvalidToken.Msg)
		}

		c.Locals("claims", claims)
		return c.Next()
	}
}
