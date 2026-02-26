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

package util

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/pkg/cache"
)

const (
	ssoStateKeyPrefix = "sso:state:"
	ssoStateTTL       = 15 * time.Minute
)

// StateData contains data stored in state
type StateData struct {
	ProviderName string `json:"providerName"`
	RedirectURI  string `json:"redirectURI,omitempty"`
}

// IStateStore defines OAuth state storage (Redis-backed for multi-instance support).
type IStateStore interface {
	Store(ctx context.Context, state string, providerName string) error
	LoadAndDelete(ctx context.Context, state string) (StateData, bool)
	Check(ctx context.Context, state string) (StateData, bool)
}

// RedisStateStore stores OAuth state in Redis.
type RedisStateStore struct {
	cache cache.ICache
}

// NewRedisStateStore creates a Redis-backed state store.
func NewRedisStateStore(c cache.ICache) *RedisStateStore {
	return &RedisStateStore{cache: c}
}

// Store stores state data in Redis with TTL.
func (s *RedisStateStore) Store(ctx context.Context, state string, providerName string) error {
	if s.cache == nil {
		return fmt.Errorf("cache is not configured")
	}
	data := StateData{ProviderName: providerName}
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal state data: %w", err)
	}
	key := ssoStateKeyPrefix + state
	return s.cache.Set(ctx, key, b, ssoStateTTL).Err()
}

// LoadAndDelete loads state from Redis and deletes it (one-time use).
func (s *RedisStateStore) LoadAndDelete(ctx context.Context, state string) (StateData, bool) {
	if s.cache == nil {
		return StateData{}, false
	}
	key := ssoStateKeyPrefix + state
	val, err := s.cache.Get(ctx, key).Result()
	if err != nil {
		return StateData{}, false
	}
	_ = s.cache.Del(ctx, key)
	var data StateData
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		// Backward compatibility: plain string as providerName
		return StateData{ProviderName: val}, true
	}
	return data, true
}

// Check checks if state exists without deleting (for debugging).
func (s *RedisStateStore) Check(ctx context.Context, state string) (StateData, bool) {
	if s.cache == nil {
		return StateData{}, false
	}
	key := ssoStateKeyPrefix + state
	val, err := s.cache.Get(ctx, key).Result()
	if err != nil {
		return StateData{}, false
	}
	var data StateData
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		return StateData{ProviderName: val}, true
	}
	return data, true
}

// GenState generates a random state string.
func GenState() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		return ""
	}
	return base64.URLEncoding.EncodeToString(b)
}
