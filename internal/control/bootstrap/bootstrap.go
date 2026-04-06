// Copyright 2026 Arcentra Authors.
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

	"github.com/arcentrix/arcentra/internal/adapter/grpc"
	"github.com/arcentrix/arcentra/internal/adapter/http"
	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/domain/agent"
	"github.com/arcentrix/arcentra/pkg/foundation/safe"
	"github.com/arcentrix/arcentra/pkg/integration/plugin"
	"github.com/arcentrix/arcentra/pkg/lifecycle/cron"
	"github.com/arcentrix/arcentra/pkg/lifecycle/shutdown"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
	"github.com/arcentrix/arcentra/pkg/telemetry/metrics"
	"github.com/arcentrix/arcentra/pkg/telemetry/trace"
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

type App struct {
	HTTPApp       *fiber.App
	PluginMgr     *plugin.Manager
	GrpcServer    *grpc.ServerWrapper
	MetricsServer *metrics.Server
	Logger        *log.Logger
	Storage       agent.IStorage
	AppConf       *config.AppConfig
	ShutdownMgr   *shutdown.Manager
}

type InitAppFunc func(configPath string, pluginConfigs map[string]any) (*App, func(), error)

func NewApp(
	rt *http.Router,
	logger *log.Logger,
	pluginMgr *plugin.Manager,
	grpcServer *grpc.ServerWrapper,
	metricsServer *metrics.Server,
	st agent.IStorage,
	appConf *config.AppConfig,
	shutdownMgr *shutdown.Manager,
) (*App, func(), error) {
	httpApp := rt.FiberApp()

	app := &App{
		HTTPApp:       httpApp,
		PluginMgr:     pluginMgr,
		GrpcServer:    grpcServer,
		MetricsServer: metricsServer,
		Logger:        logger,
		Storage:       st,
		AppConf:       appConf,
		ShutdownMgr:   shutdownMgr,
	}

	cleanup := func() {
		if metricsServer != nil {
			log.Info("Shutting down metrics server...")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := metricsServer.Stop(shutdownCtx); err != nil {
				log.Errorw("Failed to stop metrics server", zap.Error(err))
			}
		}

		if pluginMgr != nil {
			log.Info("Shutting down plugin manager...")
			if err := pluginMgr.Clear(); err != nil {
				log.Errorw("Failed to close plugin manager", zap.Error(err))
			}
		}

		if grpcServer != nil {
			log.Info("Shutting down gRPC server...")
			grpcServer.Stop()
		}

		cron.Stop()

		log.Info("Shutting down OpenTelemetry tracing...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := trace.Shutdown(shutdownCtx); err != nil {
			log.Errorw("Failed to shutdown OpenTelemetry tracing", zap.Error(err))
		}
	}

	return app, cleanup, nil
}

func Bootstrap(configFile string, pluginConfigFile string, initApp InitAppFunc) (*App, func(), *config.AppConfig, error) {
	pluginConfigs, err := plugin.LoadPluginConfig(pluginConfigFile)
	if err != nil {
		return nil, nil, nil, err
	}

	app, cleanup, err := initApp(configFile, pluginConfigs)
	if err != nil {
		return nil, nil, nil, err
	}

	appConf := app.AppConf

	if err := trace.Init(appConf.Trace); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, nil, nil, fmt.Errorf("failed to initialize OpenTelemetry tracing: %w", err)
	}

	return app, cleanup, appConf, nil
}

func Run(app *App, cleanup func()) {
	appConf := app.AppConf

	cron.Init(app.Logger)
	cron.Start()
	log.Info("Cron scheduler started.")

	if app.MetricsServer != nil {
		if err := app.MetricsServer.Start(); err != nil {
			log.Errorw("Metrics server failed: %v", err)
		}
	}

	if app.GrpcServer != nil && appConf.Grpc.Port > 0 {
		safe.Go(func() {
			if err := app.GrpcServer.Start(appConf.Grpc); err != nil {
				log.Errorw("gRPC server failed: %v", err)
			}
		})
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	safe.Go(func() {
		addr := appConf.HTTP.Host + ":" + fmt.Sprintf("%d", appConf.HTTP.Port)
		log.Infow("HTTP listener started", "address", addr)
		if err := app.HTTPApp.Listen(addr); err != nil {
			log.Errorw("HTTP listener failed", "address", addr, zap.Error(err))
		}
	})

	select {
	case sig := <-quit:
		log.Infow("Received OS signal, shutting down gracefully...", "signal", sig)
		if app.ShutdownMgr != nil {
			app.ShutdownMgr.Shutdown()
		}
	case <-app.ShutdownMgr.Wait():
		log.Info("Received shutdown request via HTTP endpoint, shutting down gracefully...")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := app.HTTPApp.ShutdownWithContext(shutdownCtx); err != nil {
		log.Errorw("HTTP server shutdown error: %v", err)
	} else {
		log.Info("HTTP server shut down gracefully")
	}

	cleanup()

	log.Info("Server shutdown complete")
}
