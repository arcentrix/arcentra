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

package router

import (
	"time"

	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/arcentrix/arcentra/pkg/shutdown"
	"github.com/arcentrix/arcentra/pkg/version"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

type Router struct {
	HTTP        *http.HTTP
	ShutdownMgr *shutdown.Manager
	// Readiness 在注册、认证和工作器启动成功后返回 true
	Readiness func() bool
}

func NewRouter(
	httpConf *http.HTTP,
	shutdownMgr *shutdown.Manager,
) *Router {
	return &Router{
		HTTP:        httpConf,
		ShutdownMgr: shutdownMgr,
	}
}

func (rt *Router) Router() *fiber.App {
	// 设置默认的 BodyLimit（100MB）
	bodyLimit := rt.HTTP.BodyLimit
	if bodyLimit <= 0 {
		bodyLimit = 100 * 1024 * 1024 // 100MB 默认值
	}

	app := fiber.New(fiber.Config{
		AppName: "Arcentra Agent",
		// DisableStartupMessage: true,
		ReadTimeout:  time.Duration(rt.HTTP.ReadTimeout) * time.Second,
		WriteTimeout: time.Duration(rt.HTTP.WriteTimeout) * time.Second,
		IdleTimeout:  time.Duration(rt.HTTP.IdleTimeout) * time.Second,
		BodyLimit:    bodyLimit, // 请求体大小限制，用于插件上传等
	})

	// 公共监听器不提供管理接口，必须先于 CORS 拒绝所有方法及预检请求
	notFound := func(c *fiber.Ctx) error {
		return http.Err(c.Status(fiber.StatusNotFound), fiber.StatusNotFound, "request path not found")
	}
	app.Use("/shutdown", notFound)
	app.Use("/debug/pprof", notFound)

	app.Use(middleware.AccessLogMiddleware())

	// 中间件
	app.Use(
		recover.New(),
		middleware.HTTPMetricsMiddleware(),
		middleware.CorsMiddleware(),
		middleware.UnifiedResponseMiddleware(),
	)

	// 存活检查只反映 HTTP 进程仍可响应，等待注册或排空时不触发重启
	app.Get("/live", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	// 就绪检查在启动未完成或下线时返回 503，/health 保留为兼容别名
	ready := func(c *fiber.Ctx) error {
		if rt.ShutdownMgr != nil && rt.ShutdownMgr.IsShuttingDown() {
			return c.Status(fiber.StatusServiceUnavailable).SendString("shutting down")
		}
		if rt.Readiness == nil || !rt.Readiness() {
			return c.Status(fiber.StatusServiceUnavailable).SendString("not ready")
		}
		return c.SendString("ok")
	}
	app.Get("/ready", ready)
	app.Get("/health", ready)

	// 版本信息
	app.Get("/version", func(c *fiber.Ctx) error {
		return c.JSON(version.GetVersion())
	})

	// 找不到路径时的处理 - 必须在所有路由注册之后
	app.Use(notFound)

	return app
}
