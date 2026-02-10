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

	"github.com/arcentrix/arcentra/internal/engine/consts"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
	goJwt "github.com/golang-jwt/jwt/v5"
)

// PermissionChecker 权限检查器接口
// 用于检查用户是否有权限访问指定路由
type PermissionChecker interface {
	// GetUserRoutes 获取用户可访问的路由列表
	// userId: 用户ID
	// resourceId: 资源ID（组织ID/团队ID/项目ID，平台级为空字符串）
	// 返回用户可访问的路由路径列表
	GetUserRoutes(userId string, resourceId string) ([]string, error)
}

// AuthorizationMiddleware 认证中间件
// secretKey: 用于验证 JWT 的密钥
// client: Redis 客户端
// This function is used as the middleware of fiber.
func AuthorizationMiddleware(secretKey string, cache cache.ICache) fiber.Handler {
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
			return http.WithRepErrMsg(c, http.TokenBeEmpty.Code, http.TokenBeEmpty.Msg, c.Path())
		}

		claims, err := jwt.ParseToken(tokenString, secretKey)
		if err != nil {
			// 检查是否是令牌过期错误
			if errors.Is(err, goJwt.ErrTokenExpired) {
				return http.WithRepErrMsg(c, http.TokenExpired.Code, http.TokenExpired.Msg, c.Path())
			}
			log.Errorw("parse token failed: ", "error", err)
			// 其他令牌无效的情况
			return http.WithRepErrMsg(c, http.InvalidToken.Code, http.InvalidToken.Msg, c.Path())
		}

		// 从 Redis 中获取 Token 信息
		tokenKey := consts.UserTokenKey + claims.UserId
		tokenInfoStr, err := cache.Get(context.Background(), tokenKey).Result()
		if err != nil {
			log.Errorw("cache get token failed: ", "error", err, "tokenKey", tokenKey)
			return http.WithRepErrMsg(c, http.TokenExpired.Code, http.TokenExpired.Msg, c.Path())
		}

		// 解析 Token 信息
		var tokenInfo http.TokenInfo
		if err := sonic.UnmarshalString(tokenInfoStr, &tokenInfo); err != nil {
			log.Errorw("failed to unmarshal token info: ", "error", err)
			return http.WithRepErrMsg(c, http.InvalidToken.Code, http.InvalidToken.Msg, c.Path())
		}

		// 验证请求中的 Token 是否与 Redis 中存储的 Token 匹配
		if tokenInfo.AccessToken != tokenString {
			log.Errorw("token mismatch for user: ", "user_id", claims.UserId)
			return http.WithRepErrMsg(c, http.InvalidToken.Code, http.InvalidToken.Msg, c.Path())
		}

		c.Locals("claims", claims)
		return c.Next()
	}
}

// PermissionMiddleware 权限鉴权中间件
// permissionChecker: 权限检查器，用于获取用户可访问的路由列表
// excludedPaths: 排除权限检查的路径列表（如登录接口等）
// This function is used as the middleware of fiber.
func PermissionMiddleware(permissionChecker PermissionChecker, excludedPaths []string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// 检查当前路径是否在排除列表中
		currentPath := c.Path()
		for _, excludedPath := range excludedPaths {
			if currentPath == excludedPath || strings.HasPrefix(currentPath, excludedPath) {
				return c.Next()
			}
		}

		// 从 context 中获取 claims（应该由 AuthorizationMiddleware 设置）
		claimsValue := c.Locals("claims")
		if claimsValue == nil {
			log.Errorw("claims not found in context", "path", currentPath)
			return http.WithRepErrMsg(c, http.Unauthorized.Code, http.Unauthorized.Msg, currentPath)
		}

		claims, ok := claimsValue.(*jwt.AuthClaims)
		if !ok || claims == nil {
			log.Errorw("invalid claims type", "path", currentPath)
			return http.WithRepErrMsg(c, http.Unauthorized.Code, http.Unauthorized.Msg, currentPath)
		}

		userId := claims.UserId
		if userId == "" {
			log.Errorw("user id is empty", "path", currentPath)
			return http.WithRepErrMsg(c, http.Unauthorized.Code, http.Unauthorized.Msg, currentPath)
		}

		// 获取资源ID（优先从 query 参数获取，其次从 header 获取）
		resourceId := c.Query("orgId")
		if resourceId == "" {
			resourceId = c.Query("teamId")
		}
		if resourceId == "" {
			resourceId = c.Query("projectId")
		}
		if resourceId == "" {
			resourceId = c.Get("X-Resource-Id")
		}
		// 如果都没有，则为空字符串，表示平台级权限

		// 获取用户可访问的路由列表
		allowedRoutes, err := permissionChecker.GetUserRoutes(userId, resourceId)
		if err != nil {
			log.Errorw("failed to get user routes", "userId", userId, "resourceId", resourceId, "error", err)
			return http.WithRepErrMsg(c, http.InternalError.Code, http.InternalError.Msg, currentPath)
		}

		// 检查当前路径是否在允许的路由列表中
		if !isRouteAllowed(currentPath, allowedRoutes) {
			log.Debugw("permission denied", "userId", userId, "resourceId", resourceId, "path", currentPath, "allowedRoutes", allowedRoutes)
			return http.WithRepErrMsg(c, http.Forbidden.Code, http.Forbidden.Msg, currentPath)
		}

		return c.Next()
	}
}

// isRouteAllowed 检查路由是否在允许的路由列表中
// 支持精确匹配和前缀匹配
func isRouteAllowed(path string, allowedRoutes []string) bool {
	for _, allowedRoute := range allowedRoutes {
		// 精确匹配
		if path == allowedRoute {
			return true
		}
		// 前缀匹配：如果允许的路由是 /api/v1/projects，则 /api/v1/projects/123 也应该被允许
		if strings.HasPrefix(path, allowedRoute+"/") || strings.HasPrefix(path, allowedRoute+"?") {
			return true
		}
	}
	return false
}
