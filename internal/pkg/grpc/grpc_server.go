// Copyright 2025 Arcentra Team
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
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/arcentrix/arcentra/internal/engine/service"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"gorm.io/gorm"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	streamv1 "github.com/arcentrix/arcentra/api/stream/v1"
	"github.com/arcentrix/arcentra/internal/pkg/grpc/interceptor"
	"github.com/arcentrix/arcentra/pkg/trace/inject"
	grpcmiddleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpcrecovery "github.com/grpc-ecosystem/go-grpc-middleware/recovery"
	grpcctxtags "github.com/grpc-ecosystem/go-grpc-middleware/tags"
)

// Conf 配置
type Conf struct {
	Host             string
	Port             int
	MaxConnections   int
	ReadWriteTimeout int
}

type ServerWrapper struct {
	svr *grpc.Server
}

// NewGrpcServer 创建 gRPC 服务
func NewGrpcServer(cfg Conf) *ServerWrapper {
	opts := []grpc.ServerOption{
		grpc.MaxConcurrentStreams(uint32(cfg.MaxConnections)),
		grpc.StreamInterceptor(grpcmiddleware.ChainStreamServer(
			// 注意顺序：先 trace（提取和传播 trace context），再 tags，再 logging，再 auth，最后 recovery
			inject.StreamServerInterceptor(), // OpenTelemetry trace interceptor
			grpcctxtags.StreamServerInterceptor(),
			interceptor.LoggingStreamInterceptor(), // 使用自定义日志拦截器，可过滤心跳接口
			interceptor.AuthStreamInterceptor(),    // 使用自定义认证拦截器，可跳过心跳接口
			grpcrecovery.StreamServerInterceptor(),
		)),
		grpc.UnaryInterceptor(grpcmiddleware.ChainUnaryServer(
			// 注意顺序：先 trace（提取和传播 trace context），再 tags，再 logging，再 auth，最后 recovery
			inject.UnaryServerInterceptor(), // OpenTelemetry trace interceptor
			grpcctxtags.UnaryServerInterceptor(),
			interceptor.LoggingUnaryInterceptor(), // 使用自定义日志拦截器，可过滤心跳接口
			interceptor.AuthUnaryInterceptor(),    // 使用自定义认证拦截器，可跳过心跳接口
			grpcrecovery.UnaryServerInterceptor(),
		)),
	}

	s := grpc.NewServer(opts...)
	return &ServerWrapper{
		svr: s,
	}
}

// Register 注册所有 gRPC 服务
func (s *ServerWrapper) Register(services *service.Services, redisClient *redis.Client, mysqlDB *gorm.DB, kafkaSettings service.KafkaSettings) {
	agentv1.RegisterAgentServiceServer(s.svr, service.NewAgentServiceImpl(services.Agent))
	steprunv1.RegisterStepRunServiceServer(s.svr, &service.StepRunServiceImpl{})
	streamv1.RegisterStreamServiceServer(s.svr, service.NewStreamService(redisClient, mysqlDB, kafkaSettings))
	pipelinev1.RegisterPipelineServiceServer(s.svr, &service.PipelineServiceImpl{})
	// reflection（调试）
	reflection.Register(s.svr)
}

// Start 启动服务
func (s *ServerWrapper) Start(cfg Conf) error {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	safe.Go(func() {
		log.Infow("gRPC listener started",
			"address", addr,
		)
		if err := s.svr.Serve(lis); err != nil {
			log.Errorw("gRPC listener failed",
				"address", addr,
				"error", err,
			)
		}
	})

	// 优雅退出
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch

	log.Info("Shutting down gRPC server...")
	s.svr.GracefulStop()
	return nil
}

// Stop 手动停止
func (s *ServerWrapper) Stop() {
	s.svr.GracefulStop()
}
