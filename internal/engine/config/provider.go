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
	"github.com/arcentrix/arcade/internal/pkg/grpc"
	"github.com/arcentrix/arcade/pkg/cache"
	"github.com/arcentrix/arcade/pkg/database"
	"github.com/arcentrix/arcade/pkg/http"
	"github.com/arcentrix/arcade/pkg/log"
	"github.com/arcentrix/arcade/pkg/metrics"
	"github.com/arcentrix/arcade/pkg/pprof"
	"github.com/google/wire"
)

// ProviderSet 提供配置层相关的依赖
var ProviderSet = wire.NewSet(
	ProvideConf,
	ProvideHttpConfig,
	ProvideGrpcConfig,
	ProvideLogConfig,
	ProvideDatabaseConfig,
	ProvideRedisConfig,
	ProvideMetricsConfig,
	ProvidePprofConfig,
)

// ProvideConf 提供应用配置
func ProvideConf(configPath string) *AppConfig {
	return NewConf(configPath)
}

// ProvideHttpConfig 提供 HTTP 配置
func ProvideHttpConfig(appConf *AppConfig) *http.Http {
	httpConfig := &appConf.Http
	httpConfig.SetDefaults()
	return httpConfig
}

// ProvideGrpcConfig 提供 gRPC 配置
func ProvideGrpcConfig(appConf *AppConfig) *grpc.Conf {
	return &appConf.Grpc
}

// ProvideLogConfig 提供日志配置
func ProvideLogConfig(appConf *AppConfig) *log.Conf {
	return &appConf.Log
}

// ProvideDatabaseConfig 提供数据库配置
func ProvideDatabaseConfig(appConf *AppConfig) database.Database {
	return appConf.Database
}

// ProvideRedisConfig 提供 Redis 配置
func ProvideRedisConfig(appConf *AppConfig) cache.Redis {
	return appConf.Redis
}

// ProvideMetricsConfig 提供 Metrics 配置
func ProvideMetricsConfig(appConf *AppConfig) metrics.MetricsConfig {
	metricsConfig := appConf.Metrics
	metricsConfig.SetDefaults()
	return metricsConfig
}

// ProvidePprofConfig 提供 Pprof 配置
func ProvidePprofConfig(appConf *AppConfig) pprof.PprofConfig {
	pprofConfig := appConf.Pprof
	pprofConfig.SetDefaults()
	return pprofConfig
}
