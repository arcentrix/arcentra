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

package cache

import (
	"context"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/v9"
)

// HybridCacheConfig holds hybrid cache configuration
type HybridCacheConfig struct {
	LocalEnabled  bool          // Enable local cache
	RemoteEnabled bool          // Enable remote cache (Redis)
	LocalMaxBytes int           // Max bytes for local cache
	LocalTTLRatio float64       // Ratio of remote TTL for local cache (0.0-1.0)
	SyncToRemote  bool          // Sync local cache to remote
	SyncInterval  time.Duration // Interval to sync local cache to remote
}

// HybridCache combines local cache (fastcache) and remote cache (Redis)
// It provides:
// 1. Fast local access via fastcache
// 2. Distributed caching via Redis
// 3. Automatic cache synchronization
type HybridCache struct {
	local          *FastCache
	remote         ICache
	config         HybridCacheConfig
	mu             sync.RWMutex
	syncTicker     *time.Ticker
	stopSyncChan   chan struct{}
	pendingUpdates sync.Map // map[string]hybridCacheEntry
}

// hybridCacheEntry stores cached data with metadata
type hybridCacheEntry struct {
	Value      string
	Expiration time.Time
}

// NewHybridCache creates a new HybridCache instance
func NewHybridCache(localCache *FastCache, remoteCache ICache, config HybridCacheConfig) *HybridCache {
	hc := &HybridCache{
		local:        localCache,
		remote:       remoteCache,
		config:       config,
		stopSyncChan: make(chan struct{}),
	}

	// Start background sync goroutine if enabled
	if config.SyncToRemote && config.SyncInterval > 0 {
		hc.startSync()
	}

	return hc
}

// Get retrieves a value from hybrid cache
// Strategy: Try local first, then remote, finally query function
func (hc *HybridCache) Get(ctx context.Context, key string) *redis.StringCmd {
	// Try local cache first
	if hc.config.LocalEnabled && hc.local != nil {
		cmd := hc.local.Get(ctx, key)
		if cmd != nil && cmd.Val() != "" {
			log.Debugw("hybrid cache hit (local)", "key", key)
			return cmd
		}
	}

	// Try remote cache
	if hc.config.RemoteEnabled && hc.remote != nil {
		cmd := hc.remote.Get(ctx, key)
		if cmd != nil && cmd.Val() != "" {
			log.Debugw("hybrid cache hit (remote)", "key", key)
			// Update local cache asynchronously using safe.Go
			if hc.config.LocalEnabled && hc.local != nil {
				localCache := hc.local
				value := cmd.Val()
				localTTL := hc.getLocalTTL(1 * time.Hour)
				safe.GoWith(func(args localSetArgs) {
					args.cache.Set(context.Background(), args.key, args.value, args.ttl)
				}, localSetArgs{cache: localCache, key: key, value: value, ttl: localTTL})
			}
			return cmd
		}
	}

	log.Debugw("hybrid cache miss", "key", key)
	return &redis.StringCmd{}
}

// localSetArgs holds arguments for local cache set goroutine
type localSetArgs struct {
	cache *FastCache
	key   string
	value string
	ttl   time.Duration
}

// getLocalTTL calculates local TTL based on remote TTL and ratio
func (hc *HybridCache) getLocalTTL(remoteTTL time.Duration) time.Duration {
	if hc.config.LocalTTLRatio > 0 && hc.config.LocalTTLRatio < 1.0 {
		return time.Duration(float64(remoteTTL) * hc.config.LocalTTLRatio)
	}
	return remoteTTL
}

// Set sets a value in hybrid cache
// Strategy: Update both local and remote caches
func (hc *HybridCache) Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd {
	var valueStr string

	// Convert value to string
	switch v := value.(type) {
	case string:
		valueStr = v
	case []byte:
		valueStr = string(v)
	default:
		var err error
		valueStr, err = sonic.MarshalString(v)
		if err != nil {
			log.Warnw("failed to marshal value for caching", "key", key, "error", err)
			valueStr = ""
		}
	}

	// Set in local cache with adjusted TTL
	if hc.config.LocalEnabled && hc.local != nil {
		localTTL := expiration
		if hc.config.LocalTTLRatio > 0 && hc.config.LocalTTLRatio < 1.0 {
			localTTL = time.Duration(float64(expiration) * hc.config.LocalTTLRatio)
		}
		hc.local.Set(ctx, key, valueStr, localTTL)
	}

	// Set in remote cache
	if hc.config.RemoteEnabled && hc.remote != nil {
		hc.remote.Set(ctx, key, valueStr, expiration)
	}

	// Track pending updates for sync
	if hc.config.SyncToRemote {
		hc.pendingUpdates.Store(key, hybridCacheEntry{
			Value:      valueStr,
			Expiration: time.Now().Add(expiration),
		})
	}

	cmd := &redis.StatusCmd{}
	cmd.SetVal("OK")
	return cmd
}

// Del deletes a key from hybrid cache
func (hc *HybridCache) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	count := int64(0)

	// Delete from local cache
	if hc.config.LocalEnabled && hc.local != nil {
		cmd := hc.local.Del(ctx, keys...)
		if cmd != nil {
			count += cmd.Val()
		}
	}

	// Delete from remote cache
	if hc.config.RemoteEnabled && hc.remote != nil {
		cmd := hc.remote.Del(ctx, keys...)
		if cmd != nil {
			count += cmd.Val()
		}
	}

	// Remove from pending updates
	for _, key := range keys {
		hc.pendingUpdates.Delete(key)
	}

	cmd := &redis.IntCmd{}
	cmd.SetVal(count)
	return cmd
}

// Pipeline returns a pipeline for batch operations
// Returns remote cache pipeline if available, otherwise nil
func (hc *HybridCache) Pipeline() redis.Pipeliner {
	if hc.config.RemoteEnabled && hc.remote != nil {
		return hc.remote.Pipeline()
	}
	// FastCache does not support pipelines
	return nil
}

// HSet sets hash fields in hybrid cache
func (hc *HybridCache) HSet(ctx context.Context, key string, values ...any) *redis.IntCmd {
	var count int64

	// Set in local cache
	if hc.config.LocalEnabled && hc.local != nil {
		cmd := hc.local.HSet(ctx, key, values...)
		if cmd != nil {
			count += cmd.Val()
		}
	}

	// Set in remote cache
	if hc.config.RemoteEnabled && hc.remote != nil {
		cmd := hc.remote.HSet(ctx, key, values...)
		if cmd != nil {
			count += cmd.Val()
		}
	}

	cmd := &redis.IntCmd{}
	cmd.SetVal(count)
	return cmd
}

// HGetAll returns all hash fields and values from hybrid cache
func (hc *HybridCache) HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd {
	// Try local cache first
	if hc.config.LocalEnabled && hc.local != nil {
		cmd := hc.local.HGetAll(ctx, key)
		if cmd != nil && len(cmd.Val()) > 0 {
			return cmd
		}
	}

	// Try remote cache
	if hc.config.RemoteEnabled && hc.remote != nil {
		cmd := hc.remote.HGetAll(ctx, key)
		if cmd != nil && len(cmd.Val()) > 0 {
			// Update local cache asynchronously
			if hc.config.LocalEnabled && hc.local != nil {
				localCache := hc.local
				result := cmd.Val()
				safe.GoWith(func(args hashSetArgs) {
					// Convert map to flat slice
					values := make([]any, 0, len(args.data)*2)
					for k, v := range args.data {
						values = append(values, k, v)
					}
					args.cache.HSet(context.Background(), args.key, values...)
				}, hashSetArgs{cache: localCache, key: key, data: result})
			}
			return cmd
		}
	}

	cmd := &redis.MapStringStringCmd{}
	cmd.SetVal(make(map[string]string))
	return cmd
}

// hashSetArgs holds arguments for hash set goroutine
type hashSetArgs struct {
	cache *FastCache
	key   string
	data  map[string]string
}

// HGet returns the value of a hash field from hybrid cache
func (hc *HybridCache) HGet(ctx context.Context, key, field string) *redis.StringCmd {
	// Try local cache first
	if hc.config.LocalEnabled && hc.local != nil {
		cmd := hc.local.HGet(ctx, key, field)
		if cmd != nil && cmd.Err() == nil && cmd.Val() != "" {
			return cmd
		}
	}

	// Try remote cache
	if hc.config.RemoteEnabled && hc.remote != nil {
		// Note: ICache interface doesn't have HGet, so we get all and extract
		allCmd := hc.remote.HGetAll(ctx, key)
		if allCmd != nil && allCmd.Err() == nil {
			if val, ok := allCmd.Val()[field]; ok {
				cmd := &redis.StringCmd{}
				cmd.SetVal(val)
				return cmd
			}
		}
	}

	cmd := &redis.StringCmd{}
	cmd.SetErr(redis.Nil)
	return cmd
}

// HDel deletes hash fields from hybrid cache
func (hc *HybridCache) HDel(ctx context.Context, key string, fields ...string) *redis.IntCmd {
	var count int64

	// Delete from local cache
	if hc.config.LocalEnabled && hc.local != nil {
		cmd := hc.local.HDel(ctx, key, fields...)
		if cmd != nil {
			count += cmd.Val()
		}
	}

	// Delete from remote cache
	if hc.config.RemoteEnabled && hc.remote != nil {
		cmd := hc.remote.HDel(ctx, key, fields...)
		if cmd != nil {
			count += cmd.Val()
		}
	}

	cmd := &redis.IntCmd{}
	cmd.SetVal(count)
	return cmd
}

// Expire sets the expiration time for a key in hybrid cache
func (hc *HybridCache) Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd {
	success := false

	// Set expiration in local cache
	if hc.config.LocalEnabled && hc.local != nil {
		localTTL := hc.getLocalTTL(expiration)
		cmd := hc.local.Expire(ctx, key, localTTL)
		if cmd != nil && cmd.Val() {
			success = true
		}
	}

	// Set expiration in remote cache
	if hc.config.RemoteEnabled && hc.remote != nil {
		cmd := hc.remote.Expire(ctx, key, expiration)
		if cmd != nil && cmd.Val() {
			success = true
		}
	}

	cmd := &redis.BoolCmd{}
	cmd.SetVal(success)
	return cmd
}

// startSync starts the background sync goroutine
func (hc *HybridCache) startSync() {
	hc.syncTicker = time.NewTicker(hc.config.SyncInterval)

	safe.Go(func() {
		for {
			select {
			case <-hc.stopSyncChan:
				hc.syncTicker.Stop()
				return
			case <-hc.syncTicker.C:
				hc.syncPendingUpdates()
			}
		}
	})
}

// syncPendingUpdates syncs pending updates to remote cache
func (hc *HybridCache) syncPendingUpdates() {
	if hc.remote == nil {
		return
	}

	ctx := context.Background()
	now := time.Now()

	hc.pendingUpdates.Range(func(key, value interface{}) bool {
		entry := value.(hybridCacheEntry)

		// Skip if already expired
		if now.After(entry.Expiration) {
			hc.pendingUpdates.Delete(key)
			return true
		}

		// Calculate remaining TTL
		remaining := entry.Expiration.Sub(now)
		if remaining > 0 {
			hc.remote.Set(ctx, key.(string), entry.Value, remaining)
		}

		hc.pendingUpdates.Delete(key)
		return true
	})
}

// Stop stops the hybrid cache and its background sync
func (hc *HybridCache) Stop() {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	if hc.stopSyncChan != nil {
		close(hc.stopSyncChan)
		hc.stopSyncChan = nil
	}

	// Sync remaining pending updates before stopping
	hc.syncPendingUpdates()
}

// Clear clears both local and remote caches
func (hc *HybridCache) Clear(ctx context.Context) {
	if hc.config.LocalEnabled && hc.local != nil {
		hc.local.Clear()
	}

	// Clear pending updates
	hc.pendingUpdates.Range(func(key, value interface{}) bool {
		hc.pendingUpdates.Delete(key)
		return true
	})
}

// Stats returns local cache statistics
func (hc *HybridCache) Stats() interface{} {
	if hc.local != nil {
		return hc.local.Stats()
	}
	return nil
}
