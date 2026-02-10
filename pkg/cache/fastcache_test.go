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
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestFastCache_Set_Get(t *testing.T) {
	cache := NewFastCache(FastCacheConfig{MaxBytes: 1024 * 1024})
	defer cache.Clear()

	ctx := context.Background()
	key := "test_key"
	value := "test_value"

	// Set a value
	cmd := cache.Set(ctx, key, value, 1*time.Hour)
	if cmd.Val() != "OK" {
		t.Errorf("expected OK, got %s", cmd.Val())
	}

	// Get the value
	getCmd := cache.Get(ctx, key)
	if getCmd.Val() != value {
		t.Errorf("expected %s, got %s", value, getCmd.Val())
	}
}

func TestFastCache_Expiration(t *testing.T) {
	cache := NewFastCache(FastCacheConfig{MaxBytes: 1024 * 1024})
	defer cache.Clear()

	ctx := context.Background()
	key := "expire_key"
	value := "expire_value"

	// Set a value with short TTL
	cache.Set(ctx, key, value, 100*time.Millisecond)

	// Verify it exists
	getCmd := cache.Get(ctx, key)
	if getCmd.Val() != value {
		t.Errorf("expected %s, got %s", value, getCmd.Val())
	}

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Verify it's expired
	getCmd = cache.Get(ctx, key)
	if getCmd.Val() != "" {
		t.Errorf("expected empty string for expired key, got %s", getCmd.Val())
	}
}

func TestFastCache_Del(t *testing.T) {
	cache := NewFastCache(FastCacheConfig{MaxBytes: 1024 * 1024})
	defer cache.Clear()

	ctx := context.Background()
	key1 := "del_key1"
	key2 := "del_key2"

	// Set values
	cache.Set(ctx, key1, "value1", 1*time.Hour)
	cache.Set(ctx, key2, "value2", 1*time.Hour)

	// Delete keys
	delCmd := cache.Del(ctx, key1, key2)
	if delCmd.Val() != 2 {
		t.Errorf("expected 2 deleted, got %d", delCmd.Val())
	}

	// Verify they're deleted
	if cache.Get(ctx, key1).Val() != "" {
		t.Error("key1 should be deleted")
	}
	if cache.Get(ctx, key2).Val() != "" {
		t.Error("key2 should be deleted")
	}
}

func TestFastCache_Stats(t *testing.T) {
	cache := NewFastCache(FastCacheConfig{MaxBytes: 1024 * 1024})
	defer cache.Clear()

	ctx := context.Background()

	// Set some values
	cache.Set(ctx, "key1", "value1", 1*time.Hour)
	cache.Set(ctx, "key2", "value2", 1*time.Hour)

	// Get stats
	stats := cache.Stats()
	if stats.EntriesCount == 0 {
		t.Error("expected non-zero entries count")
	}
}

func TestFastCache_BytesValue(t *testing.T) {
	cache := NewFastCache(FastCacheConfig{MaxBytes: 1024 * 1024})
	defer cache.Clear()

	ctx := context.Background()
	key := "bytes_key"
	value := []byte("bytes_value")

	// Set bytes value
	cache.Set(ctx, key, value, 1*time.Hour)

	// Get the value
	getCmd := cache.Get(ctx, key)
	if getCmd.Val() != string(value) {
		t.Errorf("expected %s, got %s", string(value), getCmd.Val())
	}
}

func TestFastCache_HSet_HGetAll(t *testing.T) {
	cache := NewFastCache(FastCacheConfig{MaxBytes: 1024 * 1024})
	defer cache.Clear()

	ctx := context.Background()
	key := "hash_key"

	// Set hash fields
	cmd := cache.HSet(ctx, key, "field1", "value1", "field2", "value2")
	if cmd.Val() != 2 {
		t.Errorf("expected 2 fields set, got %d", cmd.Val())
	}

	// Get all fields
	getAllCmd := cache.HGetAll(ctx, key)
	if getAllCmd == nil {
		t.Error("expected non-nil HGetAll result")
	}
}

func TestFastCache_Expire(t *testing.T) {
	cache := NewFastCache(FastCacheConfig{MaxBytes: 1024 * 1024})
	defer cache.Clear()

	ctx := context.Background()
	key := "expire_test_key"

	// Set a value without expiration
	cache.Set(ctx, key, "value", 0)

	// Set expiration
	expireCmd := cache.Expire(ctx, key, 100*time.Millisecond)
	if !expireCmd.Val() {
		t.Error("expected Expire to return true")
	}

	// Verify it exists
	if cache.Get(ctx, key).Val() != "value" {
		t.Error("key should exist")
	}

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Verify it's expired
	if cache.Get(ctx, key).Val() != "" {
		t.Error("key should be expired")
	}
}

func TestFastCache_Clear(t *testing.T) {
	cache := NewFastCache(FastCacheConfig{MaxBytes: 1024 * 1024})

	ctx := context.Background()

	// Set some values
	cache.Set(ctx, "key1", "value1", 1*time.Hour)
	cache.Set(ctx, "key2", "value2", 1*time.Hour)
	cache.Set(ctx, "key3", "value3", 1*time.Hour)

	// Clear cache
	cache.Clear()

	// Verify all keys are deleted
	if cache.Get(ctx, "key1").Val() != "" {
		t.Error("cache should be cleared")
	}
	if cache.Get(ctx, "key2").Val() != "" {
		t.Error("cache should be cleared")
	}
	if cache.Get(ctx, "key3").Val() != "" {
		t.Error("cache should be cleared")
	}
}

// MockICache implements ICache for testing HybridCache without Redis
type MockICache struct {
	data map[string]string
	hmap map[string]map[string]string
}

func NewMockICache() *MockICache {
	return &MockICache{
		data: make(map[string]string),
		hmap: make(map[string]map[string]string),
	}
}

func (m *MockICache) Get(ctx context.Context, key string) *redis.StringCmd {
	cmd := &redis.StringCmd{}
	if val, ok := m.data[key]; ok {
		cmd.SetVal(val)
	}
	return cmd
}

func (m *MockICache) Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd {
	switch v := value.(type) {
	case string:
		m.data[key] = v
	case []byte:
		m.data[key] = string(v)
	}
	cmd := &redis.StatusCmd{}
	cmd.SetVal("OK")
	return cmd
}

func (m *MockICache) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	count := 0
	for _, key := range keys {
		if _, ok := m.data[key]; ok {
			delete(m.data, key)
			count++
		}
	}
	cmd := &redis.IntCmd{}
	cmd.SetVal(int64(count))
	return cmd
}

func (m *MockICache) Pipeline() redis.Pipeliner {
	return nil
}

func (m *MockICache) HSet(ctx context.Context, key string, values ...any) *redis.IntCmd {
	if len(values)%2 != 0 {
		return &redis.IntCmd{}
	}
	if _, ok := m.hmap[key]; !ok {
		m.hmap[key] = make(map[string]string)
	}
	count := 0
	for i := 0; i < len(values); i += 2 {
		field := values[i].(string)
		value := values[i+1].(string)
		m.hmap[key][field] = value
		count++
	}
	cmd := &redis.IntCmd{}
	cmd.SetVal(int64(count))
	return cmd
}

func (m *MockICache) HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd {
	cmd := &redis.MapStringStringCmd{}
	if hash, ok := m.hmap[key]; ok {
		cmd.SetVal(hash)
	} else {
		cmd.SetVal(make(map[string]string))
	}
	return cmd
}

func (m *MockICache) HDel(ctx context.Context, key string, fields ...string) *redis.IntCmd {
	cmd := &redis.IntCmd{}
	if hash, ok := m.hmap[key]; ok {
		count := 0
		for _, field := range fields {
			if _, ok := hash[field]; ok {
				delete(hash, field)
				count++
			}
		}
		cmd.SetVal(int64(count))
	}
	return cmd
}

func (m *MockICache) Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd {
	cmd := &redis.BoolCmd{}
	cmd.SetVal(true)
	return cmd
}

func TestHybridCache_LocalOnly(t *testing.T) {
	localCache := NewFastCache(FastCacheConfig{MaxBytes: 1024 * 1024})
	mockRemote := NewMockICache()

	config := HybridCacheConfig{
		LocalEnabled:  true,
		RemoteEnabled: false,
	}

	hybridCache := NewHybridCache(localCache, mockRemote, config)
	defer hybridCache.Stop()

	ctx := context.Background()
	key := "local_only_key"
	value := "local_only_value"

	// Set value
	hybridCache.Set(ctx, key, value, 1*time.Hour)

	// Get value from local
	cmd := hybridCache.Get(ctx, key)
	if cmd.Val() != value {
		t.Errorf("expected %s, got %s", value, cmd.Val())
	}
}

func TestHybridCache_RemoteOnly(t *testing.T) {
	localCache := NewFastCache(FastCacheConfig{MaxBytes: 1024 * 1024})
	mockRemote := NewMockICache()

	config := HybridCacheConfig{
		LocalEnabled:  false,
		RemoteEnabled: true,
	}

	hybridCache := NewHybridCache(localCache, mockRemote, config)
	defer hybridCache.Stop()

	ctx := context.Background()
	key := "remote_only_key"
	value := "remote_only_value"

	// Set value
	hybridCache.Set(ctx, key, value, 1*time.Hour)

	// Get value from remote
	cmd := hybridCache.Get(ctx, key)
	if cmd.Val() != value {
		t.Errorf("expected %s, got %s", value, cmd.Val())
	}
}

func TestHybridCache_LocalAndRemote(t *testing.T) {
	localCache := NewFastCache(FastCacheConfig{MaxBytes: 1024 * 1024})
	mockRemote := NewMockICache()

	config := HybridCacheConfig{
		LocalEnabled:  true,
		RemoteEnabled: true,
		LocalTTLRatio: 0.5,
	}

	hybridCache := NewHybridCache(localCache, mockRemote, config)
	defer hybridCache.Stop()

	ctx := context.Background()
	key := "hybrid_key"
	value := "hybrid_value"

	// Set value
	hybridCache.Set(ctx, key, value, 1*time.Hour)

	// Get value
	cmd := hybridCache.Get(ctx, key)
	if cmd.Val() != value {
		t.Errorf("expected %s, got %s", value, cmd.Val())
	}

	// Verify it's in both caches
	if mockRemote.data[key] != value {
		t.Error("value should be in remote cache")
	}
	if localCache.Get(ctx, key).Val() != value {
		t.Error("value should be in local cache")
	}
}

func TestHybridCache_Del(t *testing.T) {
	localCache := NewFastCache(FastCacheConfig{MaxBytes: 1024 * 1024})
	mockRemote := NewMockICache()

	config := HybridCacheConfig{
		LocalEnabled:  true,
		RemoteEnabled: true,
	}

	hybridCache := NewHybridCache(localCache, mockRemote, config)
	defer hybridCache.Stop()

	ctx := context.Background()
	key := "del_hybrid_key"

	// Set value
	hybridCache.Set(ctx, key, "value", 1*time.Hour)

	// Delete key
	delCmd := hybridCache.Del(ctx, key)
	if delCmd.Val() != 2 { // deleted from both local and remote
		t.Errorf("expected 2 deletions, got %d", delCmd.Val())
	}

	// Verify it's deleted from both
	if localCache.Get(ctx, key).Val() != "" {
		t.Error("key should be deleted from local cache")
	}
	if mockRemote.data[key] != "" {
		t.Error("key should be deleted from remote cache")
	}
}

func TestCachedQueryWithHybrid(t *testing.T) {
	localCache := NewFastCache(FastCacheConfig{MaxBytes: 1024 * 1024})
	mockRemote := NewMockICache()

	config := HybridCacheConfig{
		LocalEnabled:  true,
		RemoteEnabled: true,
	}

	hybridCache := NewHybridCache(localCache, mockRemote, config)
	defer hybridCache.Stop()

	// Create a test data structure
	type User struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}

	queryCount := 0
	queryFunc := func(ctx context.Context) (User, error) {
		queryCount++
		return User{ID: 1, Name: "Alice"}, nil
	}

	keyFunc := func(params ...any) string {
		return "user:1"
	}

	cq := NewCachedQuery(hybridCache, keyFunc, queryFunc, WithTTL[User](1*time.Hour))

	ctx := context.Background()

	// First call should query from database
	user1, _ := cq.Get(ctx)
	if user1.ID != 1 || user1.Name != "Alice" {
		t.Error("expected user with ID 1 and name Alice")
	}
	if queryCount != 1 {
		t.Error("expected query to be called once")
	}

	// Second call should hit cache
	user2, _ := cq.Get(ctx)
	if user2.ID != 1 || user2.Name != "Alice" {
		t.Error("expected cached user with ID 1 and name Alice")
	}
	if queryCount != 1 {
		t.Error("expected query to not be called again (cache hit)")
	}
}

func TestCachedQueryWithHybrid_Invalidate(t *testing.T) {
	localCache := NewFastCache(FastCacheConfig{MaxBytes: 1024 * 1024})
	mockRemote := NewMockICache()

	config := HybridCacheConfig{
		LocalEnabled:  true,
		RemoteEnabled: true,
	}

	hybridCache := NewHybridCache(localCache, mockRemote, config)
	defer hybridCache.Stop()

	type Product struct {
		ID    int     `json:"id"`
		Price float64 `json:"price"`
	}

	queryCount := 0
	queryFunc := func(ctx context.Context) (Product, error) {
		queryCount++
		if queryCount == 1 {
			return Product{ID: 1, Price: 100.0}, nil
		}
		return Product{ID: 1, Price: 200.0}, nil
	}

	keyFunc := func(params ...any) string {
		return "product:1"
	}

	cq := NewCachedQuery(hybridCache, keyFunc, queryFunc, WithTTL[Product](1*time.Hour))

	ctx := context.Background()

	// First call
	prod1, _ := cq.Get(ctx)
	if prod1.Price != 100.0 {
		t.Error("expected price 100.0")
	}

	// Invalidate cache
	cq.Invalidate(ctx)

	// Second call after invalidation
	prod2, _ := cq.Get(ctx)
	if prod2.Price != 200.0 {
		t.Error("expected price 200.0 after cache invalidation")
	}
	if queryCount != 2 {
		t.Error("expected query to be called twice")
	}
}
