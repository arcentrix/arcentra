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
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/arcentrix/arcade/pkg/cache"
	"github.com/arcentrix/arcade/pkg/http"
	"github.com/arcentrix/arcade/pkg/log"
	"github.com/arcentrix/arcade/pkg/metrics"
	"github.com/arcentrix/arcade/pkg/pprof"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

// AgentConfig holds all configuration settings
type AgentConfig struct {
	Grpc      GrpcConfig            `mapstructure:"grpc"`
	Agent     AgentInfo             `mapstructure:"agent"`
	Log       log.Conf              `mapstructure:"log"`
	Http      http.Http             `mapstructure:"http"`
	Redis     cache.Redis           `mapstructure:"redis"`
	TaskQueue TaskQueueConfig       `mapstructure:"taskQueue"`
	Metrics   metrics.MetricsConfig `mapstructure:"metrics"`
	Pprof     pprof.PprofConfig     `mapstructure:"pprof"`
}

// TaskQueueConfig queue 任务队列配置
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

// GrpcConfig gRPC client configuration
type GrpcConfig struct {
	ServerAddr           string `mapstructure:"serverAddr"`           // server address (host:port format, e.g., "localhost:9090")
	Token                string `mapstructure:"token"`                // Bearer token for authentication (uses APIkey if not set)
	ReadWriteTimeout     int    `mapstructure:"readWriteTimeout"`     // read write timeout (seconds, default: 30)
	MaxMsgSize           int    `mapstructure:"maxMsgSize"`           // max message size (bytes), 0 means use default value
	MaxReconnectAttempts int    `mapstructure:"maxReconnectAttempts"` // max reconnection attempts, 0 means unlimited (default: 0)
}

// AgentInfo agent information
type AgentInfo struct {
	ID                string            `mapstructure:"id"`
	Name              string            `mapstructure:"name"`
	Mode              string            `mapstructure:"mode"` // Agent mode: sandbox, baremetal
	Interval          int               `mapstructure:"interval"`
	Labels            map[string]string `mapstructure:"labels"`
	MaxConcurrentJobs int               `mapstructure:"maxConcurrentJobs"` // Maximum concurrent jobs
	JobTimeout        int               `mapstructure:"jobTimeout"`        // Job timeout in seconds
	WorkspaceDir      string            `mapstructure:"workspaceDir"`      // Workspace directory
	TempDir           string            `mapstructure:"tempDir"`           // Temporary directory
	LogLevel          string            `mapstructure:"logLevel"`          // Log level
	EnableDocker      bool              `mapstructure:"enableDocker"`      // Whether Docker is enabled
	DockerNetwork     string            `mapstructure:"dockerNetwork"`     // Docker network mode
	ResourceLimits    map[string]string `mapstructure:"resourceLimits"`    // Resource limits (JSON string)
	DeniedCommands    []string          `mapstructure:"deniedCommands"`    // Denied commands (JSON string)
	EnvVars           map[string]string `mapstructure:"envVars"`           // Environment variables (JSON string)
	ProxyURL          string            `mapstructure:"proxyUrl"`          // Proxy URL
	CacheDir          string            `mapstructure:"cacheDir"`          // Cache directory
	CleanupPolicy     map[string]any    `mapstructure:"cleanupPolicy"`     // Cleanup policy (JSON string)
	Description       string            `mapstructure:"description"`       // Description
	Sandbox           SandboxConfig     `mapstructure:"sandbox"`           // Sandbox configuration
}

// SandboxConfig sandbox configuration
type SandboxConfig struct {
	Enable     bool             `mapstructure:"enable"`     // Enable sandbox, false or true
	Runtime    string           `mapstructure:"runtime"`    // Sandbox runtime: containerd, kubernetes
	Containerd ContainerdConfig `mapstructure:"containerd"` // Containerd configuration
	Kubernetes KubernetesConfig `mapstructure:"kubernetes"` // Kubernetes configuration
}

// ContainerdConfig containerd configuration
type ContainerdConfig struct {
	Network    string         `mapstructure:"network"`    // Containerd network mode: bridge, host, none
	UnixSocket string         `mapstructure:"unixSocket"` // Containerd unix socket
	Image      string         `mapstructure:"image"`      // Containerd image
	Resources  ResourceConfig `mapstructure:"resources"`  // Containerd resources
}

// ResourceConfig resource configuration
type ResourceConfig struct {
	CPU    string `mapstructure:"cpu"`    // CPU limit
	Memory string `mapstructure:"memory"` // Memory limit
}

// KubernetesConfig kubernetes configuration
type KubernetesConfig struct {
	Namespace string         `mapstructure:"namespace"` // Kubernetes namespace
	PodName   string         `mapstructure:"podName"`   // Kubernetes pod name
	Image     string         `mapstructure:"image"`     // Kubernetes pod image
	Resources ResourceConfig `mapstructure:"resources"` // Kubernetes pod resources
}

var (
	ac   AgentConfig
	mu   sync.RWMutex // 保护配置的读写
	once sync.Once
)

func NewConf(confDir string) *AgentConfig {
	once.Do(func() {
		var err error
		ac, err = loadConfigFile(confDir)
		if err != nil {
			panic(fmt.Sprintf("load config file error: %s", err))
		}
	})
	// 返回指向全局配置的指针（通过读锁保护）
	mu.RLock()
	defer mu.RUnlock()
	return &ac
}

// LoadConfigFile load config file
func loadConfigFile(confDir string) (AgentConfig, error) {

	config := viper.New()
	config.SetConfigFile(confDir)
	config.SetConfigType("toml")
	config.AutomaticEnv()

	if err := config.ReadInConfig(); err != nil {
		return ac, fmt.Errorf("failed to read configuration file: %v", err)
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
		if err := config.Unmarshal(&ac); err != nil {
			mu.Unlock()
			log.Errorw("failed to unmarshal configuration file", "error", err, "file", e.Name)
			return
		}
		if err := ac.parseServerAddr(); err != nil {
			mu.Unlock()
			log.Errorw("failed to parse server address after config reload", "error", err, "file", e.Name)
			return
		}
		mu.Unlock()
		log.Infow("configuration reloaded successfully", "file", e.Name)
	})
	if err := config.Unmarshal(&ac); err != nil {
		return ac, fmt.Errorf("failed to unmarshal configuration file: %v", err)
	}

	// parse ServerAddr to gRPC client config
	if err := ac.parseServerAddr(); err != nil {
		return ac, fmt.Errorf("failed to parse server address: %v", err)
	}

	log.Infow("config file loaded",
		"path", confDir,
		"grpc.serverAddr", ac.Grpc.ServerAddr,
	)

	return ac, nil
}

// parseServerAddr parses ServerAddr and sets gRPC client config
func (c *AgentConfig) parseServerAddr() error {
	// set default values
	if c.Grpc.ReadWriteTimeout == 0 {
		c.Grpc.ReadWriteTimeout = 30
	}

	// if ServerAddr is already set, validate and normalize it
	if c.Grpc.ServerAddr != "" {
		// try to parse as URL first
		if parsedURL, err := url.Parse(c.Grpc.ServerAddr); err == nil && parsedURL.Scheme != "" {
			// URL format: https://host:port or https://host
			host := parsedURL.Hostname()
			port := parsedURL.Port()
			if port != "" {
				c.Grpc.ServerAddr = fmt.Sprintf("%s:%s", host, port)
			} else {
				// default to port 9090 if no port specified
				c.Grpc.ServerAddr = fmt.Sprintf("%s:9090", host)
			}
			return nil
		}
		// if already in host:port format, keep it as is
		if strings.Contains(c.Grpc.ServerAddr, ":") {
			return nil
		}
		// if only host, add default port
		c.Grpc.ServerAddr = fmt.Sprintf("%s:9090", c.Grpc.ServerAddr)
		return nil
	}

	// ServerAddr is required
	return fmt.Errorf("serverAddr is required in [grpc] section")
}
