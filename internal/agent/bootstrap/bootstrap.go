// Copyright 2025 Arcade Team
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

	agentv1 "github.com/arcentrix/arcade/api/agent/v1"
	"github.com/arcentrix/arcade/internal/agent/config"
	"github.com/arcentrix/arcade/internal/agent/router"
	"github.com/arcentrix/arcade/internal/agent/service"
	grpcclient "github.com/arcentrix/arcade/internal/pkg/grpc"
	"github.com/arcentrix/arcade/internal/pkg/queue"
	"github.com/arcentrix/arcade/pkg/cron"
	"github.com/arcentrix/arcade/pkg/log"
	"github.com/arcentrix/arcade/pkg/metrics"
	"github.com/arcentrix/arcade/pkg/safe"
	"github.com/arcentrix/arcade/pkg/shutdown"
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"google.golang.org/grpc/connectivity"
)

type Agent struct {
	HttpApp       *fiber.App
	GrpcClient    *grpcclient.ClientWrapper
	QueueClient   *queue.Client // 队列客户端（任务执行者）
	MetricsServer *metrics.Server
	Logger        *log.Logger
	AgentConf     *config.AgentConfig
	AgentService  *service.AgentService
	ConfigFile    string // Configuration file path
	ShutdownMgr   *shutdown.Manager
}

type InitAppFunc func(configPath string) (*Agent, func(), error)

func NewAgent(
	rt *router.Router,
	grpcClient *grpcclient.ClientWrapper,
	queueClient *queue.Client,
	metricsServer *metrics.Server,
	logger *log.Logger,
	agentConf *config.AgentConfig,
	pipelineHandler *queue.PipelineTaskHandler,
	jobHandler *queue.JobTaskHandler,
	stepHandler *queue.StepTaskHandler,
	shutdownMgr *shutdown.Manager,
) (*Agent, func(), error) {
	httpApp := rt.Router()

	// Create agent service
	agentService := service.NewAgentService(agentConf, grpcClient)

	// 注册步骤执行处理器（Agent 作为 worker 执行步骤执行）
	if queueClient != nil {
		queueClient.RegisterHandler(queue.TaskTypePipeline, pipelineHandler)
		queueClient.RegisterHandler(queue.TaskTypeJob, jobHandler)
		queueClient.RegisterHandler(queue.TaskTypeStep, stepHandler)
	}

	cleanup := func() {
		// stop queue client
		if queueClient != nil {
			log.Info("Shutting down queue client...")
			queueClient.Shutdown()
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

		// stop global cron scheduler
		cron.Stop()
		log.Info("cron scheduler stopped")
		// close gRPC client connection
		if grpcClient != nil {
			if err := grpcClient.Close(); err != nil {
				log.Errorw("failed to close gRPC client", "error", err)
			}
		}
	}

	app := &Agent{
		HttpApp:       httpApp,
		GrpcClient:    grpcClient,
		QueueClient:   queueClient,
		MetricsServer: metricsServer,
		Logger:        logger,
		AgentConf:     agentConf,
		AgentService:  agentService,
		ShutdownMgr:   shutdownMgr,
	}
	return app, cleanup, nil
}

// Bootstrap init app, return App instance and cleanup function
func Bootstrap(configFile string, initApp InitAppFunc) (*Agent, func(), *config.AgentConfig, error) {
	// Wire build App (所有依赖都由 wire 自动注入)
	app, cleanup, err := initApp(configFile)
	if err != nil {
		return nil, nil, nil, err
	}

	// 获取配置（从 app 中获取）
	agentConf := app.AgentConf
	// 保存配置文件路径
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

	// Register Task Queue metrics if queue client is available
	if app.MetricsServer != nil && app.QueueClient != nil {
		metrics.RegisterAsynqMetricsFromQueueClient(app.MetricsServer.GetSink(), app.QueueClient)
	}

	// start metrics server
	if app.MetricsServer != nil {
		if err := app.MetricsServer.Start(); err != nil {
			log.Errorw("Metrics server failed", "error", err)
		}
	}

	// start gRPC client
	if app.GrpcClient != nil {
		safe.Go(func() {
			if err := app.GrpcClient.Start(grpcclient.ClientConf{
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

	// start queue client (Agent 作为 worker 执行步骤执行)
	if app.QueueClient != nil {
		safe.Go(func() {
			if err := app.QueueClient.Start(); err != nil {
				log.Errorw("Queue client failed", "error", err)
			}
		})
		log.Info("Queue client started")
	}

	// set signal listener (graceful shutdown)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	// start HTTP server (async)
	safe.Go(func() {
		addr := appConf.Http.Host + ":" + fmt.Sprintf("%d", appConf.Http.Port)
		log.Infow("HTTP listener started",
			"address", addr,
		)
		if err := app.HttpApp.Listen(addr); err != nil {
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

	// close components in order
	// close HTTP server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := app.HttpApp.ShutdownWithContext(shutdownCtx); err != nil {
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

	// 检查是否已注册（有token、serverAddr和agent ID）
	if appConf.Grpc.Token == "" || appConf.Grpc.ServerAddr == "" || appConf.Agent.ID == "" {
		log.Warn("Agent not registered, skipping heartbeat startup. Please configure agent.id, grpc.serverAddr and grpc.token in configuration file")
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
				// 连接成功，启动心跳
				log.Info("gRPC client connected, starting heartbeat")
				app.startPeriodicHeartbeat()
				return
			}
		}

		time.Sleep(checkInterval)
		elapsed += checkInterval
	}

	log.Warnw("wait gRPC client connection timeout, heartbeat not started", "timeout", maxWaitTime)
}

// startPeriodicHeartbeat starts a cron job that periodically calls Heartbeat to keep connection alive
func (app *Agent) startPeriodicHeartbeat() {
	appConf := app.AgentConf

	if app.AgentService == nil {
		log.Warn("AgentService is nil, skipping heartbeat setup")
		return
	}

	// Get heartbeat interval from config (default to 60 seconds)
	interval := appConf.Agent.Interval
	if interval <= 0 {
		interval = 60
	}

	// Create cron spec: @every {interval}s
	spec := fmt.Sprintf("@every %ds", interval)

	// Add heartbeat job to global cron
	err := cron.AddFunc(spec, func() {
		// Get current running step runs count from metrics
		runningStepRunsCount := getRunningStepRunsCount(app.MetricsServer)

		// Build heartbeat request
		req := &agentv1.HeartbeatRequest{
			AgentId:              appConf.Agent.ID,
			AgentName:            appConf.Agent.Name,
			Status:               agentv1.AgentStatus_AGENT_STATUS_ONLINE,
			RunningStepRunsCount: runningStepRunsCount,
			Timestamp:            time.Now().Unix(),
		}

		// Call Heartbeat through gRPC client
		if app.GrpcClient != nil && app.GrpcClient.AgentClient != nil {
			ctx, cancel := app.GrpcClient.WithTimeoutAndAuth(context.Background())
			defer cancel()

			resp, err := app.GrpcClient.AgentClient.Heartbeat(ctx, req)
			if err != nil || !resp.Success {
				log.Warnw("Periodic heartbeat failed", "error", err)
				return
			}
			log.Debugw("Heartbeat Message", "message", resp.Message, "timestamp", resp.Timestamp)

			// Update configuration from heartbeat response (except id, interval, labels)
			// if app.ConfigFile != "" {
			// 	if err := config.UpdateConfigFromHeartbeatResponse(app.ConfigFile, resp); err != nil {
			// 		log.Warnw("Failed to update config from heartbeat response", "error", err)
			// 	}
			// }
		}
	}, "agent-heartbeat")

	if err != nil {
		log.Errorw("Failed to add heartbeat cron job", "error", err)
		return
	}

	log.Infow("Added periodic heartbeat to crond", "interval", spec)

	// Do initial heartbeat immediately
	runningStepRunsCount := getRunningStepRunsCount(app.MetricsServer)
	req := &agentv1.HeartbeatRequest{
		AgentId:              appConf.Agent.ID,
		AgentName:            appConf.Agent.Name,
		Status:               agentv1.AgentStatus_AGENT_STATUS_ONLINE,
		RunningStepRunsCount: runningStepRunsCount,
		Timestamp:            time.Now().Unix(),
	}

	if app.GrpcClient != nil && app.GrpcClient.AgentClient != nil {
		ctx, cancel := app.GrpcClient.WithTimeoutAndAuth(context.Background())
		defer cancel()

		resp, err := app.GrpcClient.AgentClient.Heartbeat(ctx, req)
		if err != nil || !resp.Success {
			log.Warnw("Initial heartbeat failed", "error", err)
			return
		}
		log.Infow("Initial heartbeat Message", "message", resp.Message)

		// Update configuration from heartbeat response (except id, interval, labels)
		// if app.ConfigFile != "" {
		// 	if err := config.UpdateConfigFromHeartbeatResponse(app.ConfigFile, resp); err != nil {
		// 		log.Warnw("Failed to update config from heartbeat response", "error", err)
		// 	}
		// }
	}
}

// getRunningStepRunsCount gets the current running step runs count from prometheus metrics
// It searches for common metric names: agent_running_step_runs, running_step_runs, agent_step_runs_running
func getRunningStepRunsCount(metricsServer *metrics.Server) int32 {
	if metricsServer == nil {
		return 0
	}

	registry := metricsServer.GetRegistry()
	if registry == nil {
		return 0
	}

	// Gather all metrics
	metricFamilies, err := registry.Gather()
	if err != nil {
		log.Debugw("Failed to gather metrics", "error", err)
		return 0
	}

	// Common metric names to check (including legacy names for backward compatibility)
	metricNames := []string{
		"agent_running_step_runs",
		"running_step_runs",
		"agent_step_runs_running",
		"step_runs_running",
		// Legacy metric names (for backward compatibility)
		"agent_running_jobs",
		"running_tasks",
		"agent_running_tasks",
		"agent_tasks_running",
		"tasks_running",
	}

	// Search for the metric
	for _, mf := range metricFamilies {
		for _, name := range metricNames {
			if mf.GetName() == name {
				// Get the first metric value
				for _, metric := range mf.GetMetric() {
					if metric.Gauge != nil {
						return int32(metric.Gauge.GetValue())
					}
					if metric.Counter != nil {
						return int32(metric.Counter.GetValue())
					}
				}
			}
		}
	}

	return 0
}
