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

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/arcentrix/arcentra/pkg/log"
)

// Manager manages all builtin functions
// Builtin functions are core pipeline features that are not implemented as plugins, including:
// - shell: execute shell scripts and commands
// - artifacts: upload and download artifacts
// - reports: generate reports (dotenv, etc.)
type Manager struct {
	mu       sync.RWMutex
	handlers map[string]map[string]ActionHandler // builtin:action -> handler
	infos    map[string]*Info
	logger   log.Logger
}

// NewManager creates a new builtin function manager
func NewManager(logger log.Logger) *Manager {
	bm := &Manager{
		handlers: make(map[string]map[string]ActionHandler),
		infos:    make(map[string]*Info),
		logger:   logger,
	}
	bm.registerBuiltins()
	return bm
}

// registerBuiltins registers all builtin functions
func (m *Manager) registerBuiltins() {
	// Register shell builtin
	m.registerBuiltin("shell", &Info{
		Name:        "shell",
		Description: "Execute shell scripts and commands",
		Actions:     []string{"script", "command"},
	}, map[string]ActionHandler{
		"script":  m.handleShellScript,
		"command": m.handleShellCommand,
	})

	// Register artifacts builtin
	m.registerBuiltin("artifacts", &Info{
		Name:        "artifacts",
		Description: "Upload and download artifacts",
		Actions:     []string{"upload", "download"},
	}, map[string]ActionHandler{
		"upload":   m.handleArtifactsUpload,
		"download": m.handleArtifactsDownload,
	})

	// Register reports builtin
	m.registerBuiltin("reports", &Info{
		Name:        "reports",
		Description: "Generate and manage reports (dotenv, etc.)",
		Actions:     []string{"dotenv"},
	}, map[string]ActionHandler{
		"dotenv": m.handleReportsDotenv,
	})

	// Register stdout builtin
	m.registerBuiltin("stdout", &Info{
		Name:        "stdout",
		Description: "Print messages to stdout",
		Actions:     []string{"send", "send.template", "send.batch"},
	}, map[string]ActionHandler{
		"send":          m.handleStdoutSend,
		"send.template": m.handleStdoutSendTemplate,
		"send.batch":    m.handleStdoutSendBatch,
	})

	// Register scm builtin
	m.registerBuiltin("scm", &Info{
		Name:        "scm",
		Description: "SCM integration builtin for webhook parsing and event polling",
		Actions:     []string{"webhook.parse", "events.poll"},
	}, map[string]ActionHandler{
		"webhook.parse": m.handleScmWebhookParse,
		"events.poll":   m.handleScmEventsPoll,
	})
}

// registerBuiltin registers a builtin function
func (m *Manager) registerBuiltin(name string, info *Info, handlers map[string]ActionHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.handlers[name] == nil {
		m.handlers[name] = make(map[string]ActionHandler)
	}

	for action, handler := range handlers {
		m.handlers[name][action] = handler
	}

	m.infos[name] = info

	m.logger.Debugw("registered builtin", "name", name, "actions", info.Actions)
}

// Execute executes a builtin function
// builtin is the builtin function name (e.g., "shell", "artifacts")
// action is the action name (e.g., "script", "upload")
func (m *Manager) Execute(ctx context.Context, builtin, action string, params json.RawMessage, opts *Options) (json.RawMessage, error) {
	m.mu.RLock()
	builtinHandlers, exists := m.handlers[builtin]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("builtin %s not found", builtin)
	}

	handler, exists := builtinHandlers[action]
	if !exists {
		available := make([]string, 0, len(builtinHandlers))
		for a := range builtinHandlers {
			available = append(available, a)
		}
		return nil, fmt.Errorf("action %s not found in builtin %s, available actions: %v", action, builtin, available)
	}

	return handler(ctx, params, opts)
}

// GetInfo gets builtin function information
func (m *Manager) GetInfo(name string) (*Info, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	info, exists := m.infos[name]
	if !exists {
		return nil, fmt.Errorf("builtin %s not found", name)
	}

	return info, nil
}

// ListBuiltins lists all builtin functions
func (m *Manager) ListBuiltins() map[string]*Info {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*Info)
	for name, info := range m.infos {
		result[name] = info
	}
	return result
}

// IsBuiltin checks if the given uses string is a builtin function
// Supports formats:
// - "builtin:shell"
// - "shell" (if shell is a builtin, direct usage is also supported)
func (m *Manager) IsBuiltin(uses string) (string, bool) {
	// Check builtin:xxx format
	if strings.HasPrefix(uses, "builtin:") {
		builtin := strings.TrimPrefix(uses, "builtin:")
		m.mu.RLock()
		_, exists := m.handlers[builtin]
		m.mu.RUnlock()
		return builtin, exists
	}

	// Check if it's a direct builtin function name
	m.mu.RLock()
	_, exists := m.handlers[uses]
	m.mu.RUnlock()
	if exists {
		return uses, true
	}

	return "", false
}
