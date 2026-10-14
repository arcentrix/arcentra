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

package sandbox

import (
	"fmt"
	"strings"

	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/pkg/log"
)

// NewSandboxFromConfig 根据 Agent 配置创建独立沙箱后端，不启用 Agent 执行路径
func NewSandboxFromConfig(cfg *config.AgentConfig, logger log.Logger) (Sandbox, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}

	if !cfg.Agent.Sandbox.Enable {
		return nil, fmt.Errorf("sandbox is not enabled")
	}

	runtime := strings.ToLower(strings.TrimSpace(cfg.Agent.Sandbox.Runtime))
	if runtime == "" {
		runtime = "containerd"
	}

	switch runtime {
	case "containerd":
		return NewContainerdSandboxFromConfig(&cfg.Agent.Sandbox.Containerd, logger)
	case "kubernetes":
		return nil, fmt.Errorf("kubernetes sandbox not implemented yet")
	default:
		return nil, fmt.Errorf("unsupported sandbox runtime: %s", runtime)
	}
}

// NewContainerdSandboxFromConfig 根据配置创建 containerd 沙箱后端
func NewContainerdSandboxFromConfig(cfg *config.ContainerdConfig, logger log.Logger) (Sandbox, error) {
	if cfg == nil {
		return nil, fmt.Errorf("containerd config is required")
	}
	sandboxConfig := &ContainerdConfig{
		UnixSocket:   cfg.UnixSocket,
		RuntimeRoot:  cfg.RuntimeRoot,
		Namespace:    "arcentra",
		DefaultImage: cfg.Image,
		NetworkMode:  cfg.Network,
	}

	if cfg.Resources.CPU != "" || cfg.Resources.Memory != "" {
		sandboxConfig.Resources = &Resources{
			CPU:    cfg.Resources.CPU,
			Memory: cfg.Resources.Memory,
		}
	}

	return NewContainerdSandbox(sandboxConfig, logger)
}
