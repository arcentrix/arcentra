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

package grpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"gorm.io/gorm"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	gatewayv1 "github.com/arcentrix/arcentra/api/gateway/v1"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	streamv1 "github.com/arcentrix/arcentra/api/stream/v1"
	"github.com/arcentrix/arcentra/internal/shared/grpc/interceptor"
	"github.com/arcentrix/arcentra/pkg/trace/inject"
	grpcmiddleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpcrecovery "github.com/grpc-ecosystem/go-grpc-middleware/recovery"
	grpcctxtags "github.com/grpc-ecosystem/go-grpc-middleware/tags"
)

// Conf 配置
type Conf struct {
	Host             string          `mapstructure:"host"`
	Port             int             `mapstructure:"port"`
	MaxConnections   int             `mapstructure:"maxConnections"`
	ReadWriteTimeout int             `mapstructure:"readWriteTimeout"`
	Reflection       bool            `mapstructure:"reflection"`
	TLS              ServerTLSConfig `mapstructure:"tls" json:"-"`
}

// ServerWrapper 管理已加载传输配置的 gRPC 服务及其生命周期
type ServerWrapper struct {
	svr           *grpc.Server
	streamService *service.StreamServiceImpl
	config        Conf
	mu            sync.Mutex
	listener      net.Listener
	started       bool
	stopping      bool
	ready         bool
	serveErr      error
	done          chan struct{}
	stopOnce      sync.Once
	forceOnce     sync.Once
	stopDone      chan struct{}
	stopErr       error
}

// NewGrpcServer 创建 gRPC 服务，启动前加载并验证证书，不允许自动降级
func NewGrpcServer(cfg Conf) (*ServerWrapper, error) {
	if cfg.Port < 0 || cfg.Port > 65535 || cfg.MaxConnections < 0 || cfg.ReadWriteTimeout < 0 {
		return nil, errors.New("invalid gRPC server configuration")
	}
	transport, err := serverTransportCredentials(context.Background(), cfg.TLS)
	if err != nil {
		return nil, err
	}
	opts := []grpc.ServerOption{
		grpc.MaxConcurrentStreams(uint32(cfg.MaxConnections)),
		grpc.StreamInterceptor(grpcmiddleware.ChainStreamServer(
			// 注意顺序：先 trace（提取和传播 trace context），再 tags，再 logging，再 auth，最后 recovery
			inject.StreamServerInterceptor(), // OpenTelemetry trace interceptor
			grpcctxtags.StreamServerInterceptor(),
			interceptor.LoggingStreamInterceptor(), // 使用自定义日志拦截器，可过滤心跳接口
			interceptor.AuthStreamInterceptor(),    // 使用自定义认证拦截器，可跳过心跳接口
			interceptor.AuthzStreamInterceptor(),   // 使用统一授权拦截器
			grpcrecovery.StreamServerInterceptor(),
		)),
		grpc.UnaryInterceptor(grpcmiddleware.ChainUnaryServer(
			// 注意顺序：先 trace（提取和传播 trace context），再 tags，再 logging，再 auth，最后 recovery
			inject.UnaryServerInterceptor(), // OpenTelemetry trace interceptor
			grpcctxtags.UnaryServerInterceptor(),
			interceptor.LoggingUnaryInterceptor(), // 使用自定义日志拦截器，可过滤心跳接口
			interceptor.AuthUnaryInterceptor(),    // 使用自定义认证拦截器，可跳过心跳接口
			interceptor.AuthzUnaryInterceptor(),   // 使用统一授权拦截器
			grpcrecovery.UnaryServerInterceptor(),
		)),
	}

	if transport != nil {
		opts = append(opts, grpc.Creds(transport))
	}
	if cfg.ReadWriteTimeout > 0 {
		opts = append(opts, grpc.ConnectionTimeout(time.Duration(cfg.ReadWriteTimeout)*time.Second))
	}
	s := grpc.NewServer(opts...)
	if cfg.Reflection {
		reflection.Register(s)
	}
	return &ServerWrapper{svr: s, config: cfg, done: make(chan struct{}), stopDone: make(chan struct{})}, nil
}

// Register 注册所有 gRPC 服务
func (s *ServerWrapper) Register(services *service.Services,
	redisClient *redis.Client,
	db *gorm.DB,
	kafkaSettings service.KafkaSettings,
) {
	agentv1.RegisterAgentServiceServer(s.svr, service.NewAgentServiceImpl(services.Agent, services.StorageRepo))
	gatewayv1.RegisterGatewayServiceServer(s.svr, service.NewGatewayServiceImpl(repo.NewGatewayEventRepository(db)))
	steprunv1.RegisterStepRunServiceServer(s.svr, service.NewStepRunServiceImpl(services.StepRunRepo))
	s.streamService = service.NewStreamService(redisClient, db, kafkaSettings, services.LogAggregator)
	streamv1.RegisterStreamServiceServer(s.svr, s.streamService)
	pipelinev1.RegisterPipelineServiceServer(s.svr, service.NewPipelineServiceImpl(services))
}

// Start 同步绑定监听端口并异步服务，运行期错误通过 Done 和 Err 提供
// 传输和监听配置使用构造时的快照，配置热重载不会改变已有端点
func (s *ServerWrapper) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopping {
		return errors.New("gRPC server cannot be started more than once")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	addr := net.JoinHostPort(s.config.Host, strconv.Itoa(s.config.Port))
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("gRPC listen failed: %w", err)
	}
	s.listener = lis
	s.started = true
	s.ready = true
	safe.Go(func() { s.serve(lis) })
	log.Infow("gRPC listener started", "address", lis.Addr().String(), "tls", s.config.TLS.Enabled)
	return nil
}

func (s *ServerWrapper) serve(lis net.Listener) {
	err := s.svr.Serve(lis)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = false
	if !s.stopping {
		if err == nil {
			err = errors.New("gRPC listener exited unexpectedly")
		}
		s.serveErr = fmt.Errorf("gRPC serve failed: %w", err)
	}
	close(s.done)
}

// Ready 表示监听器已成功绑定且尚未退出或进入排空阶段
func (s *ServerWrapper) Ready() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready
}

// Done 在服务退出时关闭，可用于控制器生命周期监督
func (s *ServerWrapper) Done() <-chan struct{} { return s.done }

// Err 返回已发生的非预期服务退出错误
func (s *ServerWrapper) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serveErr
}

// Stop 停止接收新请求并在上下文期限内排空，超时后强制关闭连接
func (s *ServerWrapper) Stop(ctx context.Context) error {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopping = true
		s.ready = false
		if !s.started {
			close(s.done)
		}
		s.mu.Unlock()
		safe.Go(func() {
			gracefulDone := make(chan struct{})
			safe.Go(func() {
				s.svr.GracefulStop()
				close(gracefulDone)
			})
			var stopErr error
			select {
			case <-gracefulDone:
			case <-ctx.Done():
				stopErr = ctx.Err()
				s.forceStop()
			}
			if s.streamService != nil {
				stopErr = errors.Join(stopErr, s.streamService.Close(ctx))
			}
			s.stopErr = stopErr
			close(s.stopDone)
		})
	})
	select {
	case <-s.stopDone:
		return s.stopErr
	case <-ctx.Done():
		s.forceStop()
		return ctx.Err()
	}
}

func (s *ServerWrapper) forceStop() {
	s.forceOnce.Do(func() {
		// 单独运行强制关闭，确保忽略取消的处理器不能阻塞调用方
		safe.Go(s.svr.Stop)
	})
}
