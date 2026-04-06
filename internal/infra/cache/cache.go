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

// Package cache provides infrastructure-level cache adapters.
// It wraps the shared shared/cache utilities for use within the infra layer,
// centralising cache key management and TTL policies.
package cache

import (
	"github.com/arcentrix/arcentra/pkg/store/cache"
	"github.com/google/wire"
)

var ProviderSet = wire.NewSet(
	NewCacheAdapter,
)

// CacheAdapter wraps the shared cache implementation for the infra layer.
type CacheAdapter struct {
	cache cache.ICache
}

func NewCacheAdapter(c cache.ICache) *CacheAdapter {
	return &CacheAdapter{cache: c}
}

// Cache returns the underlying cache implementation.
func (a *CacheAdapter) Cache() cache.ICache {
	return a.cache
}
