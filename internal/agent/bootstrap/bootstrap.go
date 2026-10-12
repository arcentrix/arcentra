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
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"google.golang.org/grpc/connectivity"
)

type Agent struct {
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
	httpApp := rt.Router()

	// Create agent service
	agentService := service.NewAgentServiceImpl(agentConf, grpcClient, metricsServer)
	app := &Agent{HTTPApp: httpApp, GrpcClient: grpcClient, MetricsServer: metricsServer, Logger: logger, AgentConf: agentConf, AgentService: agentService, ShutdownMgr: shutdownMgr, Outbox: ob, ExecManager: execManager}
	cleanup := func() {
		app.workerMu.Lock()
		defer app.workerMu.Unlock()
		if app.stopping {
			return
		}
		app.stopping = true
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

	// start gRPC client
	if app.GrpcClient != nil {
		safe.Go(func() {
			if err := app.GrpcClient.Start(grpc.ClientConf{
				ServerAddr:           appConf.Grpc.ServerAddr,
				Token:                appConf.Grpc.Token,
				ReadWriteTimeout:     appConf.Grpc.ReadWriteTimeout,
				MaxMsgSize:           appConf.Grpc.MaxMsgSize,
				MaxReconnectAttempts: appConf.Grpc.MaxReconnectAttempts,
			}); err != nil {
				log.Errorw("gRPC client failed", "error", err)
			}
		})

		// 等待 gRPC 客户端连接成功后，检查是否已注册，如果已注册则启动心跳
		// 心跳将在注册成功后启动，而不是在启动时检查配置
		safe.Go(func() {
			app.waitForRegistrationAndStartHeartbeat()
		})
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
		message := "no response"
		if resp != nil {
			message = resp.Message
		}
		log.Errorw("Agent unregistration failed", "error", message)
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

// waitForRegistrationAndStartHeartbeat waits for gRPC client connection and checks if agent is registered,
// then starts heartbeat if registration is successful
func (app *Agent) waitForRegistrationAndStartHeartbeat() {
	appConf := app.AgentConf

	// Check for dynamic registration token first, then fall back to legacy token+agentID
	hasRegistrationToken := appConf.Grpc.RegistrationToken != ""
	hasLegacyConfig := appConf.Grpc.Token != "" && appConf.Agent.ID != ""

	if appConf.Grpc.ServerAddr == "" {
		log.Warn("Agent not registered, skipping heartbeat startup. " +
			"Please configure grpc.serverAddr in configuration file")
		return
	}

	if !hasRegistrationToken && !hasLegacyConfig {
		log.Warn("Agent not registered, skipping heartbeat startup. " +
			"Please configure grpc.registrationToken or (agent.id + grpc.token) in configuration file")
		return
	}

	// 等待 gRPC 客户端连接成功
	maxWaitTime := 30 * time.Second
	checkInterval := 1 * time.Second
	elapsed := time.Duration(0)

	for elapsed < maxWaitTime {
		if app.GrpcClient == nil {
			time.Sleep(checkInterval)
			elapsed += checkInterval
			continue
		}

		conn := app.GrpcClient.GetConn()
		if conn != nil {
			state := conn.GetState()
			if state == connectivity.Ready || state == connectivity.Idle {
				// 连接成功，先注册，再启动心跳
				log.Info("gRPC client connected, registering agent")
				if _, err := app.AgentService.Register(context.Background(), &agentv1.RegisterRequest{}); err != nil {
					log.Warnw("agent registration failed", "error", err)
					return
				}
				if h := app.AgentService.StorageHolder(); h != nil && h.Get() != nil {
					taskqueue.SetStorage(h.Get())
				}
				if err := app.startWorkers(); err != nil {
					log.Errorw("agent worker startup failed", "error", err)
					return
				}
				log.Info("agent registration successful, starting heartbeat")
				resp, err := app.AgentService.Heartbeat(context.Background(), nil)
				if err != nil {
					log.Warnw("heartbeat failed", "error", err)
					return
				}
				if resp != nil && resp.Success {
					log.Info("heartbeat successful")
				} else {
					message := "no response"
					if resp != nil {
						message = resp.Message
					}
					log.Warnw("heartbeat failed", "error", message)
					return
				}
				return
			}
		}

		time.Sleep(checkInterval)
		elapsed += checkInterval
	}

	log.Warnw("wait gRPC client connection timeout, heartbeat not started", "timeout", maxWaitTime)
}

func (app *Agent) startWorkers() error {
	app.workerMu.Lock()
	defer app.workerMu.Unlock()
	if app.stopping || app.draining || app.TaskQueue != nil {
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
			return err
		}
		app.logPublisher = pub
		app.ExecManager.SetLogPublisher(pub)
	}
	queue, err := taskqueue.StartWorker(context.Background(), app.AgentConf, app.GrpcClient, app.ExecManager)
	if err != nil {
		return err
	}
	app.TaskQueue = queue
	return nil
}
