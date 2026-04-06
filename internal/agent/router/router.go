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

	"github.com/arcentrix/arcentra/pkg/foundation/version"
	"github.com/arcentrix/arcentra/pkg/lifecycle/shutdown"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
	"github.com/arcentrix/arcentra/pkg/transport/http"
	"github.com/arcentrix/arcentra/pkg/transport/http/middleware"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/pprof"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

type Router struct {
	HTTP        *http.HTTP
	ShutdownMgr *shutdown.Manager
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

	app.Use(middleware.AccessLogMiddleware())

	// 中间件
	app.Use(
		recover.New(),
		middleware.HTTPMetricsMiddleware(),
		middleware.CorsMiddleware(),
		middleware.UnifiedResponseMiddleware(),
	)

	// pprof
	// path: /debug/pprof
	app.Use(pprof.New())

	// 健康检查 - 在下线时返回 503，用于 Kubernetes readiness probe
	app.Get("/health", func(c *fiber.Ctx) error {
		if rt.ShutdownMgr != nil && rt.ShutdownMgr.IsShuttingDown() {
			return c.Status(fiber.StatusServiceUnavailable).SendString("shutting down")
		}
		return c.SendString("ok")
	})

	// 优雅下线接口 - 触发服务优雅关闭
	app.Post("/shutdown", func(c *fiber.Ctx) error {
		if rt.ShutdownMgr == nil {
			return http.Err(c, fiber.StatusInternalServerError, "shutdown manager not initialized")
		}

		if rt.ShutdownMgr.Shutdown() {
			log.Info("Graceful shutdown triggered via HTTP endpoint")
			return http.Detail(c, map[string]any{"message": "shutdown initiated"})
		}

		return http.Err(c, fiber.StatusConflict, "shutdown already in progress")
	})

	// 版本信息
	app.Get("/version", func(c *fiber.Ctx) error {
		return c.JSON(version.GetVersion())
	})

	// 找不到路径时的处理 - 必须在所有路由注册之后
	app.Use(func(c *fiber.Ctx) error {
		return http.Err(c, fiber.StatusNotFound, "request path not found")
	})

	return app
}
