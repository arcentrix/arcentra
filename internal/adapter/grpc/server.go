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

package grpc

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	gatewayv1 "github.com/arcentrix/arcentra/api/gateway/v1"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	streamv1 "github.com/arcentrix/arcentra/api/stream/v1"
	"github.com/arcentrix/arcentra/internal/adapter/grpc/interceptor"
	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/pkg/foundation/safe"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
	"github.com/arcentrix/arcentra/pkg/telemetry/trace/inject"
	grpcmiddleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpcrecovery "github.com/grpc-ecosystem/go-grpc-middleware/recovery"
	grpcctxtags "github.com/grpc-ecosystem/go-grpc-middleware/tags"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

type Conf = config.GrpcConf

type ServerWrapper struct {
	svr *grpc.Server
}

func NewServerWrapper(cfg Conf) *ServerWrapper {
	opts := []grpc.ServerOption{
		grpc.MaxConcurrentStreams(uint32(cfg.MaxConnections)),
		grpc.StreamInterceptor(grpcmiddleware.ChainStreamServer(
			inject.StreamServerInterceptor(),
			grpcctxtags.StreamServerInterceptor(),
			interceptor.LoggingStreamInterceptor(),
			interceptor.AuthStreamInterceptor(),
			grpcrecovery.StreamServerInterceptor(),
		)),
		grpc.UnaryInterceptor(grpcmiddleware.ChainUnaryServer(
			inject.UnaryServerInterceptor(),
			grpcctxtags.UnaryServerInterceptor(),
			interceptor.LoggingUnaryInterceptor(),
			interceptor.AuthUnaryInterceptor(),
			grpcrecovery.UnaryServerInterceptor(),
		)),
	}

	s := grpc.NewServer(opts...)
	return &ServerWrapper{svr: s}
}

func (s *ServerWrapper) Register(
	agentSvc agentv1.AgentServiceServer,
	gatewaySvc gatewayv1.GatewayServiceServer,
	stepRunSvc steprunv1.StepRunServiceServer,
	streamSvc streamv1.StreamServiceServer,
	pipelineSvc pipelinev1.PipelineServiceServer,
) {
	agentv1.RegisterAgentServiceServer(s.svr, agentSvc)
	gatewayv1.RegisterGatewayServiceServer(s.svr, gatewaySvc)
	steprunv1.RegisterStepRunServiceServer(s.svr, stepRunSvc)
	streamv1.RegisterStreamServiceServer(s.svr, streamSvc)
	pipelinev1.RegisterPipelineServiceServer(s.svr, pipelineSvc)
	reflection.Register(s.svr)
}

func (s *ServerWrapper) Start(cfg Conf) error {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	safe.Go(func() {
		log.Infow("gRPC listener started", "address", addr)
		if err := s.svr.Serve(lis); err != nil {
			log.Errorw("gRPC listener failed", "address", addr, "error", err)
		}
	})

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch

	log.Info("Shutting down gRPC server...")
	s.svr.GracefulStop()
	return nil
}

func (s *ServerWrapper) Stop() {
	s.svr.GracefulStop()
}

func (s *ServerWrapper) Server() *grpc.Server {
	return s.svr
}
