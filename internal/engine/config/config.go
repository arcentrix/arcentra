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

package config

import (
	"fmt"
	"sync"

	"github.com/arcentrix/arcentra/internal/pkg/grpc"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"

	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/metrics"
	"github.com/arcentrix/arcentra/pkg/pprof"
	"github.com/arcentrix/arcentra/pkg/trace"
)

type TaskQueueConfig struct {
	Concurrency      int            `mapstructure:"concurrency"`
	StrictPriority   bool           `mapstructure:"strictPriority"`
	Priority         map[string]int `mapstructure:"priority"`         // 优先级配置：队列名 -> 优先级权重
	LogLevel         string         `mapstructure:"logLevel"`         // 日志级别: debug, info, warn, error
	ShutdownTimeout  int            `mapstructure:"shutdownTimeout"`  // 关闭超时时间（秒）
	GroupGracePeriod int            `mapstructure:"groupGracePeriod"` // 组优雅关闭周期（秒）
	GroupMaxDelay    int            `mapstructure:"groupMaxDelay"`    // 组最大延迟（秒）
	GroupMaxSize     int            `mapstructure:"groupMaxSize"`     // 组最大大小
}

type AppConfig struct {
	Log       log.Conf
	Grpc      grpc.Conf
	Http      http.Http
	Database  database.Database
	Redis     cache.Redis
	TaskQueue TaskQueueConfig
	Metrics   metrics.MetricsConfig
	Pprof     pprof.PprofConfig
	Trace     trace.TraceConfig
}

var (
	cfg  AppConfig
	mu   sync.RWMutex // 保护配置的读写
	once sync.Once
)

func NewConf(confDir string) *AppConfig {
	once.Do(func() {
		var err error
		cfg, err = LoadConfigFile(confDir)
		if err != nil {
			panic(fmt.Sprintf("load config file error: %s", err))
		}
	})
	mu.RLock()
	defer mu.RUnlock()
	return &cfg
}

// GetConfig 获取当前配置（用于热重载场景）
func GetConfig() AppConfig {
	mu.RLock()
	defer mu.RUnlock()
	return cfg
}

// LoadConfigFile load config file
func LoadConfigFile(confDir string) (AppConfig, error) {

	config := viper.New()
	config.SetConfigFile(confDir) //文件名
	if err := config.ReadInConfig(); err != nil {
		return cfg, fmt.Errorf("failed to read configuration file: %v", err)
	}

	config.WatchConfig()
	config.OnConfigChange(func(e fsnotify.Event) {
		log.Infow("The configuration changes, re-analyze the configuration file", "file", e.Name)
		if err := config.ReadInConfig(); err != nil {
			log.Errorw("failed to re-read configuration file", "error", err, "file", e.Name)
			return
		}
		// 使用写锁保护配置更新
		mu.Lock()
		if err := config.Unmarshal(&cfg); err != nil {
			mu.Unlock()
			log.Errorw("failed to unmarshal configuration file", "error", err, "file", e.Name)
			return
		}
		mu.Unlock()
		log.Infow("configuration reloaded successfully", "file", e.Name)
	})
	if err := config.Unmarshal(&cfg); err != nil {
		return cfg, fmt.Errorf("failed to unmarshal configuration file: %v", err)
	}
	log.Infow("config file loaded",
		"path", confDir,
	)

	return cfg, nil
}
