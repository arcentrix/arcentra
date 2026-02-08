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
