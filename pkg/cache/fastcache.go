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

package cache

import (
	"context"
	"sync"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/v9"
)

// FastCacheConfig holds fastcache configuration
type FastCacheConfig struct {
	MaxBytes int // Maximum bytes for fastcache, default 16MB
}

// FastCache is a local cache implementation using VictoriaMetrics fastcache
// It provides a fast, in-memory cache with automatic expiration support
type FastCache struct {
	cache    *fastcache.Cache
	ttls     sync.Map // map[string]time.Time for tracking expiration
	hashData sync.Map // map[string]map[string]string for hash data storage
	mu       sync.RWMutex
}

// NewFastCache creates a new FastCache instance
func NewFastCache(conf FastCacheConfig) *FastCache {
	maxBytes := conf.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 16 * 1024 * 1024 // default 16MB
	}

	return &FastCache{
		cache: fastcache.New(maxBytes),
	}
}

// Get returns the value for the given key
func (fc *FastCache) Get(ctx context.Context, key string) *redis.StringCmd {
	fc.mu.RLock()
	defer fc.mu.RUnlock()

	// Check if key has expired
	if exp, ok := fc.ttls.Load(key); ok {
		if time.Now().After(exp.(time.Time)) {
			// Key has expired, return nil
			return &redis.StringCmd{}
		}
	}

	value := fc.cache.Get(nil, []byte(key))
	if value == nil {
		return &redis.StringCmd{}
	}

	// Wrap the value in a redis.StringCmd for compatibility
	cmd := &redis.StringCmd{}
	cmd.SetVal(string(value))
	return cmd
}

// Set sets the value for the given key with expiration
func (fc *FastCache) Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	// Convert value to bytes
	var valueBytes []byte
	switch v := value.(type) {
	case string:
		valueBytes = []byte(v)
	case []byte:
		valueBytes = v
	default:
		// Use sonic for JSON serialization
		data, err := sonic.Marshal(v)
		if err != nil {
			cmd := &redis.StatusCmd{}
			cmd.SetErr(err)
			return cmd
		}
		valueBytes = data
	}

	fc.cache.Set([]byte(key), valueBytes)

	// Store expiration time if provided
	if expiration > 0 {
		fc.ttls.Store(key, time.Now().Add(expiration))
		// Start a goroutine to clean up expired keys using safe.Go
		safe.GoWith(func(args cleanupArgs) {
			fc.cleanupExpiredKeyWithDelay(args.key, args.delay)
		}, cleanupArgs{key: key, delay: expiration})
	}

	cmd := &redis.StatusCmd{}
	cmd.SetVal("OK")
	return cmd
}

// cleanupArgs holds arguments for cleanup goroutine
type cleanupArgs struct {
	key   string
	delay time.Duration
}

// cleanupExpiredKeyWithDelay waits for the specified delay then cleans up expired key
func (fc *FastCache) cleanupExpiredKeyWithDelay(key string, delay time.Duration) {
	<-time.After(delay)
	fc.cleanupExpiredKey(key)
}

// Del deletes the given keys
func (fc *FastCache) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	count := 0
	for _, key := range keys {
		// Check if key exists before deleting
		if fc.cache.Has([]byte(key)) {
			fc.cache.Del([]byte(key))
			count++
			fc.ttls.Delete(key)
		}
	}

	cmd := &redis.IntCmd{}
	cmd.SetVal(int64(count))
	return cmd
}

// Pipeline returns nil as fastcache does not support Redis pipelines.
// Use HybridCache with Redis for pipeline operations.
func (fc *FastCache) Pipeline() redis.Pipeliner {
	return nil
}

// HSet sets hash fields
func (fc *FastCache) HSet(ctx context.Context, key string, values ...any) *redis.IntCmd {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	if len(values)%2 != 0 {
		cmd := &redis.IntCmd{}
		cmd.SetErr(redis.Nil)
		return cmd
	}

	// Get existing hash data or create new
	var hashMap map[string]string
	if existing, ok := fc.hashData.Load(key); ok {
		hashMap = existing.(map[string]string)
	} else {
		hashMap = make(map[string]string)
	}

	// Count new fields added
	newFields := int64(0)

	// Parse input values (field-value pairs)
	for i := 0; i < len(values); i += 2 {
		field := values[i]
		value := values[i+1]

		fieldStr, _ := toString(field)
		valueStr, _ := toString(value)

		// Check if this is a new field
		if _, exists := hashMap[fieldStr]; !exists {
			newFields++
		}
		hashMap[fieldStr] = valueStr
	}

	// Store hash data in sync.Map
	fc.hashData.Store(key, hashMap)

	// Also mark in fastcache that this hash key exists (for Has check)
	fc.cache.Set([]byte(key+":hash"), []byte("1"))

	cmd := &redis.IntCmd{}
	cmd.SetVal(newFields)
	return cmd
}

// HGetAll returns all hash fields and values
func (fc *FastCache) HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd {
	fc.mu.RLock()
	defer fc.mu.RUnlock()

	cmd := &redis.MapStringStringCmd{}

	// Check if key has expired
	if exp, ok := fc.ttls.Load(key); ok {
		if time.Now().After(exp.(time.Time)) {
			cmd.SetVal(make(map[string]string))
			return cmd
		}
	}

	// Retrieve hash data from sync.Map
	if existing, ok := fc.hashData.Load(key); ok {
		hashMap := existing.(map[string]string)
		// Create a copy to prevent external modification
		result := make(map[string]string, len(hashMap))
		for k, v := range hashMap {
			result[k] = v
		}
		cmd.SetVal(result)
	} else {
		cmd.SetVal(make(map[string]string))
	}
	return cmd
}

// HGet returns the value of a hash field
func (fc *FastCache) HGet(ctx context.Context, key, field string) *redis.StringCmd {
	fc.mu.RLock()
	defer fc.mu.RUnlock()

	cmd := &redis.StringCmd{}

	// Check if key has expired
	if exp, ok := fc.ttls.Load(key); ok {
		if time.Now().After(exp.(time.Time)) {
			cmd.SetErr(redis.Nil)
			return cmd
		}
	}

	// Retrieve hash data from sync.Map
	if existing, ok := fc.hashData.Load(key); ok {
		hashMap := existing.(map[string]string)
		if value, exists := hashMap[field]; exists {
			cmd.SetVal(value)
			return cmd
		}
	}

	cmd.SetErr(redis.Nil)
	return cmd
}

// HDel deletes hash fields
func (fc *FastCache) HDel(ctx context.Context, key string, fields ...string) *redis.IntCmd {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	cmd := &redis.IntCmd{}
	deleted := int64(0)

	// Retrieve hash data from sync.Map
	if existing, ok := fc.hashData.Load(key); ok {
		hashMap := existing.(map[string]string)
		for _, field := range fields {
			if _, exists := hashMap[field]; exists {
				delete(hashMap, field)
				deleted++
			}
		}

		// If hash is empty, remove the key entirely
		if len(hashMap) == 0 {
			fc.hashData.Delete(key)
			fc.cache.Del([]byte(key + ":hash"))
		} else {
			fc.hashData.Store(key, hashMap)
		}
	}

	cmd.SetVal(deleted)
	return cmd
}

// Expire sets the expiration time for a key
func (fc *FastCache) Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	// Check if key exists
	if !fc.cache.Has([]byte(key)) {
		cmd := &redis.BoolCmd{}
		cmd.SetVal(false)
		return cmd
	}

	if expiration > 0 {
		fc.ttls.Store(key, time.Now().Add(expiration))
		// Start a goroutine to clean up expired keys using safe.Go
		safe.GoWith(func(args cleanupArgs) {
			fc.cleanupExpiredKeyWithDelay(args.key, args.delay)
		}, cleanupArgs{key: key, delay: expiration})
	}

	cmd := &redis.BoolCmd{}
	cmd.SetVal(true)
	return cmd
}

// cleanupExpiredKey removes a key if it has expired
func (fc *FastCache) cleanupExpiredKey(key string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	// Check if the key has actually expired
	if exp, ok := fc.ttls.Load(key); ok {
		if time.Now().After(exp.(time.Time)) {
			fc.cache.Del([]byte(key))
			fc.ttls.Delete(key)
			fc.hashData.Delete(key)
		}
	}
}

// Exists checks if a key exists in the cache
func (fc *FastCache) Exists(ctx context.Context, keys ...string) *redis.IntCmd {
	fc.mu.RLock()
	defer fc.mu.RUnlock()

	count := int64(0)
	for _, key := range keys {
		// Check expiration first
		if exp, ok := fc.ttls.Load(key); ok {
			if time.Now().After(exp.(time.Time)) {
				continue
			}
		}

		// Check if key exists in cache or hashData
		if fc.cache.Has([]byte(key)) {
			count++
		} else if _, ok := fc.hashData.Load(key); ok {
			count++
		}
	}

	cmd := &redis.IntCmd{}
	cmd.SetVal(count)
	return cmd
}

// SetNX sets the value for the given key only if it does not exist
func (fc *FastCache) SetNX(ctx context.Context, key string, value any, expiration time.Duration) *redis.BoolCmd {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	cmd := &redis.BoolCmd{}

	// Check if key already exists and not expired
	exists := false
	if fc.cache.Has([]byte(key)) {
		if exp, ok := fc.ttls.Load(key); ok {
			if time.Now().Before(exp.(time.Time)) {
				exists = true
			}
		} else {
			// Key exists without TTL
			exists = true
		}
	}

	if exists {
		cmd.SetVal(false)
		return cmd
	}

	// Convert value to bytes
	var valueBytes []byte
	switch v := value.(type) {
	case string:
		valueBytes = []byte(v)
	case []byte:
		valueBytes = v
	default:
		data, err := sonic.Marshal(v)
		if err != nil {
			cmd.SetErr(err)
			return cmd
		}
		valueBytes = data
	}

	fc.cache.Set([]byte(key), valueBytes)

	// Store expiration time if provided
	if expiration > 0 {
		fc.ttls.Store(key, time.Now().Add(expiration))
		safe.GoWith(func(args cleanupArgs) {
			fc.cleanupExpiredKeyWithDelay(args.key, args.delay)
		}, cleanupArgs{key: key, delay: expiration})
	}

	cmd.SetVal(true)
	return cmd
}

// TTL returns the remaining time to live of a key
func (fc *FastCache) TTL(ctx context.Context, key string) *redis.DurationCmd {
	fc.mu.RLock()
	defer fc.mu.RUnlock()

	cmd := &redis.DurationCmd{}

	// Check if key exists
	if !fc.cache.Has([]byte(key)) {
		// Check hashData as well
		if _, ok := fc.hashData.Load(key); !ok {
			cmd.SetVal(-2 * time.Second) // Key does not exist
			return cmd
		}
	}

	// Check expiration
	if exp, ok := fc.ttls.Load(key); ok {
		expTime := exp.(time.Time)
		remaining := time.Until(expTime)
		if remaining < 0 {
			cmd.SetVal(-2 * time.Second) // Key has expired
		} else {
			cmd.SetVal(remaining)
		}
	} else {
		cmd.SetVal(-1 * time.Second) // Key exists but no TTL set
	}

	return cmd
}

// Clear removes all items from the cache
func (fc *FastCache) Clear() {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	fc.cache.Reset()
	fc.ttls.Range(func(key, value interface{}) bool {
		fc.ttls.Delete(key)
		return true
	})
	fc.hashData.Range(func(key, value interface{}) bool {
		fc.hashData.Delete(key)
		return true
	})
}

// Stats returns cache statistics
func (fc *FastCache) Stats() fastcache.Stats {
	var stats fastcache.Stats
	fc.cache.UpdateStats(&stats)
	return stats
}

// toString converts a value to string
func toString(v interface{}) (string, error) {
	switch val := v.(type) {
	case string:
		return val, nil
	case []byte:
		return string(val), nil
	default:
		return "", redis.Nil
	}
}
