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

package sandbox

import (
	"fmt"

	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/pkg/log"
)

// NewSandboxFromConfig creates a sandbox instance from agent configuration
func NewSandboxFromConfig(cfg *config.AgentConfig, logger log.Logger) (Sandbox, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}

	if !cfg.Agent.Sandbox.Enable {
		return nil, fmt.Errorf("sandbox is not enabled")
	}

	runtime := cfg.Agent.Sandbox.Runtime
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

// NewContainerdSandboxFromConfig creates a containerd sandbox from configuration
func NewContainerdSandboxFromConfig(cfg *config.ContainerdConfig, logger log.Logger) (Sandbox, error) {
	sandboxConfig := &ContainerdConfig{
		UnixSocket:   cfg.UnixSocket,
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
