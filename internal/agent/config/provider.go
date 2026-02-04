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

package config

import (
	grpcclient "github.com/arcentrix/arcade/internal/pkg/grpc"
	"github.com/arcentrix/arcade/pkg/cache"
	"github.com/arcentrix/arcade/pkg/http"
	"github.com/arcentrix/arcade/pkg/log"
	"github.com/arcentrix/arcade/pkg/metrics"
	"github.com/arcentrix/arcade/pkg/pprof"
	"github.com/google/wire"
)

// ProviderSet is a Wire provider set for configuration
var ProviderSet = wire.NewSet(
	NewConf,
	ProvideHttpConfig,
	ProvideLogConfig,
	ProvideGrpcClientConfig,
	ProvideRedisConfig,
	ProvideMetricsConfig,
	ProvidePprofConfig,
)

// ProvideRedisConfig 提供 Redis 配置
func ProvideRedisConfig(agentConf *AgentConfig) cache.Redis {
	return agentConf.Redis
}

// ProvideHttpConfig 提供 HTTP 配置
func ProvideHttpConfig(agentConf *AgentConfig) *http.Http {
	httpConfig := &agentConf.Http
	httpConfig.SetDefaults()
	return httpConfig
}

// ProvideLogConfig 提供日志配置
func ProvideLogConfig(agentConf *AgentConfig) *log.Conf {
	return &agentConf.Log
}

// ProvideGrpcClientConfig 提供 gRPC 客户端配置
func ProvideGrpcClientConfig(agentConf *AgentConfig) *grpcclient.ClientConf {
	// build server address from host and port
	return &grpcclient.ClientConf{
		ServerAddr:           agentConf.Grpc.ServerAddr,
		Token:                agentConf.Grpc.Token,
		ReadWriteTimeout:     agentConf.Grpc.ReadWriteTimeout,
		MaxMsgSize:           agentConf.Grpc.MaxMsgSize,
		MaxReconnectAttempts: agentConf.Grpc.MaxReconnectAttempts,
	}
}

// ProvideMetricsConfig 提供 Metrics 配置
func ProvideMetricsConfig(agentConf *AgentConfig) metrics.MetricsConfig {
	metricsConfig := agentConf.Metrics
	metricsConfig.SetDefaults()
	return metricsConfig
}

// ProvidePprofConfig 提供 Pprof 配置
func ProvidePprofConfig(agentConf *AgentConfig) pprof.PprofConfig {
	pprofConfig := agentConf.Pprof
	pprofConfig.SetDefaults()
	return pprofConfig
}
