# Cron 包使用注意事项

## 1. 任务名称管理

### ⚠️ 重要：任务名称应该唯一
- 使用 `AddFunc`、`AddJob` 或 `Schedule` 时，**强烈建议**提供唯一的任务名称
- 如果不提供名称，系统会自动生成基于时间戳的名称，可能导致难以管理
- 重复的名称虽然不会报错，但会在日志中记录警告

```go
// ✅ 推荐：使用有意义的唯一名称
c.AddFunc("0 * * * *", myFunc, "hourly-report")

// ❌ 不推荐：依赖自动生成的名称
c.AddFunc("0 * * * *", myFunc) // 名称会是时间戳，难以追踪
```

## 2. 资源清理

### ⚠️ 必须：在程序退出前停止 Cron
- 使用 `Start()` 启动后，**必须**在程序退出前调用 `Stop()` 或 `Close()`
- 未停止的 Cron 会导致 goroutine 泄漏
- 推荐使用 `defer` 确保清理

```go
c := cron.New()
c.Start()
defer c.Stop() // 或 c.Close()

// 或者使用 context 控制生命周期
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

safe.Go(func() {
    <-ctx.Done()
    c.Stop()
})
```

## 3. Redis 分布式锁（可选）

### 使用场景
- 在**多实例部署**环境中，使用 Redis 客户端可以防止同一任务在多个实例上同时执行
- 单实例环境可以不使用 Redis

### 注意事项
- Redis 锁的过期时间固定为 **5 分钟**
- 如果任务执行时间超过 5 分钟，锁可能会提前释放（但任务仍会继续执行）
- Redis 连接失败时，任务会**跳过执行**（不会 panic）

```go
// ✅ 多实例环境：使用 Redis 锁
redisClient := redis.NewClient(&redis.Options{...})
c := cron.New(cron.WithRedisClient(redisClient))

// ✅ 单实例环境：不使用 Redis
c := cron.New()
```

## 4. 错误处理和 Panic 恢复

### 内置保护机制
- 任务执行中的 **panic 会被自动捕获**，不会导致整个 Cron 停止
- Panic 信息会记录到 `ErrorLog`（如果设置了）
- 任务 panic 后，Cron 会继续运行其他任务

### 建议
- 在任务函数内部处理业务错误，避免 panic
- 设置 `ErrorLog` 以便追踪问题

```go
// ✅ 推荐：在任务内部处理错误
c.AddFunc("0 * * * *", func() {
    if err := doSomething(); err != nil {
        log.Error("Task failed: %v", err)
        return // 不要 panic
    }
}, "my-task")

// 设置错误日志
logger := log.New(...)
c.ErrorLog = logger
```

## 5. 时区设置

### ⚠️ 重要：明确指定时区
- 默认使用系统本地时区（`time.Now().Location()`）
- 生产环境建议**明确指定时区**，避免因服务器时区不同导致问题

```go
// ✅ 推荐：明确指定时区
loc, _ := time.LoadLocation("Asia/Shanghai")
c := cron.NewWithLocation(loc)

// ❌ 不推荐：依赖系统时区
c := cron.New() // 可能在不同服务器上行为不一致
```

## 6. Cron 表达式格式

### 两种格式支持
- **5 字段格式**（标准 cron）：`"0 0 * * *"` → 使用 `ParseStandard`
- **6 字段格式**（包含秒）：`"0 0 0 * * *"` → 使用 `Parse`

```go
// 5 字段：分钟 小时 日 月 星期
c.AddFunc("0 0 * * *", func() {}, "daily") // 每天 00:00

// 6 字段：秒 分钟 小时 日 月 星期
c.AddFunc("0 0 0 * * *", func() {}, "daily") // 每天 00:00:00

// 描述符格式
c.AddFunc("@daily", func() {}, "daily")
c.AddFunc("@every 1h", func() {}, "hourly")
```

## 7. 任务执行时间

### 注意事项
- 任务在**独立的 goroutine** 中执行，不会阻塞调度器
- 长时间运行的任务不会影响其他任务的调度
- 但要注意：如果任务执行时间超过调度间隔，可能会**并发执行多次**

```go
// ⚠️ 注意：如果任务执行时间 > 调度间隔，可能并发执行
c.AddFunc("@every 1m", func() {
    time.Sleep(2 * time.Minute) // 这个任务可能并发执行
}, "long-task")

// ✅ 推荐：使用分布式锁防止并发
c := cron.New(cron.WithRedisClient(redisClient))
c.AddFunc("@every 1m", longTask, "long-task")
```

## 8. 任务添加时机

### Start() 前后都可以添加
- **Start() 之前**：直接添加到 entries 列表
- **Start() 之后**：通过 channel 异步添加（线程安全）

```go
c := cron.New()

// ✅ Start 之前添加（推荐，更简单）
c.AddFunc("0 * * * *", func() {}, "task1")
c.Start()

// ✅ Start 之后添加（也支持，线程安全）
c.Start()
c.AddFunc("0 * * * *", func() {}, "task2")
```

## 9. 任务移除

### Remove() 方法
- 移除任务时使用任务名称
- 如果任务不存在，会返回错误
- 运行中移除任务是安全的

```go
// 添加任务
c.AddFunc("0 * * * *", func() {}, "my-task")

// 移除任务
if err := c.Remove("my-task"); err != nil {
    log.Error("Failed to remove task: %v", err)
}
```

## 10. AddOnceFunc 特殊用法

### 一次性任务
- `AddOnceFunc` 会在执行后**自动移除**任务
- 适用于只需要执行一次的场景
- 如果移除失败，任务会继续保留（但会记录日志）

```go
// ✅ 适合场景：初始化任务、一次性清理等
c.AddOnceFunc("@every 1h", func() {
    // 这个任务只执行一次，然后自动移除
    doInitialization()
}, "init-task")
```

## 11. 并发安全

### 线程安全操作
- `AddFunc`、`AddJob`、`Schedule`：线程安全
- `Remove`：线程安全
- `Entries`：线程安全（返回快照）
- `Start`、`Stop`：线程安全

```go
// ✅ 可以在多个 goroutine 中安全调用
safe.Go(func() {
    c.AddFunc("0 * * * *", func() {}, "task1")
})

safe.Go(func() {
    c.AddFunc("0 * * * *", func() {}, "task2")
})
```

## 12. 性能考虑

### 注意事项
- 每个任务在独立 goroutine 中执行
- 大量任务（>1000）可能消耗较多资源
- 建议监控 goroutine 数量

```go
// ⚠️ 注意：避免创建过多任务
for i := 0; i < 10000; i++ {
    c.AddFunc("@every 1m", func() {}, fmt.Sprintf("task-%d", i))
    // 这会创建大量 goroutine
}
```

## 最佳实践总结

1. ✅ **总是提供唯一的任务名称**
2. ✅ **使用 defer c.Stop() 确保资源清理**
3. ✅ **多实例环境使用 Redis 分布式锁**
4. ✅ **明确指定时区，不要依赖系统时区**
5. ✅ **在任务内部处理错误，避免 panic**
6. ✅ **长时间任务注意并发执行问题**
7. ✅ **设置 ErrorLog 以便追踪问题**

## 常见错误示例

```go
// ❌ 错误1：忘记停止 Cron
c := cron.New()
c.Start()
// 程序退出时 goroutine 泄漏

// ❌ 错误2：任务中 panic 导致程序崩溃（实际上会被捕获，但不好）
c.AddFunc("@every 1m", func() {
    panic("something wrong") // 虽然不会崩溃，但不推荐
})

// ❌ 错误3：依赖自动生成的名称
c.AddFunc("@every 1m", func() {}) // 难以管理和追踪

// ❌ 错误4：任务执行时间超过调度间隔，导致并发执行
c.AddFunc("@every 1m", func() {
    time.Sleep(2 * time.Minute) // 可能并发执行
})
```

