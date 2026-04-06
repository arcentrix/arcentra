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
	"context"
	"embed"
	"time"

	"github.com/arcentrix/arcentra/internal/case/agent"
	"github.com/arcentrix/arcentra/internal/case/execution"
	"github.com/arcentrix/arcentra/internal/case/identity"
	"github.com/arcentrix/arcentra/internal/case/pipeline"
	"github.com/arcentrix/arcentra/internal/case/project"
	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/pkg/foundation/version"
	"github.com/arcentrix/arcentra/pkg/lifecycle/shutdown"
	"github.com/arcentrix/arcentra/pkg/store/cache"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
	"github.com/arcentrix/arcentra/pkg/telemetry/trace/inject"
	"github.com/arcentrix/arcentra/pkg/transport/http"
	"github.com/arcentrix/arcentra/pkg/transport/http/middleware"
	"github.com/bytedance/sonic"
	"github.com/gofiber/contrib/fiberi18n/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/pprof"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"golang.org/x/text/language"
)

//go:embed localize/*
var localizeFS embed.FS

const apiContextPath = "/api/v1"

type Router struct {
	HTTP        *http.HTTP
	Cache       cache.ICache
	ShutdownMgr *shutdown.Manager
	AppConf     *config.AppConfig

	RegisterAgent   *agent.RegisterAgentUseCase
	GetAgent        *agent.GetAgentUseCase
	ListAgents      *agent.ListAgentsUseCase
	AgentStatistics *agent.GetAgentStatisticsUseCase
	UpdateAgent     *agent.UpdateAgentUseCase
	DeleteAgent     *agent.DeleteAgentUseCase
	UploadUC        *agent.UploadUseCase

	ManageUser *identity.ManageUserUseCase
	ManageRole *identity.ManageRoleUseCase
	ManageTeam *identity.ManageTeamUseCase

	ManageProject  *project.ManageProjectUseCase
	ManageSecret   *project.ManageSecretUseCase
	ManageSettings *project.ManageSettingsUseCase

	ManagePipeline *pipeline.ManagePipelineUseCase

	ManageStepRun *execution.ManageStepRunUseCase
}

func NewRouter(
	httpConf *http.HTTP,
	ch cache.ICache,
	shutdownMgr *shutdown.Manager,
	appConf *config.AppConfig,
	registerAgent *agent.RegisterAgentUseCase,
	getAgent *agent.GetAgentUseCase,
	listAgents *agent.ListAgentsUseCase,
	agentStats *agent.GetAgentStatisticsUseCase,
	updateAgent *agent.UpdateAgentUseCase,
	deleteAgent *agent.DeleteAgentUseCase,
	uploadUC *agent.UploadUseCase,
	manageUser *identity.ManageUserUseCase,
	manageRole *identity.ManageRoleUseCase,
	manageTeam *identity.ManageTeamUseCase,
	manageProject *project.ManageProjectUseCase,
	manageSecret *project.ManageSecretUseCase,
	manageSettings *project.ManageSettingsUseCase,
	managePipeline *pipeline.ManagePipelineUseCase,
	manageStepRun *execution.ManageStepRunUseCase,
) *Router {
	return &Router{
		HTTP: httpConf, Cache: ch, ShutdownMgr: shutdownMgr, AppConf: appConf,
		RegisterAgent: registerAgent, GetAgent: getAgent, ListAgents: listAgents,
		AgentStatistics: agentStats, UpdateAgent: updateAgent, DeleteAgent: deleteAgent,
		UploadUC:   uploadUC,
		ManageUser: manageUser, ManageRole: manageRole, ManageTeam: manageTeam,
		ManageProject: manageProject, ManageSecret: manageSecret, ManageSettings: manageSettings,
		ManagePipeline: managePipeline, ManageStepRun: manageStepRun,
	}
}

func (rt *Router) FiberApp() *fiber.App {
	bodyLimit := rt.HTTP.BodyLimit
	if bodyLimit <= 0 {
		bodyLimit = 100 * 1024 * 1024
	}

	app := fiber.New(fiber.Config{
		AppName:      "Arcentra",
		ReadTimeout:  time.Duration(rt.HTTP.ReadTimeout) * time.Second,
		WriteTimeout: time.Duration(rt.HTTP.WriteTimeout) * time.Second,
		IdleTimeout:  time.Duration(rt.HTTP.IdleTimeout) * time.Second,
		BodyLimit:    bodyLimit,
		ProxyHeader:  fiber.HeaderXForwardedFor,
	})

	app.Use(
		middleware.RealIPMiddleware(),
		middleware.RequestMiddleware(),
		inject.FiberMiddleware(),
		recover.New(),
		middleware.HTTPMetricsMiddleware(),
		middleware.CorsMiddleware(),
		middleware.UnifiedResponseMiddleware(),
		middleware.AccessLogMiddleware(),
	)

	app.Use(fiberi18n.New(&fiberi18n.Config{
		RootPath:         "localize",
		FormatBundleFile: "yaml",
		AcceptLanguages:  []language.Tag{language.English, language.Chinese},
		DefaultLanguage:  language.English,
		Loader:           &fiberi18n.EmbedLoader{FS: localizeFS},
	}))

	app.Use(pprof.New())

	app.Get("/health", func(c *fiber.Ctx) error {
		if rt.ShutdownMgr != nil && rt.ShutdownMgr.IsShuttingDown() {
			return http.Err(c, fiber.StatusServiceUnavailable, "shutting down")
		}
		return c.SendString("ok")
	})

	api := app.Group(apiContextPath)
	rt.registerRoutes(api)

	app.Use(func(c *fiber.Ctx) error {
		return http.Err(c, fiber.StatusNotFound, "request path not found")
	})

	return app
}

func (rt *Router) registerRoutes(api fiber.Router) {
	auth := middleware.AuthorizationMiddleware(rt.HTTP.Auth.SecretKey, rt.Cache)

	rt.wsRoutes(api, auth)
	rt.scmRoutes(api)

	api.Get("/version", func(c *fiber.Ctx) error {
		return c.JSON(version.GetVersion())
	})

	rt.userRoutes(api, auth)
	rt.userExtRoutes(api, auth)
	rt.identityRoutes(api, auth)
	rt.agentRoutes(api, auth)
	rt.teamRoutes(api, auth)
	rt.storageRoutes(api, auth)
	rt.generalSettingsRoutes(api, auth)
	rt.projectRoutes(api, auth)
	rt.pipelineRoutes(api, auth)
	rt.secretRoutes(api, auth)
	rt.roleRoutes(api, auth)
}

func (rt *Router) storeTokenInCache(userID string, token *identity.AuthToken, ttl time.Duration) {
	tokenInfo := http.TokenInfo{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpireAt:     token.ExpireAt,
		CreateAt:     time.Now().Unix(),
	}
	data, err := sonic.MarshalString(tokenInfo)
	if err != nil {
		log.Errorw("failed to marshal token info", "error", err)
		return
	}
	tokenKey := consts.UserTokenKey + userID
	if err := rt.Cache.Set(context.Background(), tokenKey, data, ttl).Err(); err != nil {
		log.Errorw("failed to store token in cache", "error", err)
	}
}
