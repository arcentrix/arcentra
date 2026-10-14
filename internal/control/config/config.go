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
	"fmt"

	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/shared/deployment"
	"github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/mq/kafka"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/spf13/viper"

	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/logstream"
	"github.com/arcentrix/arcentra/pkg/metrics"
	"github.com/arcentrix/arcentra/pkg/pprof"
	"github.com/arcentrix/arcentra/pkg/trace"
)

type EventsConfig struct {
	SourcePrefix string `mapstructure:"sourcePrefix"`
	Timeout      int    `mapstructure:"timeout"`
}

type MessageQueueConfig struct {
	Kafka kafka.Config `mapstructure:"kafka"`
}

type TaskQueueConfig struct {
	Type              string `mapstructure:"type"`
	DelaySlotCount    int    `mapstructure:"delaySlotCount"`
	DelaySlotDuration int    `mapstructure:"delaySlotDuration"`
	AutoCommit        bool   `mapstructure:"autoCommit"`
	SessionTimeout    int    `mapstructure:"sessionTimeout"`
	MaxPollInterval   int    `mapstructure:"maxPollInterval"`
	MessageFormat     string `mapstructure:"messageFormat"`
	MessageCodec      string `mapstructure:"messageCodec"`
}

// AuthzConfig 定义授权模块配置
type AuthzConfig struct {
	Enabled          bool   `mapstructure:"enabled" json:"enabled"`
	Enforcer         string `mapstructure:"enforcer" json:"enforcer"`
	CacheTTLSeconds  int    `mapstructure:"cacheTtlSeconds" json:"cacheTtlSeconds"`
	PolicySyncEnable bool   `mapstructure:"policySyncEnable" json:"policySyncEnable"`
}

type AppConfig struct {
	Deployment   deployment.Config     `mapstructure:"deployment" json:"-"`
	Copilot      copilot.Config        `mapstructure:"copilot" json:"-"`
	LogSpool     logstream.SpoolConfig `mapstructure:"logSpool" json:"logSpool"`
	Log          log.Conf              `mapstructure:"log" json:"Log"`
	Grpc         grpc.Conf             `mapstructure:"grpc" json:"Grpc"`
	HTTP         http.HTTP             `mapstructure:"http" json:"Http"`
	Database     database.Database     `mapstructure:"database" json:"Database"`
	Redis        cache.Redis           `mapstructure:"redis" json:"Redis"`
	Events       EventsConfig          `mapstructure:"events" json:"Events"`
	MessageQueue MessageQueueConfig    `mapstructure:"messageQueue" json:"MessageQueue"`
	Metrics      metrics.Config        `mapstructure:"metrics" json:"Metrics"`
	Pprof        pprof.Config          `mapstructure:"pprof" json:"Pprof"`
	Trace        trace.Config          `mapstructure:"trace" json:"Trace"`
	Authz        AuthzConfig           `mapstructure:"authz" json:"Authz"`
	TaskQueue    nova.TaskQueueConfig  `mapstructure:"taskQueue" json:"TaskQueue"`
	Pipeline     PipelineConfig        `mapstructure:"pipeline" json:"Pipeline"`
}

// PipelineConfig holds pipeline process settings.
type PipelineConfig struct {
	RetryApproval  *retryapproval.Policy          `mapstructure:"retryApproval" json:"retryApproval"`
	OfflineRuntime taskqueue.OfflineRuntimePolicy `mapstructure:"offlineRuntime" json:"offlineRuntime"`
	KeepWorkspace  bool                           `mapstructure:"keepWorkspace" json:"keepWorkspace"`
}

// NewConf 读取独立的启动配置快照，配置和证书变更必须重启控制器
func NewConf(confDir string) (*AppConfig, error) {
	conf, err := LoadConfigFile(confDir)
	if err != nil {
		return nil, err
	}
	if conf.MessageQueue.Kafka.BootstrapServers != "" {
		if _, err := taskqueue.ResolveMessageCodec(conf.TaskQueue.MessageFormat, conf.TaskQueue.MessageCodec); err != nil {
			return nil, err
		}
	}
	return &conf, nil
}

// LoadConfigFile 读取配置到独立结构，不监听或热更新运行中的安全配置
func LoadConfigFile(confDir string) (AppConfig, error) {
	var cfg AppConfig
	config := viper.New()
	config.SetConfigFile(confDir)
	if err := config.ReadInConfig(); err != nil {
		return cfg, fmt.Errorf("failed to read configuration file: %w", err)
	}
	if err := config.Unmarshal(&cfg); err != nil {
		return cfg, fmt.Errorf("failed to unmarshal configuration file: %w", err)
	}
	if err := cfg.Pipeline.OfflineRuntime.ValidateProduction(); err != nil {
		return cfg, err
	}
	cfg.HTTP.SetDefaults()
	if err := http.ApplyHTTPAuthExpiry(config, &cfg.HTTP); err != nil {
		return cfg, err
	}
	cfg.Metrics.SetDefaults()
	cfg.Pprof.SetDefaults()
	log.Infow("config file loaded", "path", confDir)
	return cfg, nil
}
