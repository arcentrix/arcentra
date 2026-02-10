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
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// mockCache is a simple mock implementation of ICache for testing
type mockCache struct {
	data map[string]string
}

func newMockCache() *mockCache {
	return &mockCache{
		data: make(map[string]string),
	}
}

func (m *mockCache) Get(ctx context.Context, key string) *redis.StringCmd {
	val, ok := m.data[key]
	if !ok {
		cmd := redis.NewStringCmd(ctx, "get", key)
		cmd.SetErr(redis.Nil)
		return cmd
	}
	cmd := redis.NewStringCmd(ctx, "get", key)
	cmd.SetVal(val)
	return cmd
}

func (m *mockCache) Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd {
	m.data[key] = value.(string)
	cmd := redis.NewStatusCmd(ctx, "set", key, value)
	cmd.SetVal("OK")
	return cmd
}

func (m *mockCache) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	count := int64(0)
	for _, key := range keys {
		if _, ok := m.data[key]; ok {
			delete(m.data, key)
			count++
		}
	}
	cmd := redis.NewIntCmd(ctx, "del", keys)
	cmd.SetVal(count)
	return cmd
}

func (m *mockCache) Pipeline() redis.Pipeliner {
	return nil
}

func (m *mockCache) HSet(ctx context.Context, key string, values ...any) *redis.IntCmd {
	// Simple mock implementation - just return success
	cmd := redis.NewIntCmd(ctx, "hset", key, values)
	cmd.SetVal(int64(len(values) / 2)) // Return number of fields set
	return cmd
}

func (m *mockCache) HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd {
	// Return empty map for mock - tests can override if needed
	cmd := redis.NewMapStringStringCmd(ctx, "hgetall", key)
	cmd.SetVal(make(map[string]string))
	return cmd
}

func (m *mockCache) HDel(ctx context.Context, key string, fields ...string) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx, "hdel", key, fields)
	cmd.SetVal(int64(len(fields)))
	return cmd
}

func (m *mockCache) Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd {
	cmd := redis.NewBoolCmd(ctx, "expire", key, expiration)
	cmd.SetVal(true)
	return cmd
}

type TestData struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func TestCachedQuery_Get_CacheHit(t *testing.T) {
	mockCache := newMockCache()
	ctx := context.Background()

	// Pre-populate cache
	cachedData := `{"id":1,"name":"test"}`
	mockCache.Set(ctx, "test:1", cachedData, time.Hour)

	keyFunc := func(params ...any) string {
		return "test:" + params[0].(string)
	}

	queryFunc := func(ctx context.Context) (TestData, error) {
		t.Error("queryFunc should not be called on cache hit")
		return TestData{}, nil
	}

	cq := NewCachedQuery(mockCache, keyFunc, queryFunc, WithLogPrefix[TestData]("[Test]"))

	result, err := cq.Get(ctx, "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ID != 1 || result.Name != "test" {
		t.Errorf("expected {ID:1, Name:\"test\"}, got %+v", result)
	}
}

func TestCachedQuery_Get_CacheMiss(t *testing.T) {
	mockCache := newMockCache()
	ctx := context.Background()

	keyFunc := func(params ...any) string {
		return "test:" + params[0].(string)
	}

	queryCalled := false
	queryFunc := func(ctx context.Context) (TestData, error) {
		queryCalled = true
		return TestData{ID: 2, Name: "from_db"}, nil
	}

	cq := NewCachedQuery(mockCache, keyFunc, queryFunc, WithLogPrefix[TestData]("[Test]"))

	result, err := cq.Get(ctx, "2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !queryCalled {
		t.Error("queryFunc should be called on cache miss")
	}

	if result.ID != 2 || result.Name != "from_db" {
		t.Errorf("expected {ID:2, Name:\"from_db\"}, got %+v", result)
	}

	// Verify data is cached
	cached, err := mockCache.Get(ctx, "test:2").Result()
	if err != nil {
		t.Fatalf("data should be cached: %v", err)
	}
	if cached == "" {
		t.Error("cached data should not be empty")
	}
}

func TestCachedQuery_Get_QueryError(t *testing.T) {
	mockCache := newMockCache()
	ctx := context.Background()

	keyFunc := func(params ...any) string {
		return "test:" + params[0].(string)
	}

	queryFunc := func(ctx context.Context) (TestData, error) {
		return TestData{}, errors.New("database error")
	}

	cq := NewCachedQuery(mockCache, keyFunc, queryFunc, WithLogPrefix[TestData]("[Test]"))

	_, err := cq.Get(ctx, "3")
	if err == nil {
		t.Error("expected error from queryFunc")
	}
}

func TestCachedQuery_Invalidate(t *testing.T) {
	mockCache := newMockCache()
	ctx := context.Background()

	// Pre-populate cache
	mockCache.Set(ctx, "test:1", `{"id":1,"name":"test"}`, time.Hour)

	keyFunc := func(params ...any) string {
		return "test:" + params[0].(string)
	}

	queryFunc := func(ctx context.Context) (TestData, error) {
		return TestData{}, nil
	}

	cq := NewCachedQuery(mockCache, keyFunc, queryFunc, WithLogPrefix[TestData]("[Test]"))

	err := cq.Invalidate(ctx, "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify cache is deleted
	_, err = mockCache.Get(ctx, "test:1").Result()
	if !errors.Is(err, redis.Nil) {
		t.Error("cache should be deleted")
	}
}

func TestCachedQuery_WithTTL(t *testing.T) {
	mockCache := newMockCache()
	ctx := context.Background()

	keyFunc := func(params ...any) string {
		return "test:" + params[0].(string)
	}

	queryFunc := func(ctx context.Context) (TestData, error) {
		return TestData{ID: 1, Name: "test"}, nil
	}

	customTTL := 30 * time.Minute
	cq := NewCachedQuery(mockCache, keyFunc, queryFunc, WithTTL[TestData](customTTL))

	_, err := cq.Get(ctx, "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cq.ttl != customTTL {
		t.Errorf("expected TTL %v, got %v", customTTL, cq.ttl)
	}
}

func TestCachedQuery_GetOrSet(t *testing.T) {
	mockCache := newMockCache()
	ctx := context.Background()

	keyFunc := func(params ...any) string {
		return "test:" + params[0].(string)
	}

	setFunc := func(ctx context.Context) (TestData, error) {
		return TestData{ID: 1, Name: "from_set"}, nil
	}

	cq := NewCachedQuery(mockCache, keyFunc, nil, WithLogPrefix[TestData]("[Test]"))

	result, err := cq.GetOrSet(ctx, setFunc, "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ID != 1 || result.Name != "from_set" {
		t.Errorf("expected {ID:1, Name:\"from_set\"}, got %+v", result)
	}

	// Verify data is cached
	cached, err := mockCache.Get(ctx, "test:1").Result()
	if err != nil {
		t.Fatalf("data should be cached: %v", err)
	}
	if cached == "" {
		t.Error("cached data should not be empty")
	}
}
