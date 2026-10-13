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
	"net"
	"net/url"
	"strconv"
	"strings"

	grpcclient "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/logstream"
	"github.com/arcentrix/arcentra/pkg/metrics"
	"github.com/arcentrix/arcentra/pkg/mq/kafka"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/pprof"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/spf13/viper"
)

// AgentConfig holds all configuration settings
type AgentConfig struct {
	LogSpool     logstream.SpoolConfig `mapstructure:"logSpool" json:"logSpool"`
	Grpc         GrpcConfig            `mapstructure:"grpc"`
	Agent        AgentInfo             `mapstructure:"agent"`
	Log          log.Conf              `mapstructure:"log"`
	HTTP         http.HTTP             `mapstructure:"http"`
	Redis        cache.Redis           `mapstructure:"redis"`
	TaskQueue    nova.TaskQueueConfig  `mapstructure:"taskQueue"`
	MessageQueue MqConfig              `mapstructure:"messageQueue"`
	Outbox       OutboxConfig          `mapstructure:"outbox"`
	Metrics      metrics.Config        `mapstructure:"metrics"`
	Pprof        pprof.Config          `mapstructure:"pprof"`
}

// OutboxConfig holds agent-side outbox (WAL) settings for reliable event sending.
// WAL directory is fixed at 工作目录/data/wal (work directory = agent.workspaceDir; default "./data/wal" when workspaceDir is empty).
type OutboxConfig struct {
	// SendIntervalMs is the send loop poll interval in milliseconds (default: 50).
	SendIntervalMs int `mapstructure:"sendIntervalMs"`
	// SendBatchSize is the batch size per send call (default: 100).
	SendBatchSize int `mapstructure:"sendBatchSize"`
	// MaxDiskUsageMB is the max disk usage in MB; Append blocks when exceeded (default: 5120).
	MaxDiskUsageMB int64 `mapstructure:"maxDiskUsageMB"`
}

type MqConfig struct {
	Kafka kafka.Config `mapstructure:"kafka"`
}

// GrpcConfig gRPC client configuration
type GrpcConfig struct {
	TLS                  grpcclient.ClientTLSConfig `mapstructure:"tls"`
	ServerAddr           string                     `mapstructure:"serverAddr"`           // server address (host:port format, e.g., "localhost:9090")
	Token                string                     `mapstructure:"token"`                // Bearer token for authentication (uses APIkey if not set)
	RegistrationToken    string                     `mapstructure:"registrationToken"`    // Dynamic registration token (art_ prefix)
	ReadWriteTimeout     int                        `mapstructure:"readWriteTimeout"`     // read write timeout (seconds, default: 30)
	MaxMsgSize           int                        `mapstructure:"maxMsgSize"`           // max message size (bytes), 0 means use default value
	MaxReconnectAttempts int                        `mapstructure:"maxReconnectAttempts"` // max reconnection attempts, 0 means unlimited (default: 0)
}

// AgentInfo agent information
type AgentInfo struct {
	IdentityPersistence IdentityPersistenceConfig `mapstructure:"identityPersistence"`
	ID                  string                    `mapstructure:"id"`
	Name                string                    `mapstructure:"name"`
	Mode                string                    `mapstructure:"mode"` // Agent mode: sandbox, baremetal
	Interval            int                       `mapstructure:"interval"`
	Labels              map[string]string         `mapstructure:"labels"`
	MaxConcurrentJobs   int                       `mapstructure:"maxConcurrentJobs"` // 串行工作器仅支持 1，0 表示默认 1
	JobTimeout          int                       `mapstructure:"jobTimeout"`        // Job timeout in seconds
	WorkspaceDir        string                    `mapstructure:"workspaceDir"`      // Workspace directory
	TempDir             string                    `mapstructure:"tempDir"`           // Temporary directory
	LogLevel            string                    `mapstructure:"logLevel"`          // Log level
	EnableDocker        bool                      `mapstructure:"enableDocker"`      // Whether Docker is enabled
	DockerNetwork       string                    `mapstructure:"dockerNetwork"`     // Docker network mode
	ResourceLimits      map[string]string         `mapstructure:"resourceLimits"`    // Resource limits (JSON string)
	DeniedCommands      []string                  `mapstructure:"deniedCommands"`    // Denied commands (JSON string)
	EnvVars             map[string]string         `mapstructure:"envVars"`           // Environment variables (JSON string)
	ProxyURL            string                    `mapstructure:"proxyUrl"`          // Proxy URL
	CacheDir            string                    `mapstructure:"cacheDir"`          // Cache directory
	CleanupPolicy       map[string]any            `mapstructure:"cleanupPolicy"`     // Cleanup policy (JSON string)
	Description         string                    `mapstructure:"description"`       // Description
	Sandbox             SandboxConfig             `mapstructure:"sandbox"`           // Sandbox configuration
}

// IdentityPersistenceConfig 配置可选的进程独占身份持久化，默认禁用
// 目录必须由操作者显式指定，且位于构建工作区之外
type IdentityPersistenceConfig struct {
	Enabled   bool   `mapstructure:"enabled"`
	Directory string `mapstructure:"directory"`
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

// NewConf 读取独立的启动配置快照，配置变更必须重启 Agent
func NewConf(confDir string) (*AgentConfig, error) {
	conf, err := loadConfigFile(confDir)
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

// LoadConfigFile load config file
func loadConfigFile(confDir string) (AgentConfig, error) {
	var ac AgentConfig
	config := viper.New()
	config.SetConfigFile(confDir)
	config.SetConfigType("toml")
	config.AutomaticEnv()

	if err := config.ReadInConfig(); err != nil {
		return ac, fmt.Errorf("failed to read configuration file: %v", err)
	}

	if err := config.Unmarshal(&ac); err != nil {
		return ac, fmt.Errorf("failed to unmarshal configuration file: %v", err)
	}

	// Apply defaults/normalization after initial load (e.g. auth token expiry units).
	ac.HTTP.SetDefaults()
	if err := http.ApplyHTTPAuthExpiry(config, &ac.HTTP); err != nil {
		return ac, err
	}
	ac.Metrics.SetDefaults()
	ac.Pprof.SetDefaults()

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

	address := strings.TrimSpace(c.Grpc.ServerAddr)
	if address == "" {
		return fmt.Errorf("serverAddr is required in [grpc] section")
	}
	if address != c.Grpc.ServerAddr || strings.ContainsAny(address, "\r\n\t") {
		return fmt.Errorf("invalid gRPC server address")
	}
	if strings.Contains(address, "://") {
		parsed, err := url.Parse(address)
		if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return fmt.Errorf("invalid gRPC server URL")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("unsupported gRPC server URL scheme")
		}
		if (parsed.Scheme == "https") != c.Grpc.TLS.Enabled {
			return fmt.Errorf("gRPC server URL scheme must match explicit TLS enabled setting")
		}
		port := parsed.Port()
		if port == "" {
			port = "9090"
		}
		address = net.JoinHostPort(parsed.Hostname(), port)
	} else if !strings.Contains(address, ":") {
		address = net.JoinHostPort(address, "9090")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || strings.ContainsAny(host, "/\\ @") {
		return fmt.Errorf("gRPC server address must be host:port")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("invalid gRPC server port")
	}
	c.Grpc.ServerAddr = net.JoinHostPort(host, port)
	return nil
}
