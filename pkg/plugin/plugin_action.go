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

// Package plugin action registry for unified action management
package plugin

import (
	"encoding/json"
	"fmt"
	"sync"
)

// ActionHandler is a function that handles a specific action
// params: action-specific parameters (JSON)
// opts: optional overrides (JSON, e.g., timeout, workdir, env)
// Returns: action result (JSON) and error
type ActionHandler func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error)

// ActionInfo contains metadata about an action
type ActionInfo struct {
	// Name is the action name (e.g., "send", "build", "clone")
	Name string `json:"name"`
	// Description describes what the action does
	Description string `json:"description"`
	// Handler is the function that handles this action
	Handler ActionHandler `json:"-"`
	// ParamsSchema is optional JSON schema for parameters validation
	Args json.RawMessage `json:"args,omitempty"`
	// ResultSchema is optional JSON schema for result validation
	Returns json.RawMessage `json:"returns,omitempty"`
}

// ActionRegistry manages action handlers for plugins
// Provides unified action routing, extensibility, and multi-language support
type ActionRegistry struct {
	mu       sync.RWMutex
	actions  map[string]*ActionInfo
	metadata map[string]json.RawMessage // Additional metadata per action
}

// Base provides a base implementation for plugins using ActionRegistry
// Plugins can embed this struct and register their actions during Init
type Base struct {
	registry *ActionRegistry
}

// NewPluginBase creates a new plugin base with action registry
func NewPluginBase() *Base {
	return &Base{
		registry: NewActionRegistry(),
	}
}

// Registry returns the action registry
func (p *Base) Registry() *ActionRegistry {
	return p.registry
}

// Execute executes an action using the registry
// This is the unified entry point for all plugin operations
func (p *Base) Execute(action string, params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	return p.registry.Execute(action, params, opts)
}

// NewActionRegistry creates a new action registry
func NewActionRegistry() *ActionRegistry {
	return &ActionRegistry{
		actions:  make(map[string]*ActionInfo),
		metadata: make(map[string]json.RawMessage),
	}
}

// Register registers an action handler
// If an action with the same name already exists, it will be replaced
func (r *ActionRegistry) Register(info *ActionInfo) error {
	if info == nil {
		return fmt.Errorf("action info cannot be nil")
	}
	if info.Name == "" {
		return fmt.Errorf("action name cannot be empty")
	}
	if info.Handler == nil {
		return fmt.Errorf("action handler cannot be nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.actions[info.Name] = info
	return nil
}

// RegisterFunc registers an action handler using a function
// This is a convenience method for simple action handlers
func (r *ActionRegistry) RegisterFunc(name, description string, handler ActionHandler) error {
	return r.Register(&ActionInfo{
		Name:        name,
		Description: description,
		Handler:     handler,
	})
}

// Unregister removes an action handler
func (r *ActionRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.actions, name)
	delete(r.metadata, name)
}

// Get retrieves an action handler by name
func (r *ActionRegistry) Get(name string) (*ActionInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.actions[name]
	return info, ok
}

// Execute executes an action by name
// This is the unified entry point for all plugin operations
func (r *ActionRegistry) Execute(action string, params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	r.mu.RLock()
	info, ok := r.actions[action]
	r.mu.RUnlock()

	if !ok {
		// Return list of available actions for better error message
		available := r.ListActions()
		return nil, fmt.Errorf("unknown action: %s, available actions: %v", action, available)
	}

	return info.Handler(params, opts)
}

// ListActions returns a list of all registered action names
func (r *ActionRegistry) ListActions() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	actions := make([]string, 0, len(r.actions))
	for name := range r.actions {
		actions = append(actions, name)
	}
	return actions
}

// ListActionInfos returns detailed information about all registered actions
func (r *ActionRegistry) ListActionInfos() []*ActionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	infos := make([]*ActionInfo, 0, len(r.actions))
	for _, info := range r.actions {
		// Create a copy without the handler (for JSON serialization)
		infos = append(infos, &ActionInfo{
			Name:        info.Name,
			Description: info.Description,
			Args:        info.Args,
			Returns:     info.Returns,
		})
	}
	return infos
}

// Count returns the number of registered actions
func (r *ActionRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.actions)
}

// SetMetadata sets additional metadata for an action
func (r *ActionRegistry) SetMetadata(action string, metadata json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.actions[action]; !ok {
		return fmt.Errorf("action %s not found", action)
	}

	r.metadata[action] = metadata
	return nil
}

// GetMetadata retrieves metadata for an action
func (r *ActionRegistry) GetMetadata(action string) (json.RawMessage, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	metadata, ok := r.metadata[action]
	return metadata, ok
}

// Clear removes all registered actions
func (r *ActionRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions = make(map[string]*ActionInfo)
	r.metadata = make(map[string]json.RawMessage)
}
