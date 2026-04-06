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

package config

import (
	"github.com/arcentrix/arcentra/pkg/store/cache"
	"github.com/arcentrix/arcentra/pkg/store/database"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
	"github.com/arcentrix/arcentra/pkg/telemetry/metrics"
	"github.com/arcentrix/arcentra/pkg/telemetry/pprof"
	"github.com/arcentrix/arcentra/pkg/transport/http"
	"github.com/google/wire"
)

// ProviderSet provides all configuration-layer dependencies.
var ProviderSet = wire.NewSet(
	ProvideConf,
	ProvideHTTPConfig,
	ProvideGrpcConfig,
	ProvideLogConfig,
	ProvideDatabaseConfig,
	ProvideRedisConfig,
	ProvideMetricsConfig,
	ProvidePprofConfig,
	ProvideKafkaSettings,
)

// ProvideConf provides the application config.
func ProvideConf(configPath string) *AppConfig {
	return NewConf(configPath)
}

// ProvideHTTPConfig provides HTTP configuration.
func ProvideHTTPConfig(appConf *AppConfig) *http.HTTP {
	httpConfig := &appConf.HTTP
	httpConfig.SetDefaults()
	return httpConfig
}

// ProvideGrpcConfig provides gRPC configuration.
func ProvideGrpcConfig(appConf *AppConfig) *GrpcConf {
	return &appConf.Grpc
}

// ProvideLogConfig provides log configuration.
func ProvideLogConfig(appConf *AppConfig) *log.Conf {
	return &appConf.Log
}

// ProvideDatabaseConfig provides database configuration.
func ProvideDatabaseConfig(appConf *AppConfig) database.Database {
	return appConf.Database
}

// ProvideRedisConfig provides Redis configuration.
func ProvideRedisConfig(appConf *AppConfig) cache.Redis {
	return appConf.Redis
}

// ProvideMetricsConfig provides metrics configuration.
func ProvideMetricsConfig(appConf *AppConfig) metrics.Config {
	metricsConfig := appConf.Metrics
	metricsConfig.SetDefaults()
	return metricsConfig
}

// ProvidePprofConfig provides pprof configuration.
func ProvidePprofConfig(appConf *AppConfig) pprof.Config {
	pprofConfig := appConf.Pprof
	pprofConfig.SetDefaults()
	return pprofConfig
}

// ProvideKafkaSettings provides Kafka settings for log consumption.
func ProvideKafkaSettings(appConf *AppConfig) KafkaSettings {
	if appConf == nil {
		return KafkaSettings{}
	}
	return KafkaSettings{
		BootstrapServers: appConf.MessageQueue.Kafka.BootstrapServers,
		SecurityProtocol: appConf.MessageQueue.Kafka.SecurityProtocol,
		Sasl: SaslSettings{
			Mechanism: appConf.MessageQueue.Kafka.Sasl.Mechanism,
			Username:  appConf.MessageQueue.Kafka.Sasl.Username,
			Password:  appConf.MessageQueue.Kafka.Sasl.Password,
		},
		Ssl: SslSettings{
			CaFile:   appConf.MessageQueue.Kafka.Ssl.CaFile,
			CertFile: appConf.MessageQueue.Kafka.Ssl.CertFile,
			KeyFile:  appConf.MessageQueue.Kafka.Ssl.KeyFile,
			Password: appConf.MessageQueue.Kafka.Ssl.Password,
		},
	}
}
