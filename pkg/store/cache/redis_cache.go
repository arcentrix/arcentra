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
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCache Redis 缓存实现（支持单节点、Sentinel 和集群模式）
type RedisCache struct {
	cmdable redis.Cmdable
}

// NewRedisCache 创建 Redis 缓存实例（支持单节点、Sentinel 和集群模式）
func NewRedisCache(cmdable redis.Cmdable) ICache {
	return &RedisCache{cmdable: cmdable}
}

// GetCmdable 获取底层的 redis.Cmdable（支持所有模式：单节点、Sentinel 和集群）
func (r *RedisCache) GetCmdable() redis.Cmdable {
	return r.cmdable
}

// Get 获取缓存值
func (r *RedisCache) Get(ctx context.Context, key string) *redis.StringCmd {
	return r.cmdable.Get(ctx, key)
}

// Set 设置缓存值
func (r *RedisCache) Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd {
	return r.cmdable.Set(ctx, key, value, expiration)
}

// Del 删除缓存
func (r *RedisCache) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	return r.cmdable.Del(ctx, keys...)
}

// Pipeline 创建管道
func (r *RedisCache) Pipeline() redis.Pipeliner {
	return r.cmdable.Pipeline()
}

// HSet 设置 Hash 字段
func (r *RedisCache) HSet(ctx context.Context, key string, values ...any) *redis.IntCmd {
	return r.cmdable.HSet(ctx, key, values...)
}

// HGetAll 获取 Hash 所有字段
func (r *RedisCache) HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd {
	return r.cmdable.HGetAll(ctx, key)
}

// HDel 删除 Hash 字段
func (r *RedisCache) HDel(ctx context.Context, key string, fields ...string) *redis.IntCmd {
	return r.cmdable.HDel(ctx, key, fields...)
}

// Expire 设置过期时间
func (r *RedisCache) Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd {
	return r.cmdable.Expire(ctx, key, expiration)
}
