## 上下文

Arcentra 的 DDD 分层迁移已完成"内层"建设（`internal/domain/`、`internal/infra/persistence/`、`internal/case/`、`pkg/` 重组），但"外层"尚未接通：

- **`internal/adapter/http/`**：仅 Agent 路由已实现，其余 11 个路由组（identity/user/user_ext/role/team/project/secret/scm/general_settings/pipeline/storage/ws）虽然在 `Router` 结构体中注入了 Use Case，但 `registerRoutes` 只调用了 `agentRoutes()`
- **`internal/adapter/grpc/`**：空壳 `ServerAdapter`，`ProviderSet` 为空。真实 gRPC 栈仍在 `internal/pkg/grpc/`，注册 5 个服务（Agent、Gateway、Pipeline、StepRun、Stream）并依赖 `*service.Services`
- **`internal/control/bootstrap/`**：`App` 结构体依赖 `control/router.Router`、`control/service.Services`、`control/repo.Repositories`、`internal/pkg/grpc.ServerWrapper`
- **`cmd/arcentra/wire.go`**：Build 链仍为 `config → log → database → cache → metrics → repo → storage → plugin → service → router（control）→ grpc（internal/pkg）→ bootstrap`
- **`internal/pkg/storage/`**：`IStorage` 接口和 5 种云厂商实现直接在 `internal/pkg/` 中，未对接 `domain/agent.IStorageRepository`
- **`internal/pkg/notify/`**：`ProvideNotifyManager` 直接依赖 `*repo.Repositories`（control 层的上帝对象）

**约束**：
- 外部 HTTP/gRPC API 契约不变
- 整个迁移过程系统保持可运行
- Wire 仍为 DI 方案
- Agent 端 `internal/agent/` 不在本次范围
- Proto 定义 `api/*/v1` 不变

## 目标 / 非目标

**目标：**
- 完成 12 个 HTTP 路由组到 `adapter/http/` 的迁移，每个路由依赖 `case/` 层 Use Case
- 实现 `adapter/grpc/` 的完整 gRPC 服务注册（5 服务 + 拦截器），替代 `internal/pkg/grpc/`
- 创建新的 `App` 引导结构体，依赖 adapter 层提供的 HTTP/gRPC 入口，管理完整生命周期
- 将 `internal/pkg/storage` 和 `internal/pkg/notify` 对接到 domain 层接口
- 重写 Wire 组合根，全面切换到 `platform → infra → case → adapter → bootstrap` 链路
- 删除 `internal/control/` 和 `internal/pkg/grpc/`，完成新旧代码切换
- 编译通过 + 测试通过

**非目标：**
- 不重构 Use Case 内部逻辑（仅接线）
- 不修改 HTTP/gRPC 的外部 API 行为
- 不重构 Agent 端
- 不引入新的业务功能
- 不拆分微服务

## 决策

### 决策 1：HTTP 路由迁移策略 — 按域拆分文件 + Use Case 注入

每个路由组迁移为 `adapter/http/` 下的独立文件（`router_user.go`、`router_project.go` 等），方法挂在已有的 `Router` 结构体上。`Router` 已经注入了所有 Use Case（`ManageUser`、`ManageProject` 等），只需在 `registerRoutes()` 中补全路由注册调用。

**迁移模式**（以 `router_user.go` 为例）：
1. 从 `control/router/router_user.go` 复制路由定义和 handler 函数
2. 将 `rt.Services.User.Xxx()` 调用替换为 `rt.ManageUser.Xxx()`
3. 将 `*service.Services` 相关类型引用替换为 `case/identity` 的 DTO
4. 在 `registerRoutes()` 中添加 `rt.userRoutes(api, auth)` 调用

**替代方案**：为每个路由组创建独立的 handler struct + 各自的 Wire provider — 增加 Wire 复杂度和样板代码，且 `Router` 已有正确的注入结构，无需拆分。

### 决策 2：gRPC 适配器设计 — 将 ServerWrapper 迁入 adapter/grpc/

将 `internal/pkg/grpc/grpc_server.go` 的 `ServerWrapper` 结构迁移到 `internal/adapter/grpc/`，但改变其依赖：

- **旧依赖**：`ServerWrapper.Register(services *service.Services, redisClient, db, kafkaSettings)` — 直接依赖 Services 上帝对象
- **新依赖**：每个 gRPC 服务实现改为依赖对应的 `case/` Use Case，通过 Wire 注入

```
internal/adapter/grpc/
├── server.go           // ServerWrapper + NewGrpcServer + Start/Stop
├── interceptor/        // 从 internal/pkg/grpc/interceptor/ 迁入
│   ├── logging.go
│   ├── auth.go
│   └── token_verifier.go
├── service_agent.go    // AgentServiceImpl → 依赖 case/agent Use Case
├── service_gateway.go  // GatewayServiceImpl
├── service_pipeline.go // PipelineServiceImpl → 依赖 case/pipeline Use Case
├── service_steprun.go  // StepRunServiceImpl → 依赖 case/execution Use Case
├── service_stream.go   // StreamService → 依赖 case/execution Use Case
└── provider.go         // ProviderSet
```

5 个 gRPC 服务实现当前散落在 `internal/control/service/service_*_pb.go` 中，需要迁移到 `adapter/grpc/` 并改为依赖 Use Case。

**拦截器**：`interceptor.AgentTokenVerifier` 当前依赖 `service.AgentService` 和 `repo.AgentRepo`，迁移后改为依赖 `case/agent.GetAgentUseCase` 或 domain 层接口。

**替代方案**：保留 `internal/pkg/grpc/` 仅改其依赖 — 违反架构分层（gRPC 是入站适配器，不应在 `internal/pkg/`）。

### 决策 3：Bootstrap 重构 — 新 App 结构体

在 `internal/control/bootstrap/` 原地重构 `App` 结构体（保持包路径不变，避免额外 import 变更），移除对 `control/router`、`control/service`、`control/repo` 的依赖：

```go
type App struct {
    HTTPApp       *fiber.App          // 来自 adapter/http.Router.FiberApp()
    GrpcServer    *grpc.ServerWrapper  // 来自 adapter/grpc
    MetricsServer *metrics.Server
    Logger        *log.Logger
    PluginMgr     *plugin.Manager
    Storage       storage.IStorage
    AppConf       *config.AppConfig
    ShutdownMgr   *shutdown.Manager
    CronAdapter   *cron.Adapter       // SCM 轮询迁入
}
```

**关键变更**：
- `NewApp` 接收 `*adapterhttp.Router` 而非 `*controlrouter.Router`
- 移除 `Repos` 和 `Services` 字段（不再需要上帝对象）
- SCM 轮询从 `Run()` 内联迁到 `adapter/cron/scm_poll.go`，通过 `CronAdapter` 管理

**替代方案 1**：迁移到 `internal/adapter/bootstrap/` — 增加路径变更复杂度，且 bootstrap 不是"适配器"角色。

**替代方案 2**：迁移到 `cmd/arcentra/bootstrap/` — 与 wire.go 同级合理，但破坏当前 import。在此次变更中保持原路径，降低变更风险。

### 决策 4：Storage 对接 domain 层

`internal/pkg/storage/` 已有完整的 `IStorage` 接口和 5 种实现（S3/MinIO/OSS/COS/GCS）。不重写这些实现，而是：

1. 在 `internal/infra/storage/` 创建适配器，将 `internal/pkg/storage.IStorage` 包装为 `domain/agent.IStorageRepository` 实现
2. `internal/pkg/storage/` 保持不变（它是 `pkg` 级别的通用存储抽象）
3. Wire 通过 `infra/storage.ProviderSet` 将实现绑定到 domain 接口

**替代方案**：将 `internal/pkg/storage/` 整体移入 `internal/infra/storage/` — 会影响 Agent 端（`internal/agent/` 也使用此包），不合理。

### 决策 5：Notify 对接 domain 层

`internal/pkg/notify/` 当前的 `ProvideNotifyManager` 直接依赖 `*repo.Repositories`。迁移方式：

1. 定义 domain 层通知相关接口（如果尚未存在）
2. 将 `notify.ProvideNotifyManager` 改为依赖 domain 层的 `INotificationChannelRepository` 接口
3. `ChannelRepositoryAdapter` 改为适配 domain 层接口

**替代方案**：将 notify 整体迁到 `internal/infra/notify/` — 通知系统是基础设施关注点，可以考虑，但本次优先解耦对 `control/repo` 的依赖，位置可后续调整。

### 决策 6：Wire 重写策略 — 一次性切换

`cmd/arcentra/wire.go` 从旧链路一次性切换到新链路：

```go
wire.Build(
    // 平台层（配置、日志、指标）
    config.ProviderSet,
    log.ProviderSet,
    database.ProviderSet,
    cache.ProviderSet,
    metrics.ProviderSet,
    // 基础设施层（仓储实现、缓存适配、存储适配）
    persistence.ProviderSet,
    infraStorage.ProviderSet,
    // 应用层（Use Case）
    agentCase.ProviderSet,
    identityCase.ProviderSet,
    projectCase.ProviderSet,
    pipelineCase.ProviderSet,
    executionCase.ProviderSet,
    // 适配器层（HTTP、gRPC、Cron）
    adapter.ProviderSet,
    // 插件
    plugin.ProviderSet,
    // 引导
    bootstrap.NewApp,
)
```

**替代方案**：渐进式切换（先 adapter/http，再 adapter/grpc…）— 每步都需要 Wire 能编译，中间态会有 control + adapter 混合依赖，更复杂。由于所有"内层"已就绪，一次性切换更干净。

### 决策 7：旧代码删除 — 验证后清理

删除顺序：
1. 先完成所有适配器迁移和 Wire 切换
2. 运行 `go build ./...` 确认无编译引用
3. 删除 `internal/control/`（router、service、repo、model、config、bootstrap）
4. 删除 `internal/pkg/grpc/`
5. 运行 `go build ./...` + `go test ./...` 最终验证

**注意**：`internal/control/bootstrap/` 如果选择原地重构（决策 3），则该包保留但内容已更新，不删除。

## 风险 / 权衡

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| gRPC 服务实现迁移时遗漏业务逻辑 | 高 | 逐个服务对比旧实现，确保所有 RPC 方法都已迁移；用 proto 文件验证完整性 |
| Wire 一次性切换编译错误多 | 中 | 先用 `wire check` 验证依赖图，逐步修复类型不匹配；每修复一批立即编译 |
| token_verifier 依赖链变更导致 Agent 认证失败 | 高 | 迁移后保持完全相同的验证逻辑，仅改变依赖注入方式；编写集成测试验证 |
| Storage/Notify 适配层引入额外间接调用 | 低 | Go 接口调用开销极小；适配器层仅做类型转换，无业务逻辑 |
| SCM 轮询迁移后 cron 调度行为变化 | 低 | 保持相同的 cron 表达式和超时配置；确认 `adapter/cron/` 的生命周期管理与旧代码一致 |
| 12 个路由迁移的请求/响应 DTO 类型不匹配 | 中 | HTTP handler 的 DTO 可复用 `case/` 层已定义的 DTO；对齐 JSON tag 命名 |
| 删除旧代码后发现遗漏引用 | 中 | 删除前用 `grep -r "internal/control" --include="*.go"` 全扫描；保留 git 历史可回滚 |
