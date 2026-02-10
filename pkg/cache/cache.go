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

// ICache 定义缓存接口（抽象）
type ICache interface {
	// Get 获取缓存值
	Get(ctx context.Context, key string) *redis.StringCmd
	// Set 设置缓存值
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
	// Del 删除缓存
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	// Pipeline 创建管道
	Pipeline() redis.Pipeliner
	// HSet 设置 Hash 字段
	HSet(ctx context.Context, key string, values ...any) *redis.IntCmd
	// HGetAll 获取 Hash 所有字段
	HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd
	// HDel 删除 Hash 字段
	HDel(ctx context.Context, key string, fields ...string) *redis.IntCmd
	// Expire 设置过期时间
	Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd
}
