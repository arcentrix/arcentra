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

package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/process"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/router"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/trigger"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	"github.com/arcentrix/arcentra/pkg/cron"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/metrics"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/shutdown"
	"github.com/arcentrix/arcentra/pkg/trace"
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

type App struct {
	HTTPApp       *fiber.App
	PluginMgr     *plugin.Manager
	GrpcServer    *grpc.ServerWrapper
	MetricsServer *metrics.Server
	Logger        *log.Logger
	Storage       storage.IStorage
	AppConf       *config.AppConfig
	Repos         *repo.Repositories
	Services      *service.Services
	ShutdownMgr   *shutdown.Manager
	Engine        *process.Process
}

// InitAppFunc init app function type
type InitAppFunc func(configPath string, pluginConfigs map[string]any) (*App, func(), error)

func NewApp(
	rt *router.Router,
	logger *log.Logger,
	pluginMgr *plugin.Manager,
	grpcServer *grpc.ServerWrapper,
	metricsServer *metrics.Server,
	st storage.IStorage,
	appConf *config.AppConfig,
	_ database.IDatabase,
	repos *repo.Repositories,
	shutdownMgr *shutdown.Manager,
	pipelineEngine *process.Process,
) (*App, func(), error) {
	httpApp := rt.Router()

	// Wire the process into Services so PipelineServiceImpl can use it.
	if pipelineEngine != nil {
		rt.Services.PipelineEngine = pipelineEngine
	}

	app := &App{
		HTTPApp:       httpApp,
		PluginMgr:     pluginMgr,
		GrpcServer:    grpcServer,
		MetricsServer: metricsServer,
		Logger:        logger,
		Storage:       st,
		AppConf:       appConf,
		Repos:         repos,
		Services:      rt.Services,
		ShutdownMgr:   shutdownMgr,
		Engine:        pipelineEngine,
	}

	cleanup := func() {
		// stop pipeline process (waits for in-progress runs)
		if pipelineEngine != nil {
			pipelineEngine.Stop()
		}

		// stop metrics server
		if metricsServer != nil {
			log.Info("Shutting down metrics server...")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := metricsServer.Stop(shutdownCtx); err != nil {
				log.Errorw("Failed to stop metrics server", zap.Error(err))
			}
		}

		// stop all plugins
		if pluginMgr != nil {
			log.Info("Shutting down plugin manager...")
			if err := pluginMgr.Clear(); err != nil {
				log.Errorw("Failed to close plugin manager", zap.Error(err))
			}
		}

		// stop gRPC server
		if grpcServer != nil {
			log.Info("Shutting down gRPC server...")
			grpcServer.Stop()
		}

		// stop global cron scheduler
		cron.Stop()

		// shutdown OpenTelemetry tracing
		log.Info("Shutting down OpenTelemetry tracing...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := trace.Shutdown(shutdownCtx); err != nil {
			log.Errorw("Failed to shutdown OpenTelemetry tracing", zap.Error(err))
		}
	}

	return app, cleanup, nil
}

// Bootstrap init app, return App instance and cleanup function
func Bootstrap(configFile string, pluginConfigFile string, initApp InitAppFunc) (*App, func(), *config.AppConfig, error) {
	pluginConfigs, err := plugin.LoadPluginConfig(pluginConfigFile)
	if err != nil {
		return nil, nil, nil, err
	}

	// Wire build App (所有依赖都由 wire 自动注入)
	app, cleanup, err := initApp(configFile, pluginConfigs)
	if err != nil {
		return nil, nil, nil, err
	}

	// 获取配置（从 app 中获取）
	appConf := app.AppConf

	// Initialize OpenTelemetry Tracing (在 Run 之前，确保拦截器/中间件生效)
	if err := trace.Init(appConf.Trace); err != nil {
		// 如果 trace 初始化失败，清理已创建的资源
		if cleanup != nil {
			cleanup()
		}
		return nil, nil, nil, fmt.Errorf("failed to initialize OpenTelemetry tracing: %w", err)
	}

	return app, cleanup, appConf, nil
}

// Run start app and wait for exit signal, then gracefully shutdown
func Run(app *App, cleanup func()) {
	appConf := app.AppConf

	// Initialize and start global cron scheduler
	cron.Init(app.Logger)
	cron.Start()
	log.Info("Cron scheduler started.")

	// SCM polling job (webhook is primary, polling is fallback)
	if app.Services != nil && app.Services.Scm != nil {
		_ = cron.AddFunc("*/1 * * * *", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			_ = app.Services.Scm.PollOnce(ctx)
		}, "scm-poll")
	}

	// Pipeline cron triggers: dynamically register each pipeline's custom cron
	// expression with the scheduler. SyncAll on startup, then every 5 minutes.
	if app.Engine != nil && app.Repos != nil {
		cronMgr := trigger.NewCronTriggerManager(
			cron.Get(),
			app.Engine,
			app.Repos.Pipeline,
			app.Repos.Project,
			service.LoadPipelineDefinition,
		)
		syncCtx, syncCancel := context.WithTimeout(context.Background(), 60*time.Second)
		cronMgr.SyncAll(syncCtx)
		syncCancel()

		_ = cron.AddFunc("*/5 * * * *", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cronMgr.SyncAll(ctx)
		}, "pipeline-cron-sync")
	}

	// Wire the pipeline process into ScmService for webhook-triggered runs.
	if app.Engine != nil && app.Services != nil && app.Services.Scm != nil {
		app.Services.Scm.SetEngine(app.Engine)
	}

	// start metrics server
	if app.MetricsServer != nil {
		if err := app.MetricsServer.Start(); err != nil {
			log.Errorw("Metrics server failed: %v", err)
		}
	}

	// start gRPC server
	if app.GrpcServer != nil && appConf.Grpc.Port > 0 {
		safe.Go(func() {
			if err := app.GrpcServer.Start(appConf.Grpc); err != nil {
				log.Errorw("gRPC server failed: %v", err)
			}
		})
	}

	// set signal listener (graceful shutdown)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	// start HTTP server (async)
	safe.Go(func() {
		addr := appConf.HTTP.Host + ":" + fmt.Sprintf("%d", appConf.HTTP.Port)
		log.Infow("HTTP listener started",
			"address", addr,
		)
		if err := app.HTTPApp.Listen(addr); err != nil {
			log.Errorw("HTTP listener failed",
				"address", addr,
				zap.Error(err),
			)
		}
	})

	// wait for exit signal (either from OS signal or HTTP shutdown endpoint)
	select {
	case sig := <-quit:
		log.Infow("Received OS signal, shutting down gracefully...", "signal", sig)
		// mark as shutting down for health check
		if app.ShutdownMgr != nil {
			app.ShutdownMgr.Shutdown()
		}
	case <-app.ShutdownMgr.Wait():
		log.Info("Received shutdown request via HTTP endpoint, shutting down gracefully...")
	}

	// close components in order
	// close HTTP server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := app.HTTPApp.ShutdownWithContext(shutdownCtx); err != nil {
		log.Errorw("HTTP server shutdown error: %v", err)
	} else {
		log.Info("HTTP server shut down gracefully")
	}

	// close plugin manager and other resources
	cleanup()

	log.Info("Server shutdown complete")
}
