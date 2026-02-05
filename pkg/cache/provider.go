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
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
)

// defaultLocalMaxBytes is the default cache size (32MB)
const defaultLocalMaxBytes = 32 * 1024 * 1024

// ProviderSet 提供缓存依赖（Redis + 本地 FastCache）
var ProviderSet = wire.NewSet(
	ProvideRedisCmdable,
	ProvideICache,
	ProvideFastCache,
	ProvideHybridCache,
)

// ProvideRedisCmdable 提供 Redis 实例（支持单节点、Sentinel 和集群模式）
func ProvideRedisCmdable(conf Redis) (redis.Cmdable, error) {
	return NewRedisCmdable(conf)
}

// ProvideICache 提供 ICache 接口实例
func ProvideICache(cmdable redis.Cmdable) ICache {
	return NewRedisCache(cmdable)
}

// ProvideFastCache 提供 FastCache 实例（默认 32MB）
func ProvideFastCache() *FastCache {
	return NewFastCache(FastCacheConfig{MaxBytes: defaultLocalMaxBytes})
}

// ProvideHybridCache 提供混合缓存实例（本地 FastCache + 远程 Redis）
func ProvideHybridCache(local *FastCache, remote ICache) *HybridCache {
	return NewHybridCache(local, remote, HybridCacheConfig{
		LocalEnabled:  true,
		RemoteEnabled: true,
		LocalMaxBytes: defaultLocalMaxBytes,
		LocalTTLRatio: 0.8,
	})
}
