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
	"embed"
	"time"

	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/shutdown"
	"github.com/arcentrix/arcentra/pkg/trace/inject"
	"github.com/arcentrix/arcentra/pkg/version"
	"github.com/gofiber/contrib/fiberi18n/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/pprof"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"golang.org/x/text/language"
)

type Router struct {
	HTTP        *http.HTTP
	Cache       cache.ICache
	Services    *service.Services
	ShutdownMgr *shutdown.Manager
	AppConf     *config.AppConfig
}

const (
	apiContextPath = "/api/v1"
	// openApiPath    = "/openapi"
)

//go:embed localize/*
var localizeFS embed.FS

func NewRouter(
	httpConf *http.HTTP,
	cache cache.ICache,
	services *service.Services,
	shutdownMgr *shutdown.Manager,
	appConf *config.AppConfig,
) *Router {
	return &Router{
		HTTP:        httpConf,
		Cache:       cache,
		Services:    services,
		ShutdownMgr: shutdownMgr,
		AppConf:     appConf,
	}
}

func (rt *Router) Router() *fiber.App {
	// 设置默认的 BodyLimit（100MB）
	bodyLimit := rt.HTTP.BodyLimit
	if bodyLimit <= 0 {
		bodyLimit = 100 * 1024 * 1024 // 100MB 默认值
	}

	app := fiber.New(fiber.Config{
		AppName: "Arcentra",
		// DisableStartupMessage: true,
		ReadTimeout:  time.Duration(rt.HTTP.ReadTimeout) * time.Second,
		WriteTimeout: time.Duration(rt.HTTP.WriteTimeout) * time.Second,
		IdleTimeout:  time.Duration(rt.HTTP.IdleTimeout) * time.Second,
		BodyLimit:    bodyLimit,                 // 请求体大小限制
		ProxyHeader:  fiber.HeaderXForwardedFor, // get real ip from proxy header
	})

	// WARNING: middleware.RealIPMiddleware -> middleware.RequestMiddleware -> inject.FiberMiddleware sequence is important
	app.Use(
		middleware.RealIPMiddleware(),  // 先解析真实 IP 到 Locals("ip")
		middleware.RequestMiddleware(), // 设置 request_id 到 Locals("request_id")
		inject.FiberMiddleware(),       // trace middleware 读取 request_id/ip 并传播上下文
		recover.New(),
		middleware.HTTPMetricsMiddleware(),
		middleware.CorsMiddleware(),
		middleware.UnifiedResponseMiddleware(),
		middleware.AccessLogMiddleware(),
	)

	// Configure i18n middleware with embedded filesystem
	app.Use(fiberi18n.New(&fiberi18n.Config{
		RootPath:         "localize",
		FormatBundleFile: "yaml",
		AcceptLanguages: []language.Tag{
			language.English,
			language.Chinese,
		},
		DefaultLanguage: language.English,
		Loader:          &fiberi18n.EmbedLoader{FS: localizeFS},
	}))

	// pprof
	// path: /debug/pprof
	app.Use(pprof.New())

	// 健康检查 - 在下线时返回 503，用于 Kubernetes readiness probe
	app.Get("/health", func(c *fiber.Ctx) error {
		if rt.ShutdownMgr != nil && rt.ShutdownMgr.IsShuttingDown() {
			return http.Err(c, fiber.StatusServiceUnavailable, "shutting down")
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
			return http.Msg(c, fiber.StatusOK, "shutdown initiated")
		}

		return http.Err(c, fiber.StatusConflict, "shutdown already in progress")
	})

	// API路由
	api := app.Group(apiContextPath)
	{
		// 核心路由
		rt.routerGroup(api)
	}

	// openapi
	// openApi := app.Group("/openapi")
	// {
	// 	openApi.Get("/swagger.json", func(c *fiber.Ctx) error {
	// 		return c.SendFile("docs/swagger.json")
	// 	})
	// }

	// 找不到路径时的处理 - 必须在所有路由注册之后
	app.Use(func(c *fiber.Ctx) error {
		return http.Err(c, fiber.StatusNotFound, "request path not found")
	})

	return app
}

func (rt *Router) routerGroup(r fiber.Router) {
	auth := middleware.AuthorizationMiddleware(rt.HTTP.Auth.SecretKey, rt.Cache)

	// WebSocket
	rt.wsRouter(r, auth)

	// SCM (webhook/polling) - no auth middleware
	rt.scmRouter(r)

	// 版本信息
	r.Get("/version", func(c *fiber.Ctx) error {
		return c.JSON(version.GetVersion())
	})

	// user
	rt.userRouter(r, auth)
	rt.userExtRouter(r, auth)

	// identity
	rt.identityRouter(r, auth)

	// agent
	rt.agentRouter(r, auth)

	// team
	rt.teamRouter(r, auth)

	// storag
	rt.storageRouter(r, auth)

	// general settings
	rt.generalSettingsRouter(r, auth)

	// project
	rt.projectRouter(r, auth)

	// pipeline
	rt.pipelineRouter(r, auth)

	// secrets
	rt.secretRouter(r, auth)

	// role
	rt.roleRouter(r, auth)
}
