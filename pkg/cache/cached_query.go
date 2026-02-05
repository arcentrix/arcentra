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
	"errors"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/v9"
)

var (
	// ErrCacheMiss indicates that the key was not found in cache
	ErrCacheMiss = redis.Nil
)

// QueryFunc defines a function that queries data from database
// T is the type of data to be queried
type QueryFunc[T any] func(ctx context.Context) (T, error)

// KeyFunc defines a function that generates cache key from parameters
type KeyFunc func(params ...any) string

// CachedQuery provides a generic cache-aside pattern implementation
// It queries Redis first, and falls back to database if cache miss
type CachedQuery[T any] struct {
	cache     ICache
	keyFunc   KeyFunc
	queryFunc QueryFunc[T]
	ttl       time.Duration
	logPrefix string
}

// CachedQueryOption configures CachedQuery behavior
type CachedQueryOption[T any] func(*CachedQuery[T])

// WithTTL sets the cache expiration time
// Note: This function doesn't need type parameter but we keep it for consistency
// Use WithTTL[YourType] or let type inference work from NewCachedQuery context
func WithTTL[T any](ttl time.Duration) CachedQueryOption[T] {
	return func(cq *CachedQuery[T]) {
		cq.ttl = ttl
	}
}

// WithLogPrefix sets the log prefix for debugging
// Note: This function doesn't need type parameter but we keep it for consistency
// Use WithLogPrefix[YourType] or let type inference work from NewCachedQuery context
func WithLogPrefix[T any](prefix string) CachedQueryOption[T] {
	return func(cq *CachedQuery[T]) {
		cq.logPrefix = prefix
	}
}

// NewCachedQuery creates a new CachedQuery instance
// cache: Redis cache instance
// keyFunc: function to generate cache key from parameters
// queryFunc: function to query data from database
// opts: optional configurations
func NewCachedQuery[T any](
	cache ICache,
	keyFunc KeyFunc,
	queryFunc QueryFunc[T],
	opts ...CachedQueryOption[T],
) *CachedQuery[T] {
	cq := &CachedQuery[T]{
		cache:     cache,
		keyFunc:   keyFunc,
		queryFunc: queryFunc,
		ttl:       1 * time.Hour, // default TTL
		logPrefix: "[CachedQuery]",
	}

	for _, opt := range opts {
		opt(cq)
	}

	return cq
}

// Get queries data with cache-aside pattern
// It first checks Redis cache, if miss, queries from database and caches the result
// params: parameters used to generate cache key
func (cq *CachedQuery[T]) Get(ctx context.Context, params ...any) (T, error) {
	var zero T
	cacheKey := cq.keyFunc(params...)

	// Try to get from cache first
	if cq.cache != nil {
		cacheData, err := cq.cache.Get(ctx, cacheKey).Result()
		if err == nil && cacheData != "" {
			var result T
			if err := sonic.UnmarshalString(cacheData, &result); err == nil {
				log.Debugw(cq.logPrefix+" cache hit", "key", cacheKey)
				return result, nil
			}
			log.Warnw(cq.logPrefix+" failed to unmarshal cached data", "key", cacheKey, "error", err)
		} else if !errors.Is(err, ErrCacheMiss) {
			log.Warnw(cq.logPrefix+" cache get error", "key", cacheKey, "error", err)
		}
	}

	// Cache miss, query from database
	log.Debugw(cq.logPrefix+" cache miss, querying from database", "key", cacheKey)
	result, err := cq.queryFunc(ctx)
	if err != nil {
		return zero, fmt.Errorf("failed to query from database: %w", err)
	}

	// Cache the result
	if cq.cache != nil {
		cacheData, err := sonic.MarshalString(result)
		if err == nil {
			err = cq.cache.Set(ctx, cacheKey, cacheData, cq.ttl).Err()
			if err != nil {
				log.Warnw(cq.logPrefix+" failed to cache result", "key", cacheKey, "error", err)
			} else {
				log.Debugw(cq.logPrefix+" cached result", "key", cacheKey)
			}
		} else {
			log.Warnw(cq.logPrefix+" failed to marshal result for caching", "key", cacheKey, "error", err)
		}
	}

	return result, nil
}

// Invalidate removes the cached data
func (cq *CachedQuery[T]) Invalidate(ctx context.Context, params ...any) error {
	if cq.cache == nil {
		return nil
	}
	cacheKey := cq.keyFunc(params...)
	err := cq.cache.Del(ctx, cacheKey).Err()
	if err != nil {
		log.Warnw(cq.logPrefix+" failed to invalidate cache", "key", cacheKey, "error", err)
		return err
	}
	log.Debugw(cq.logPrefix+" cache invalidated", "key", cacheKey)
	return nil
}

// GetOrSet queries data with cache-aside pattern, but allows setting a custom value if cache miss
// This is useful when you want to set a default value or handle cache miss differently
func (cq *CachedQuery[T]) GetOrSet(ctx context.Context, setFunc func(ctx context.Context) (T, error), params ...any) (T, error) {
	var zero T
	cacheKey := cq.keyFunc(params...)

	// Try to get from cache first
	if cq.cache != nil {
		cacheData, err := cq.cache.Get(ctx, cacheKey).Result()
		if err == nil && cacheData != "" {
			var result T
			if err := sonic.UnmarshalString(cacheData, &result); err == nil {
				log.Debugw(cq.logPrefix+" cache hit", "key", cacheKey)
				return result, nil
			}
			log.Warnw(cq.logPrefix+" failed to unmarshal cached data", "key", cacheKey, "error", err)
		} else if !errors.Is(err, ErrCacheMiss) {
			log.Warnw(cq.logPrefix+" cache get error", "key", cacheKey, "error", err)
		}
	}

	// Cache miss, use setFunc to get/set value
	result, err := setFunc(ctx)
	if err != nil {
		return zero, fmt.Errorf("failed to set value: %w", err)
	}

	// Cache the result
	if cq.cache != nil {
		cacheData, err := sonic.MarshalString(result)
		if err == nil {
			err = cq.cache.Set(ctx, cacheKey, cacheData, cq.ttl).Err()
			if err != nil {
				log.Warnw(cq.logPrefix+" failed to cache result", "key", cacheKey, "error", err)
			} else {
				log.Debugw(cq.logPrefix+" cached result", "key", cacheKey)
			}
		} else {
			log.Warnw(cq.logPrefix+" failed to marshal result for caching", "key", cacheKey, "error", err)
		}
	}

	return result, nil
}

// HashMarshalFunc defines a function that converts a value to hash fields
type HashMarshalFunc[T any] func(value T) map[string]interface{}

// HashUnmarshalFunc defines a function that converts hash fields to a value
type HashUnmarshalFunc[T any] func(hashData map[string]string) (T, error)

// CachedHashQuery provides a generic cache-aside pattern implementation using Redis Hash
// It queries Redis Hash first, and falls back to database if cache miss
type CachedHashQuery[T any] struct {
	cache         ICache
	keyFunc       KeyFunc
	queryFunc     QueryFunc[T]
	hashMarshal   HashMarshalFunc[T]
	hashUnmarshal HashUnmarshalFunc[T]
	ttl           time.Duration
	logPrefix     string
}

// CachedHashQueryOption configures CachedHashQuery behavior
type CachedHashQueryOption[T any] func(*CachedHashQuery[T])

// WithHashTTL sets the cache expiration time for hash
func WithHashTTL[T any](ttl time.Duration) CachedHashQueryOption[T] {
	return func(cq *CachedHashQuery[T]) {
		cq.ttl = ttl
	}
}

// WithHashLogPrefix sets the log prefix for debugging
func WithHashLogPrefix[T any](prefix string) CachedHashQueryOption[T] {
	return func(cq *CachedHashQuery[T]) {
		cq.logPrefix = prefix
	}
}

// NewCachedHashQuery creates a new CachedHashQuery instance
// cache: Redis cache instance
// keyFunc: function to generate cache key from parameters
// queryFunc: function to query data from database
// hashMarshal: function to convert value to hash fields
// hashUnmarshal: function to convert hash fields to value
// opts: optional configurations
func NewCachedHashQuery[T any](
	cache ICache,
	keyFunc KeyFunc,
	queryFunc QueryFunc[T],
	hashMarshal HashMarshalFunc[T],
	hashUnmarshal HashUnmarshalFunc[T],
	opts ...CachedHashQueryOption[T],
) *CachedHashQuery[T] {
	cq := &CachedHashQuery[T]{
		cache:         cache,
		keyFunc:       keyFunc,
		queryFunc:     queryFunc,
		hashMarshal:   hashMarshal,
		hashUnmarshal: hashUnmarshal,
		ttl:           1 * time.Hour, // default TTL
		logPrefix:     "[CachedHashQuery]",
	}

	for _, opt := range opts {
		opt(cq)
	}

	return cq
}

// Get queries data with cache-aside pattern using Redis Hash
// It first checks Redis Hash cache, if miss, queries from database and caches the result as Hash
// params: parameters used to generate cache key
func (cq *CachedHashQuery[T]) Get(ctx context.Context, params ...any) (T, error) {
	var zero T
	cacheKey := cq.keyFunc(params...)

	// Try to get from Hash cache first
	if cq.cache != nil {
		hashData, err := cq.cache.HGetAll(ctx, cacheKey).Result()
		if err == nil && len(hashData) > 0 {
			result, err := cq.hashUnmarshal(hashData)
			if err == nil {
				log.Debugw(cq.logPrefix+" cache hit (hash)", "key", cacheKey)
				return result, nil
			}
			log.Warnw(cq.logPrefix+" failed to unmarshal hash data", "key", cacheKey, "error", err)
		} else if !errors.Is(err, ErrCacheMiss) && err != nil {
			log.Warnw(cq.logPrefix+" cache get error", "key", cacheKey, "error", err)
		}
	}

	// Cache miss, query from database
	log.Debugw(cq.logPrefix+" cache miss, querying from database", "key", cacheKey)
	result, err := cq.queryFunc(ctx)
	if err != nil {
		return zero, fmt.Errorf("failed to query from database: %w", err)
	}

	// Cache the result as Hash
	if cq.cache != nil {
		hashFields := cq.hashMarshal(result)
		if len(hashFields) > 0 {
			err = cq.cache.HSet(ctx, cacheKey, hashFields).Err()
			if err != nil {
				log.Warnw(cq.logPrefix+" failed to cache result (hash)", "key", cacheKey, "error", err)
			} else {
				// Set expiration
				err = cq.cache.Expire(ctx, cacheKey, cq.ttl).Err()
				if err != nil {
					log.Warnw(cq.logPrefix+" failed to set expiration for hash", "key", cacheKey, "error", err)
				} else {
					log.Debugw(cq.logPrefix+" cached result (hash)", "key", cacheKey)
				}
			}
		}
	}

	return result, nil
}

// Invalidate removes the cached Hash data
func (cq *CachedHashQuery[T]) Invalidate(ctx context.Context, params ...any) error {
	if cq.cache == nil {
		return nil
	}
	cacheKey := cq.keyFunc(params...)
	err := cq.cache.Del(ctx, cacheKey).Err()
	if err != nil {
		log.Warnw(cq.logPrefix+" failed to invalidate hash cache", "key", cacheKey, "error", err)
		return err
	}
	log.Debugw(cq.logPrefix+" hash cache invalidated", "key", cacheKey)
	return nil
}
