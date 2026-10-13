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
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	agentoutbox "github.com/arcentrix/arcentra/internal/agent/outbox"
	"github.com/arcentrix/arcentra/internal/agent/router"
	"github.com/arcentrix/arcentra/internal/agent/service"
	"github.com/arcentrix/arcentra/internal/agent/taskqueue"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/cron"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/metrics"
	"github.com/arcentrix/arcentra/pkg/outbox"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/shutdown"
	tasktransport "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"google.golang.org/grpc/connectivity"
)

type Agent struct {
	ready         atomic.Bool
	draining      bool
	workerMu      sync.Mutex
	stopping      bool
	logPublisher  *executor.ReliableKafkaLogPublisher
	HTTPApp       *fiber.App
	GrpcClient    *grpc.ClientWrapper
	MetricsServer *metrics.Server
	Logger        *log.Logger
	AgentConf     *config.AgentConfig
	AgentService  *service.AgentServiceImpl
	ConfigFile    string // Configuration file path
	ShutdownMgr   *shutdown.Manager
	TaskQueue     interface{ Stop() error }
	Outbox        *outbox.Outbox    // local outbox for reliable event sending; nil when agent id not set
	ExecManager   *executor.Manager // step executor (ShellExecutor + events via Outbox)
}

type InitAppFunc func(configPath string) (*Agent, func(), error)

func NewAgent(
	rt *router.Router,
	grpcClient *grpc.ClientWrapper,
	metricsServer *metrics.Server,
	logger *log.Logger,
	agentConf *config.AgentConfig,
	shutdownMgr *shutdown.Manager,
	ob *outbox.Outbox,
	execManager *executor.Manager,
) (*Agent, func(), error) {
	if err := taskqueue.ValidateExecutionConfig(agentConf); err != nil {
		return nil, nil, err
	}

	// Create agent service
	agentService := service.NewAgentServiceImpl(agentConf, grpcClient, metricsServer)
	app := &Agent{GrpcClient: grpcClient, MetricsServer: metricsServer, Logger: logger, AgentConf: agentConf, AgentService: agentService, ShutdownMgr: shutdownMgr, Outbox: ob, ExecManager: execManager}
	rt.Readiness = app.IsReady
	app.HTTPApp = rt.Router()
	cleanup := func() {
		app.workerMu.Lock()
		defer app.workerMu.Unlock()
		if app.stopping {
			return
		}
		app.stopping = true
		app.ready.Store(false)
		if app.TaskQueue != nil {
			if err := app.TaskQueue.Stop(); err != nil {
				log.Errorw("failed to stop task queue", "error", err)
			}
		}
		if app.logPublisher != nil {
			app.logPublisher.Close()
		}
		if app.Outbox != nil {
			if err := app.Outbox.Close(); err != nil {
				log.Errorw("failed to close outbox", "error", err)
			}
		}
		if metricsServer != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := metricsServer.Stop(shutdownCtx); err != nil {
				log.Errorw("failed to stop metrics server", zap.Error(err))
			}
		}
		cron.Stop()
		if err := app.AgentService.CloseIdentity(); err != nil {
			log.Errorw("failed to close agent identity store", "error", err)
		}
		if grpcClient != nil {
			if err := grpcClient.Close(); err != nil {
				log.Errorw("failed to close gRPC client", "error", err)
			}
		}
	}

	return app, cleanup, nil
}

// Bootstrap init app, return App instance and cleanup function
func Bootstrap(configFile string, initApp InitAppFunc) (*Agent, func(), *config.AgentConfig, error) {
	app, cleanup, err := initApp(configFile)
	if err != nil {
		return nil, nil, nil, err
	}

	agentConf := app.AgentConf
	app.ConfigFile = configFile

	return app, cleanup, agentConf, nil
}

// Run start app and wait for exit signal, then gracefully shutdown
func Run(app *Agent, cleanup func()) {
	appConf := app.AgentConf

	// Initialize and start global cron scheduler
	cron.Init(app.Logger)
	cron.Start()
	log.Info("Cron scheduler started.")

	// start metrics server
	if app.MetricsServer != nil {
		if err := app.MetricsServer.Start(); err != nil {
			log.Errorw("Metrics server failed", "error", err)
		}
	}

	// Freeze transport startup settings before registration changes runtime identity.
	clientConf := *config.ProvideGrpcClientConfig(appConf)
	if app.GrpcClient != nil {
		safe.Go(func() {
			if err := app.GrpcClient.Start(clientConf); err != nil {
				log.Errorw("gRPC client failed", "error", err)
			}
		})
		safe.Go(app.waitForRegistrationAndStartHeartbeat)
	}

	// set signal listener (graceful shutdown)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer signal.Stop(quit)

	// start HTTP server (async)
	safe.Go(func() {
		addr := appConf.HTTP.Host + ":" + fmt.Sprintf("%d", appConf.HTTP.Port)
		log.Infow("HTTP listener started",
			"address", addr,
		)
		if err := app.HTTPApp.Listen(addr); err != nil {
			log.Errorw("HTTP listener failed",
				"address", addr,
				"error", err,
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

	// 先停止工作队列，让运行中的步骤使用有效连接报告终态
	app.workerMu.Lock()
	app.draining = true
	app.ready.Store(false)
	if app.TaskQueue != nil {
		if err := app.TaskQueue.Stop(); err != nil {
			log.Warnw("stop agent workers", "error", err)
		}
	}
	app.workerMu.Unlock()
	// close components in order
	resp, err := app.AgentService.Unregister(context.Background(), &agentv1.UnregisterRequest{})
	if err != nil {
		log.Errorw("Agent unregistration failed", "error", err)
	}
	if resp != nil && resp.Success {
		log.Info("Agent unregistered successfully")
	} else {
		log.Error("Agent unregistration failed or returned no response")
	}

	// close HTTP server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := app.HTTPApp.ShutdownWithContext(shutdownCtx); err != nil {
		log.Errorw("HTTP server shutdown error", "error", err)
	} else {
		log.Info("HTTP server shut down gracefully")
	}

	// stop global cron scheduler
	cron.Stop()

	// close plugin manager and other resources
	cleanup()

	log.Info("Server shutdown complete")
}

// IsReady 返回已完成身份恢复、认证和工作器初始化的就绪状态
func (app *Agent) IsReady() bool {
	if app == nil || !app.ready.Load() || !app.AgentService.Authenticated() {
		return false
	}
	if app.AgentConf.Agent.Sandbox.Enable {
		probe, ok := app.TaskQueue.(interface{ OfflineRuntimeReady() bool })
		return ok && probe.OfflineRuntimeReady()
	}
	return true
}

// waitForRegistrationAndStartHeartbeat 先恢复身份，再等待连接并执行单次注册流程
func (app *Agent) waitForRegistrationAndStartHeartbeat() {
	if app.AgentConf == nil || app.AgentConf.Grpc.ServerAddr == "" {
		return
	}
	if app.AgentService == nil {
		app.AgentService = service.NewAgentServiceImpl(app.AgentConf, app.GrpcClient, app.MetricsServer)
	}
	if err := app.AgentService.PrepareIdentity(context.Background()); err != nil {
		log.Errorw("agent identity preparation failed", "error", err)
		return
	}
	maxWaitTime := 30 * time.Second
	deadline := time.Now().Add(maxWaitTime)
	for time.Now().Before(deadline) {
		app.workerMu.Lock()
		stopped := app.stopping || app.draining
		app.workerMu.Unlock()
		if stopped {
			return
		}
		if app.GrpcClient != nil {
			if conn := app.GrpcClient.GetConn(); conn != nil {
				state := conn.GetState()
				if state == connectivity.Ready || state == connectivity.Idle {
					if err := app.registerAndStart(); err != nil {
						log.Warnw("agent startup failed", "error", err)
					}
					return
				}
			}
		}
		time.Sleep(time.Second)
	}
	log.Warnw("wait gRPC client connection timeout, agent remains not ready", "timeout", maxWaitTime)
}

func (app *Agent) registerAndStart() error {
	resp, err := app.AgentService.Register(context.Background(), &agentv1.RegisterRequest{})
	if err != nil {
		return err
	}
	if resp == nil || resp.PendingApproval {
		return fmt.Errorf("agent registration is awaiting operator approval")
	}
	if _, err := app.AgentService.Heartbeat(context.Background(), nil); err != nil {
		return err
	}
	if h := app.AgentService.StorageHolder(); h != nil && h.Get() != nil {
		st, reference := h.Snapshot()
		taskqueue.SetStorage(st, reference)
	}
	return app.startWorkers()
}

func (app *Agent) startWorkers() error {
	app.workerMu.Lock()
	defer app.workerMu.Unlock()
	if app.stopping || app.draining {
		return fmt.Errorf("agent is stopping")
	}
	if !app.AgentService.Authenticated() {
		return fmt.Errorf("agent must authenticate before starting workers")
	}
	if app.ready.Load() {
		return nil
	}
	if app.ExecManager == nil {
		app.ExecManager = agentoutbox.ProvideExecutorManager(nil)
	}
	if app.Outbox == nil {
		ob, err := agentoutbox.NewRegisteredOutbox(app.AgentConf, app.GrpcClient)
		if err != nil {
			return fmt.Errorf("initialize registered agent outbox: %w", err)
		}
		app.Outbox = ob
	}
	if app.Outbox != nil {
		app.ExecManager.SetEventPublisher(agentoutbox.NewPublisher(app.Outbox), executor.EventEmitterConfig{})
	}
	if app.AgentConf.MessageQueue.Kafka.BootstrapServers != "" && app.logPublisher == nil {
		pub, err := executor.NewReliableKafkaLogPublisher(app.AgentConf.MessageQueue.Kafka, "arcentra-agent-logs-"+app.AgentConf.Agent.ID, app.AgentConf.LogSpool, filepath.Join(app.AgentConf.Agent.WorkspaceDir, ".build-log-spool"))
		if err != nil {
			return tasktransport.WrapStartupError("create agent Kafka log publisher", err)
		}
		app.logPublisher = pub
		app.ExecManager.SetLogPublisher(pub)
	}
	queue, err := taskqueue.StartWorker(context.Background(), app.AgentConf, app.GrpcClient, app.ExecManager)
	if err != nil {
		return err
	}
	app.TaskQueue = queue
	if probe, ok := queue.(interface{ OfflineRuntimeReady() bool }); ok {
		app.AgentService.SetOfflineRuntimeReadiness(probe.OfflineRuntimeReady)
	}
	app.ready.Store(true)
	return nil
}
