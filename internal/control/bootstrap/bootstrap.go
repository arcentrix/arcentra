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
	"errors"
	"fmt"
	"net"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
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
	ready         atomic.Bool
	grpcRequired  atomic.Bool
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
	if rt.Services != nil && rt.Services.Polling != nil {
		if err := rt.Services.ConfigurePollingAuthorization(); err != nil {
			return nil, nil, err
		}
	}
	if appConf != nil && rt.Services != nil {
		if err := rt.Services.ConfigureRetryApprovalPolicy(appConf.Pipeline.RetryApproval); err != nil {
			return nil, nil, err
		}
		if err := rt.Services.ConfigureRetryApprovalAuthorization(); err != nil {
			return nil, nil, err
		}
	}
	if appConf != nil && rt.Services != nil && rt.Services.Deployment != nil {
		if err := service.ConfigureDeployment(context.Background(), rt.Services.Deployment, appConf.Deployment); err != nil {
			if pipelineEngine != nil {
				pipelineEngine.Stop()
			}
			return nil, nil, err
		}
	}
	if rt.Services != nil && rt.Services.Deployment != nil && repos != nil {
		deploymentService := rt.Services.Deployment
		deploymentService.ConfigureDeploymentApprovals(repos.ApprovalDecision,
			service.NewDeploymentApprovalAuthorizer(repos, deploymentService),
			service.NewDeploymentRejectionAuthorizer(repos, deploymentService))
		if rt.Services.Approval != nil {
			rt.Services.Approval.SetDecisionValidator(deploymentService.ValidateApprovalDecision)
		}
		deploymentService.ConfigurePipelineDeployments(repos.JobRun, repos.StepRun,
			service.NewPipelineDeploymentAuthorizer(repos, deploymentService))
		if pipelineEngine != nil {
			pipelineEngine.ConfigurePipelineDeployments(deploymentService)
		}
	}

	// 服务可能早于执行器构造；启动时仅绑定一次，已有 gRPC 服务通过同步 getter 读取
	if pipelineEngine != nil {
		if err := rt.Services.BindPipelineEngine(pipelineEngine); err != nil {
			pipelineEngine.Stop()
			return nil, nil, err
		}
	}

	app := &App{
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

	app.grpcRequired.Store(appConf != nil && appConf.Grpc.Port > 0)
	rt.Readiness = app.Ready
	app.HTTPApp = rt.Router()

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
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			if err := grpcServer.Stop(ctx); err != nil {
				log.Errorw("Failed to stop gRPC server", "error", err)
			}
			cancel()
		}

		if rt.Services != nil && rt.Services.LogAggregator != nil {
			flushCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			if err := rt.Services.LogAggregator.Close(flushCtx); err != nil {
				log.Errorw("Failed to flush build logs during shutdown", "error", err)
			}
			cancel()
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

// Ready 表示控制器启动完成且所需 gRPC 监听器仍可服务
func (app *App) Ready() bool {
	return app != nil && app.ready.Load() && (!app.grpcRequired.Load() || app.GrpcServer.Ready()) &&
		(app.ShutdownMgr == nil || !app.ShutdownMgr.IsShuttingDown())
}

// Run 使用进程信号管理唯一的服务生命周期，并返回启动或运行失败
func Run(app *App, cleanup func()) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer cancel()
	return RunContext(ctx, app, cleanup)
}

// RunContext 在给定上下文中运行控制器，停机最多等待三十秒
func RunContext(ctx context.Context, app *App, cleanup func()) error {
	return runContext(ctx, app, cleanup, 30*time.Second)
}

func runContext(ctx context.Context, app *App, cleanup func(), shutdownTimeout time.Duration) (runErr error) {
	if app == nil || app.AppConf == nil || app.HTTPApp == nil {
		return errors.New("controller application is incomplete")
	}
	ctx, cancelRun := context.WithCancel(ctx)
	appConf := *app.AppConf
	app.grpcRequired.Store(appConf.Grpc.Port > 0)
	app.ready.Store(false)
	var httpListener *startupListener
	defer func() {
		app.ready.Store(false)
		cancelRun()
		if app.Services != nil && app.Services.CopilotHistoryMaintenance != nil {
			app.Services.CopilotHistoryMaintenance.Stop()
		}
		// Join Inbox before releasing the database.
		if app.Services != nil && app.Services.Inbox != nil {
			app.Services.Inbox.Stop()
		}
		if app.ShutdownMgr != nil {
			app.ShutdownMgr.Shutdown()
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		// 停止调度新任务并取消运行派生上下文，随后网络排空和清理共享总期限
		runErr = errors.Join(runErr, drainComponents(shutdownCtx, []func() error{func() error { cron.Stop(); return nil }}))
		// 先停止接收 HTTP 请求，gRPC 排空和其余组件清理共享总期限
		if httpListener != nil {
			_ = httpListener.Close()
		}
		tasks := []func() error{}
		if httpListener != nil {
			tasks = append(tasks, func() error { return app.HTTPApp.ShutdownWithContext(shutdownCtx) })
		}
		if app.GrpcServer != nil {
			tasks = append(tasks, func() error { return app.GrpcServer.Stop(shutdownCtx) })
		}
		runErr = errors.Join(runErr, drainComponents(shutdownCtx, tasks))
		// 依赖资源在网络请求排空之后释放，所有清理仍受同一总期限约束
		runErr = errors.Join(runErr, drainComponents(shutdownCtx, []func() error{func() error {
			if cleanup != nil {
				cleanup()
			}
			return nil
		}}))
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if app.grpcRequired.Load() {
		if app.GrpcServer == nil {
			return errors.New("configured gRPC listener is unavailable")
		}
		if err := app.GrpcServer.Start(ctx); err != nil {
			return err
		}
	}
	var lc net.ListenConfig
	bound, err := lc.Listen(ctx, "tcp", net.JoinHostPort(appConf.HTTP.Host, strconv.Itoa(appConf.HTTP.Port)))
	if err != nil {
		return fmt.Errorf("HTTP listen failed: %w", err)
	}
	httpListener = &startupListener{Listener: bound, started: make(chan struct{}), onStart: func() { app.ready.Store(true) }}
	if app.Services != nil && app.Services.Inbox != nil {
		app.Services.Inbox.Start(ctx)
	}
	if app.Services != nil && app.Services.CopilotHistoryMaintenance != nil {
		app.Services.CopilotHistoryMaintenance.Start(ctx)
	}
	// Initialize and start global cron scheduler
	cron.Init(app.Logger)
	cron.Start()
	log.Info("Cron scheduler started.")

	// Explicit GitLab snapshot polling; service bounds each scan and skips overlapping local cycles
	if app.Services != nil && app.Services.Scm != nil {
		_ = cron.AddFunc("*/1 * * * *", func() {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			_ = app.Services.Scm.PollOnce(ctx)
		}, "scm-poll")
	}

	// Agent heartbeat timeout checker — mark agents offline when last_heartbeat
	// exceeds AGENT_HEARTBEAT_EXPIRE_SECONDS (default 180s, matching 3× 60s heartbeat interval).
	if app.Services != nil && app.Services.Agent != nil {
		_ = cron.AddFunc("@every 30s", func() {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			app.Services.Agent.MarkTimeoutAgentsOffline(ctx)
		}, "agent-heartbeat-timeout")
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
		syncCtx, syncCancel := context.WithTimeout(ctx, 60*time.Second)
		cronMgr.SyncAll(syncCtx)
		syncCancel()

		_ = cron.AddFunc("*/5 * * * *", func() {
			ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
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

	var grpcDone <-chan struct{}
	if app.grpcRequired.Load() {
		grpcDone = app.GrpcServer.Done()
	}
	var shutdownRequested <-chan struct{}
	if app.ShutdownMgr != nil {
		shutdownRequested = app.ShutdownMgr.Wait()
	}
	httpDone := make(chan error, 1)
	safe.Go(func() {
		err := app.HTTPApp.Listener(httpListener)
		app.ready.Store(false)
		httpDone <- err
	})
	// 第一次 Accept 证明 HTTP 服务循环已启动，绑定失败不会暴露就绪状态
	select {
	case <-httpListener.started:
	case err := <-httpDone:
		return listenerError("HTTP", err)
	case <-grpcDone:
		return listenerError("gRPC", app.GrpcServer.Err())
	case <-ctx.Done():
		return nil
	case <-shutdownRequested:
		return nil
	}
	select {
	case err := <-httpDone:
		return listenerError("HTTP", err)
	case <-grpcDone:
		return listenerError("gRPC", app.GrpcServer.Err())
	case <-ctx.Done():
		return nil
	case <-shutdownRequested:
		return nil
	}
}

func listenerError(name string, err error) error {
	if err == nil {
		err = errors.New("listener exited unexpectedly")
	}
	return fmt.Errorf("%s server failed: %w", name, err)
}

func drainComponents(ctx context.Context, tasks []func() error) error {
	finished := make(chan error, len(tasks))
	for _, task := range tasks {
		safe.Go(func() { finished <- task() })
	}
	var result error
	for range tasks {
		select {
		case err := <-finished:
			result = errors.Join(result, err)
		case <-ctx.Done():
			return errors.Join(result, fmt.Errorf("controller shutdown deadline exceeded: %w", ctx.Err()))
		}
	}
	return result
}

type startupListener struct {
	net.Listener
	started   chan struct{}
	onStart   func()
	once      sync.Once
	closeOnce sync.Once
	closeErr  error
}

func (l *startupListener) Accept() (net.Conn, error) {
	l.once.Do(func() {
		if l.onStart != nil {
			l.onStart()
		}
		close(l.started)
	})
	return l.Listener.Accept()
}

func (l *startupListener) Close() error {
	l.closeOnce.Do(func() {
		l.closeErr = l.Listener.Close()
		if errors.Is(l.closeErr, net.ErrClosed) {
			l.closeErr = nil
		}
	})
	return l.closeErr
}
