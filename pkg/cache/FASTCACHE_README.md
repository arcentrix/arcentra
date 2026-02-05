# FastCache - 高性能本地缓存实现

## 概述

FastCache 是基于 VictoriaMetrics 的 `fastcache` 库实现的高性能本地缓存解决方案。它提供了三层递进式的缓存能力：

1. **FastCache**: 轻量级本地内存缓存
2. **HybridCache**: 本地 + 分布式混合缓存
3. **CachedQueryWithHybrid**: 查询缓存抽象层

## 核心特性

### FastCache 特性

- ✅ **超高性能**: 基于 VictoriaMetrics fastcache，内存访问速度极快
- ✅ **自动过期**: 支持 TTL，自动清理过期数据
- ✅ **内存可控**: 可配置最大内存占用
- ✅ **简单易用**: 原生支持 string、bytes 类型，支持 JSON 序列化
- ✅ **统计信息**: 提供缓存统计数据（项数、内存占用等）
- ✅ **Thread-safe**: 使用互斥锁保证并发安全

### HybridCache 特性

- ✅ **分层缓存**: 结合本地缓存和 Redis 缓存
- ✅ **灵活配置**: 可单独启用/禁用本地或远程缓存
- ✅ **自动同步**: 支持后台同步本地缓存到 Redis
- ✅ **TTL 优化**: 可为本地缓存独立配置 TTL 比例
- ✅ **降级策略**: 远程缓存不可用时自动降级到本地缓存
- ✅ **生命周期管理**: 提供优雅的启动和停止机制

### CachedQueryWithHybrid 特性

- ✅ **缓存-旁路模式**: 标准的 Cache-Aside 实现
- ✅ **泛型支持**: 支持任意类型的数据缓存
- ✅ **自动序列化**: 使用 sonic 库进行高效序列化
- ✅ **失效管理**: 支持手动失效缓存
- ✅ **错误处理**: 完整的错误处理和日志记录

## 文件结构

```
Arcentra/pkg/cache/
├── fastcache.go                 # FastCache 实现
├── hybrid_cache.go              # HybridCache 混合缓存实现
├── wire_providers.go            # Wire 依赖注入提供者
├── fastcache_test.go            # 单元测试
├── example_test.go              # 使用示例
├── FASTCACHE_README.md          # 本文件
├── FASTCACHE_GUIDE.md           # 详细使用指南
├── cache.go                     # ICache 接口定义
├── redis.go                     # Redis 配置和初始化
└── cached_query.go              # 原有的查询缓存实现
```

## 快速开始

### 1. 安装依赖

fastcache 已作为间接依赖包含在项目中：

```bash
go get github.com/VictoriaMetrics/fastcache
```

### 2. 基础使用

```go
import "github.com/arcentrix/arcentra/pkg/cache"

// 创建本地缓存
fc := cache.NewFastCache(cache.FastCacheConfig{
    MaxBytes: 32 * 1024 * 1024, // 32MB
})
defer fc.Clear()

// 设置缓存
fc.Set(ctx, "user:123", "Alice", 1*time.Hour)

// 获取缓存
cmd := fc.Get(ctx, "user:123")
println(cmd.Val()) // Output: Alice

// 删除缓存
fc.Del(ctx, "user:123")
```

### 3. 混合缓存使用

```go
// 创建混合缓存
localCache := cache.NewFastCache(config)
remoteCache := cache.NewRedisCache(rdb)

hybridCache := cache.NewHybridCache(
    localCache,
    remoteCache,
    cache.HybridCacheConfig{
        LocalEnabled:  true,
        RemoteEnabled: true,
        LocalTTLRatio: 0.8,
        SyncToRemote:  true,
        SyncInterval:  30 * time.Second,
    },
)
defer hybridCache.Stop()

// 使用方式与单一缓存相同
hybridCache.Set(ctx, "session:123", "data", 1*time.Hour)
```

### 4. Wire 依赖注入

```go
import "github.com/google/wire"

// 在 wire.go 中
func init() {
    wire.Build(
        cache.ProviderSet, // 标准缓存（Redis + FastCache）
        // 或者
        cache.LocalOnlyProviderSet, // 仅本地缓存（开发/测试）
    )
}
```

## 架构设计

### FastCache 设计

```
┌─────────────────────────────┐
│      FastCache              │
├─────────────────────────────┤
│ - VictoriaMetrics fastcache │
│ - sync.Map (TTL tracking)   │
│ - RWMutex (concurrency)     │
└─────────────────────────────┘
         ↓
┌─────────────────────────────┐
│   Memory Operations         │
│ (O(1) access, no network)   │
└─────────────────────────────┘
```

### HybridCache 架构

```
┌────────────────────────────────────────┐
│         HybridCache                    │
├────────────────────────────────────────┤
│  ┌──────────────────┐                  │
│  │  Local Layer     │                  │
│  │  (FastCache)     │ ← Fast access    │
│  └────────┬─────────┘                  │
│           │ (miss)                     │
│  ┌────────▼─────────┐                  │
│  │  Remote Layer    │                  │
│  │  (Redis)         │ ← Distributed    │
│  └────────┬─────────┘                  │
│           │ (miss)                     │
│  ┌────────▼──────────────┐             │
│  │  Background Sync      │             │
│  │  (Periodic)           │             │
│  └───────────────────────┘             │
└────────────────────────────────────────┘
```

### CachedQuery 模式

```
Get(params...)
    ↓
Check Local Cache (FastCache)
    ↓ (hit)
Return ✓
    
    ↓ (miss)
Check Remote Cache (Redis)
    ↓ (hit)
Update Local Cache (async)
Return ✓
    
    ↓ (miss)
Query Database
    ↓
Update Both Caches
Return ✓
```

## 性能对比

| 操作 | FastCache | Redis | 备注 |
|------|-----------|-------|------|
| Get | ~1μs | ~0.5ms | FastCache 快 500 倍 |
| Set | ~1μs | ~0.5ms | FastCache 快 500 倍 |
| 内存开销 | 低 | 中等 | FastCache 更高效 |
| 分布式共享 | ✗ | ✓ | Redis 支持分布式 |
| 自动过期 | ✓ | ✓ | 都支持 |

## 配置参数

### FastCacheConfig

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| MaxBytes | int | 32MB | 最大内存占用 |

### HybridCacheConfig

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| LocalEnabled | bool | true | 启用本地缓存 |
| RemoteEnabled | bool | true | 启用远程缓存 |
| LocalMaxBytes | int | 32MB | 本地缓存大小 |
| LocalTTLRatio | float64 | 0.8 | 本地 TTL 比率 |
| SyncToRemote | bool | true | 是否同步到远程 |
| SyncInterval | time.Duration | 30s | 同步间隔 |

## 使用场景

### 适合 FastCache 的场景

✅ 单机应用  
✅ 热点数据缓存  
✅ 对性能要求极高  
✅ 数据量较小  
✅ 无需跨节点共享  

### 适合 HybridCache 的场景

✅ 分布式系统  
✅ 高并发应用  
✅ 需要缓存共享  
✅ 性能和一致性要求平衡  
✅ Redis 基础设施可用  

### 适合 CachedQueryWithHybrid 的场景

✅ 数据库查询优化  
✅ API 响应缓存  
✅ 用户信息缓存  
✅ 配置数据缓存  
✅ 计算结果缓存  

## 最佳实践

### 1. Key 命名规范

```go
// 推荐
"user:123"
"product:456:details"
"order:789:items"

// 避免
"u123"
"123"
```

### 2. TTL 策略

```go
// 热数据：较长 TTL
cache.Set(ctx, "hot:key", value, 24*time.Hour)

// 温数据：中等 TTL
cache.Set(ctx, "warm:key", value, 1*time.Hour)

// 冷数据：较短 TTL
cache.Set(ctx, "cold:key", value, 5*time.Minute)
```

### 3. 混合缓存配置

```go
config := cache.HybridCacheConfig{
    LocalEnabled:  true,
    RemoteEnabled: true,
    LocalTTLRatio: 0.8,  // 本地缓存更短，减少不一致
    SyncToRemote:  true,
    SyncInterval:  30 * time.Second,
}
```

### 4. 错误处理

```go
result, err := query.Get(ctx, params...)
if err != nil {
    log.Errorf("cache query failed: %v", err)
    // 降级处理或返回错误
    return nil, err
}
```

### 5. 缓存预热

```go
func warmupCache(ctx context.Context, cache *HybridCache) {
    hotData := queryHotData() // 从数据库查询
    for _, item := range hotData {
        cache.Set(ctx, item.Key, item.Value, 24*time.Hour)
    }
}
```

## 监控和调试

### 获取缓存统计信息

```go
stats := localCache.Stats()
log.Infof("Items: %d, Bytes: %d, Max: %d",
    stats.ItemsCount,
    stats.BytesSize,
    stats.MaxBytesSize,
)
```

### 监控缓存命中率

```go
// 在应用中统计缓存命中和未命中
type CacheMetrics struct {
    hits   int64
    misses int64
}

func (m *CacheMetrics) HitRate() float64 {
    total := m.hits + m.misses
    if total == 0 {
        return 0
    }
    return float64(m.hits) / float64(total)
}
```

## 故障排除

### 缓存未命中

检查清单：
- [ ] Key 是否正确拼写
- [ ] 数据是否已过期
- [ ] 缓存是否被清空
- [ ] 本地/远程缓存是否启用

### 内存占用过高

解决方案：
- 减少 MaxBytes 配置
- 缩短 TTL 时间
- 增加同步和清理频率
- 优化缓存数据大小

### Redis 同步失败

检查项：
- Redis 连接是否正常
- 网络是否连通
- Redis 内存是否充足
- 错误日志中的具体错误信息

## 性能优化建议

1. **合理设置内存上限**: 根据实际可用内存调整
2. **优化 TTL 配置**: 热数据长 TTL，冷数据短 TTL
3. **使用异步同步**: 减少主线程阻塞
4. **监控命中率**: 定期检查缓存效率
5. **定期清理**: 防止内存泄漏

## 对比其他方案

| 方案 | 性能 | 分布式 | 一致性 | 复杂度 |
|------|------|--------|--------|--------|
| FastCache | ⭐⭐⭐⭐⭐ | ✗ | ✓ | ⭐ |
| Redis Only | ⭐⭐⭐ | ✓ | ✓ | ⭐⭐ |
| HybridCache | ⭐⭐⭐⭐ | ✓ | ⭐⭐⭐ | ⭐⭐⭐ |
| Memcached | ⭐⭐⭐⭐ | ✓ | ⭐⭐ | ⭐⭐ |

## 集成示例

### 与 Wire 集成

```go
// wire.go
//go:build wireinject
package main

import (
    "github.com/google/wire"
    "github.com/arcentrix/arcentra/pkg/cache"
)

func initApp() (*App, error) {
    wire.Build(
        cache.ProviderSet,
        newApp,
    )
    return nil, nil
}
```

### 与 Fiber 框架集成

```go
app := fiber.New()

// 使用混合缓存
cacheMiddleware := func(c *fiber.Ctx) error {
    key := c.Path()
    if cached := cache.Get(c.Context(), key); cached.Val() != "" {
        return c.SendString(cached.Val())
    }
    return c.Next()
}

app.Use(cacheMiddleware)
```

### 与 GORM 集成

```go
type User struct {
    ID   int
    Name string
}

func GetUser(ctx context.Context, id int) (*User, error) {
    query := cache.NewCachedQueryWithHybrid(
        hybridCache,
        func(params ...any) string {
            return fmt.Sprintf("user:%d", params[0])
        },
        func(ctx context.Context) (User, error) {
            var user User
            db.WithContext(ctx).First(&user, id)
            return user, nil
        },
        1*time.Hour,
    )
    
    return query.Get(ctx, id)
}
```

## 常见问题 FAQ

**Q: FastCache 和 Redis 应该如何选择？**  
A: 单机选 FastCache，分布式选 HybridCache 或 Redis。

**Q: HybridCache 中本地和远程数据不一致怎么办？**  
A: 可以设置本地 TTL 比远程短，或增加同步频率。

**Q: 如何监控缓存效果？**  
A: 记录命中/未命中数，定期检查命中率。

**Q: 大数据量场景下如何使用？**  
A: 分层存储，热数据用缓存，冷数据用数据库。

## 参考资源

- [VictoriaMetrics fastcache](https://github.com/VictoriaMetrics/fastcache)
- [Redis Go Client](https://github.com/redis/go-redis)
- [Google Wire](https://github.com/google/wire)
- [使用指南](./FASTCACHE_GUIDE.md)

## License

Apache License 2.0
