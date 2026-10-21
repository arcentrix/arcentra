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
	"github.com/arcentrix/arcentra/pkg/shutdown"
	"github.com/arcentrix/arcentra/pkg/trace/inject"
	"github.com/arcentrix/arcentra/pkg/version"
	"github.com/gofiber/contrib/fiberi18n/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"golang.org/x/text/language"
)

type Router struct {
	HTTP        *http.HTTP
	Cache       cache.ICache
	Services    *service.Services
	ShutdownMgr *shutdown.Manager
	AppConf     *config.AppConfig
	// Readiness 在启动依赖和 gRPC 服务就绪后返回 true
	Readiness func() bool
}

const (
	apiContextPath = "/api/v1"
	// openApiPath    = "/openapi"
)

//go:embed localize/*
var localizeFS embed.FS

func NewRouter(
	httpConf *http.HTTP,
	ch cache.ICache,
	services *service.Services,
	shutdownMgr *shutdown.Manager,
	appConf *config.AppConfig,
) *Router {
	return &Router{
		HTTP:        httpConf,
		Cache:       ch,
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

	// 公共监听器不提供管理接口，必须先于 CORS 拒绝所有方法及预检请求
	notFound := func(c *fiber.Ctx) error {
		return http.Err(c.Status(fiber.StatusNotFound), fiber.StatusNotFound, "request path not found")
	}
	app.Use("/shutdown", notFound)
	app.Use("/debug/pprof", notFound)

	// WARNING: middleware.RealIPMiddleware -> middleware.RequestMiddleware -> inject.FiberMiddleware sequence is important
	app.Use(
		middleware.RealIPMiddleware(),  // 先解析真实 IP 到 Locals("ip")
		middleware.RequestMiddleware(), // 设置 request_id 和独立可取消的 UserContext
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
	app.Use(notFound)

	return app
}

func (rt *Router) routerGroup(r fiber.Router) {
	auth := middleware.AuthorizationMiddleware(rt.HTTP.Auth.SecretKey, rt.Cache)
	subject := middleware.SubjectMiddleware(rt.Services.TeamMemberRepo)

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
	rt.meRouter(r, auth, subject)

	// identity
	rt.identityRouter(r, auth)

	// agent
	rt.agentRouter(r, auth, subject)

	// registration tokens (dynamic agent registration)
	rt.registrationTokenRouter(r, auth)

	// team
	rt.teamRouter(r, auth, subject)

	// storag
	rt.storageRouter(r, auth)

	// settings
	rt.settingRouter(r, auth)

	// project
	rt.organizationRouter(r, auth)
	rt.projectRouter(r, auth, subject)
	rt.pollingRouter(r, auth)
	rt.artifactRouter(r, auth, subject)
	rt.deploymentRouter(r, auth, subject)
	rt.copilotRouter(r, auth, subject)

	// pipeline
	rt.pipelineRouter(r, auth, subject)

	// secrets
	rt.secretRouter(r, auth, subject)

	// role
	rt.roleRouter(r, auth, subject)

	// permission dictionary
	rt.permissionRouter(r, auth, subject)

	// audit logs
	rt.auditLogRouter(r, auth, subject)

	// approval
	rt.approvalRouter(r, auth, subject)
	rt.inboxRouter(r, auth)

	// approval policies
	rt.approvalPolicyRouter(r, auth, subject)

	// pipeline templates
	rt.pipelineTemplateRouter(r, auth, subject)
}
