// Copyright 2026 Arcentra Authors.
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

package scm

import (
	"fmt"
	"sync"
)

type ProviderFactory func(cfg ProviderConfig) (Provider, error)

var (
	mu        sync.RWMutex
	factories = map[ProviderKind]ProviderFactory{}
)

// Register registers a provider factory for the given kind.
// A later call with the same kind overrides the previous factory.
func Register(kind ProviderKind, factory ProviderFactory) {
	if kind == "" || factory == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	factories[kind] = factory
}

// NewProvider creates a provider instance from the registered factory.
// It returns an error if the kind is empty or not registered.
func NewProvider(cfg ProviderConfig) (Provider, error) {
	if cfg.Kind == "" {
		return nil, fmt.Errorf("provider kind is required")
	}
	mu.RLock()
	factory := factories[cfg.Kind]
	mu.RUnlock()
	if factory == nil {
		return nil, fmt.Errorf("provider not registered: %s", cfg.Kind)
	}
	return factory(cfg)
}
