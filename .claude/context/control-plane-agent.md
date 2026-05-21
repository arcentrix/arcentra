# 控制平面与 Agent 架构

本文档供 Claude 在审查跨进程通信、Agent 生命周期、Repository / Service 分层、Wire DI 相关 PR 时复用。

## 双进程模型

arcentra 由两个独立二进制组成，共享 `internal/shared/` 与 `pkg/`：

| 进程 | 入口 | 引导 |
|------|------|------|
| 控制平面 | `cmd/arcentra/main.go` | `internal/control/bootstrap/bootstrap.go` |
| Agent | `cmd/arcentra-agent/main.go` | `internal/agent/bootstrap/bootstrap.go` |
| CLI | `cmd/cli/main.go` | 轻量命令行工具 |

两进程都用 Google Wire 编译时 DI：`wire.go`（手写）+ `wire_gen.go`（生成）。

## 控制平面分层

```
internal/control/
├── bootstrap/      # 引导：加载配置 → Wire 注入 → 启动 router / process / cron
├── config/         # 配置结构体（TOML / 环境变量）
├── consts/         # 领域常量、错误
├── model/          # GORM 模型（映射 t_xxx 表）
├── repo/           # Repository 接口与实现（IXxxRepository）
├── service/        # 业务服务（XxxService）
├── router/         # Fiber v2 HTTP 路由 + 中间件
├── process/        # 流水线编排（Process / Coordinator / AgentCancel / ArtifactCleanup）
└── authz/          # RBAC 鉴权
```

### Model 约定

- 所有 model 嵌入 `BaseModel`（`ID` / `CreatedAt` / `UpdatedAt`）
- 表名前缀 `t_`：`t_pipeline`、`t_pipeline_run`、`t_job_run`、`t_agent`、`t_org`、`t_team` 等
- 软删除：`is_enabled tinyint(1)` + `is_deleted tinyint(1)`
- 业务唯一标识：`xxx_id varchar(64)` 存 UUID 字符串，加唯一索引
- 字符集 / 排序：`utf8mb4` / `utf8mb4_0900_ai_ci`
- 必须实现 `TableName() string`

### Repository 约定

- 接口名以 `I` 开头：`IPipelineRepository`、`IPipelineRunRepository`、`IJobRunRepository`、`IAgentRepository`
- 接口方法用业务 ID 查询：`GetByPipelineID(ctx, pipelineID string)`，不暴露 `GetByID(ctx, id uint64)`
- 接口方法第一个参数永远是 `context.Context`
- 实现层使用 GORM，禁止 `SELECT *`，按需 `Select("xxx_id, name, status")`
- 聚合在 `repo.Repositories` 结构体中，通过 Wire 注入到 service
- 禁止把 `*gorm.DB` 漏到 service 层

### Service 约定

- 接口名 `IXxxService`，实现 `XxxService`
- 持有 `*repo.Repositories`、`pkg/*` 客户端、`process.IProcess` 等依赖（通过 Wire 注入）
- 业务日志在此层打印
- 跨表事务：在 service 用 `repo.Tx(ctx, func(txRepo) error { ... })` 包装，禁止直接拿 `gorm.DB.Transaction`

### Router 约定

- 基于 Fiber v2
- API 路径前缀：`/api/v1/`
- 中间件链（顺序固定，见 bootstrap）：
  `RealIP → RequestID → Trace → Recover → Metrics → CORS → UnifiedResponse → AccessLog → i18n → pprof`
- JWT 鉴权按路由组挂载
- handler 只做参数解析、调用 service、返回；业务逻辑不下沉

## Agent 分层

```
internal/agent/
├── bootstrap/        # 引导：注册 → 心跳 → 启动 Worker / Outbox / Router（健康检查）
├── config/           # 配置
├── outbox/           # WAL 发件箱（Agent → 控制平面状态投递）
├── router/           # 健康检查 / metrics（本地 HTTP）
├── service/          # Agent 内部服务（注册、心跳、任务上报）
├── taskqueue/        # Worker：Nova 任务消费 + 分发
└── storage_holder.go # 共享 storage 客户端
```

### Worker 分发

`taskqueue/worker.go`：

| 任务类型 | 行为 |
|----------|------|
| `TaskTypeJobRun` | git clone → 下载产物 → 顺序执行 step → 上传产物 → 上报状态 |
| `TaskTypeStepRun` | 仅执行单步（用于继承 Job 工作区的场景） |
| `TaskTypeAgentCancel` | 中断 Job / Step 执行 |

### Outbox

`internal/agent/outbox/`：

- WAL 写盘 → 后台 drainer → gRPC 调控制平面
- 控制平面 ack 后才删除 WAL 项
- Agent 重启后 replay 未 ack 项
- 至少一次投递；下游必须幂等

## 控制平面 ↔ Agent 通信

| 方向 | 协议 | 用途 |
|------|------|------|
| Agent → 控制平面 | gRPC `Register` | 注册 + 上报能力 |
| Agent → 控制平面 | gRPC `Heartbeat` stream | 周期心跳 / 容量 |
| 控制平面 → Agent | Kafka (Nova) | 任务派发 |
| Agent → 控制平面 | gRPC `StreamJobStatus` | 状态回流 |
| Agent → 控制平面 | gRPC `StreamLog` | 实时日志（`pkg/logstream`） |
| Agent → 控制平面 | HTTP（控制平面 SSE） | 极少数浏览器直推场景 |

gRPC 拦截器（`internal/shared/grpc/`）：tracing、auth、recover、log、metrics。

## 共享代码

```
internal/shared/
├── convert/         # 类型转换工具
├── dsl/             # 流水线表达式 DSL
├── executor/        # 步骤执行（shell / plugin / http / unified） + Kafka publisher
├── grpc/            # gRPC server / client 封装 + 拦截器
├── notify/          # 11 通道通知系统
├── pipeline/        # 流水线执行引擎核心
├── prefixtree/      # 路径前缀树（路由 / 权限匹配）
├── sse/             # Server-Sent Events
└── storage/         # 多云对象存储 IStorage 抽象
```

## pkg 依赖

`pkg/` 中 arcentra 自有的关键库：

| 包 | 职责 |
|----|------|
| `pkg/nova` | Kafka 任务队列（时间轮、优先级、批量、延迟） |
| `pkg/plugin` | 插件框架（12 类型、生命周期、Action 路由、TOML 热加载） |
| `pkg/plugins` | 内置插件实现 |
| `pkg/cron` | 分布式 Cron（Redis `SET NX` 去重） |
| `pkg/dag` | 通用 DAG（环检测 / 拓扑排序） |
| `pkg/statemachine` | 通用线程安全状态机（钩子 / 历史） |
| `pkg/safe` | `SafelyGo`、panic recover、defer 工具 |
| `pkg/cache` | 本地 / Redis 二级缓存 |
| `pkg/database` | GORM 初始化、健康检查 |
| `pkg/mq` | Kafka 抽象 |
| `pkg/taskqueue` | 任务队列底层接口（被 nova 使用） |
| `pkg/logstream` | 日志流（Agent → 控制平面 → 前端） |
| `pkg/scm` | Git 操作（clone / checkout / push） |
| `pkg/sandbox` | 沙箱化 shell 执行 |
| `pkg/dispatch` | 事件分发 |
| `pkg/sso` | SSO 集成 |
| `pkg/auth` | JWT / API Key |
| `pkg/i18n` | 国际化 |
| `pkg/trace` / `pkg/metrics` / `pkg/log` / `pkg/logger` | 可观测性 |

`pkg/` 严禁：

- 直接 `database/sql` / GORM（应在 pkg 定义接口，在 `internal/control/repo` 实现）
- import `internal/`
- 持有业务领域常量

## Wire DI 拓扑

```
cmd/arcentra/wire.go
  ↓ wire.Build(
    config.ProviderSet,
    database.ProviderSet,
    cache.ProviderSet,
    mq.ProviderSet,
    nova.ProviderSet,
    repo.ProviderSet,
    service.ProviderSet,
    process.ProviderSet,
    router.ProviderSet,
    bootstrap.ProviderSet,
    ...
  )
  → 生成 wire_gen.go

cmd/arcentra-agent/wire.go
  ↓ wire.Build(
    config.ProviderSet,
    mq.ProviderSet,
    nova.ProviderSet,
    storage.ProviderSet,
    plugin.ProviderSet,
    grpc.ProviderSet,
    outbox.ProviderSet,
    taskqueue.ProviderSet,
    bootstrap.ProviderSet,
    ...
  )
```

每个包 `provider.go` 暴露 `ProviderSet` 变量。修改 `wire.go` 或新加 provider 后必须 `make wire`。

## 配置加载

- TOML 优先，环境变量覆盖
- 默认配置在 `conf.d/`
- 热加载（部分模块）：插件 TOML、cron job 定义

## 启动顺序

控制平面 bootstrap：

1. 加载配置
2. 初始化 logger / trace / metrics
3. 初始化 GORM / Redis / Kafka 客户端
4. Wire 构建所有 service / repo / process / router
5. 启动 cron / nova worker（控制平面消费部分管理任务） / pprof
6. 启动 HTTP / gRPC server
7. 信号处理 → 优雅停机（`pkg/shutdown`）

Agent bootstrap：

1. 加载配置
2. 初始化 logger / trace / metrics
3. gRPC 连接控制平面 → Register
4. 启动心跳
5. 启动 Outbox drainer
6. 启动 Nova Worker（消费任务）
7. 启动本地 HTTP（健康检查 / metrics）
8. 信号处理 → 优雅停机
