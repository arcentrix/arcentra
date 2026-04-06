## 上下文

Arcentra 是一个 CI/CD 平台，采用多二进制架构：控制面 `arcentra`（HTTP + gRPC）、分布式 `arcentra-agent`、CLI 工具。当前控制面核心逻辑集中在 `internal/control/` 下，采用经典分层：

```
cmd/arcentra/wire.go (Wire DI 组合根)
  → config → log → database → cache → metrics
  → repo.Repositories (20+ 仓储聚合在一个结构体)
  → service.Services (15+ 服务聚合在一个结构体，部分直接暴露 Repo)
  → router.Router (Fiber HTTP)
  → grpc.ServerWrapper
  → bootstrap.App
```

**现状问题**：
1. `Repositories` 和 `Services` 是上帝对象，所有领域的仓储/服务混在一起，无边界约束
2. `Services` 直接暴露 `ProjectMemberRepo`、`StepRunRepo` 等 Repo，表现层可以绕过服务层直接操作数据
3. `internal/control/model/` 中的 GORM 模型同时承担领域模型和持久化映射双重职责
4. `internal/control/router/` 中 HTTP Router 直接依赖 `*service.Services`，跨域调用无约束
5. gRPC 服务实现在 `internal/pkg/grpc/` 中，与 HTTP Router 不在同一层级，职责混乱
6. 已预留的 DDD 目录结构（`internal/domain/`、`internal/adapter/` 等）全部为空，代码未迁入
7. `pkg/` 公共库已膨胀到 ~50 个扁平包，缺乏分类：纯工具（env/id/safe/retry）、基础设施（database/cache/log/metrics）、引擎（dag/dispatch/runner/sandbox）、集成（scm/sso/plugin）等全部平铺在同一层级，新包随意添加

**约束**：
- 系统必须在整个迁移过程中保持可运行
- Wire 依赖注入仍作为 DI 方案
- Agent 端 `internal/agent/` 相对独立，此次重构聚焦控制面
- Proto/gRPC API 契约（`api/*/v1`）保持不变

## 目标 / 非目标

**目标：**
- 按 5 个有界上下文（Agent、Execution、Identity、Pipeline、Project）将领域逻辑拆分到 `internal/domain/` 下
- 领域层仅包含纯 Go 结构体和接口，不依赖任何基础设施框架（GORM、Fiber、gRPC 等）
- 建立应用层（`internal/case/`）作为用例编排，替代 Services 上帝对象
- 实现端口与适配器模式，将入站适配器（HTTP、gRPC、MQ、WS）统一到 `internal/adapter/`
- 将仓储实现、缓存、MQ 等基础设施迁到 `internal/infra/`，实现依赖倒置
- 将 `pkg/` 从 ~50 个扁平包重组为 8 个按职责分组的子目录，建立清晰的包分类体系
- 定义增量迁移路径，确保每个阶段都可编译、可测试、可部署

**非目标：**
- 不拆分微服务 — 保持单体部署
- 不更换 ORM（仍用 GORM）或 Web 框架（仍用 Fiber）
- 不重构 Agent 端架构（`internal/agent/`）
- 不引入 CQRS 或事件溯源 — 仅预留领域事件定义
- 不更改外部 API 行为（HTTP/gRPC 接口契约不变）

## 决策

### 决策 1：分层架构模型

采用四层架构（从内到外）：

```
┌─────────────────────────────────────────────────────┐
│  internal/adapter/    (入站适配器: HTTP, gRPC, MQ)   │
│  ┌─────────────────────────────────────────────────┐ │
│  │  internal/case/     (应用层: Use Case 编排)      │ │
│  │  ┌─────────────────────────────────────────────┐ │ │
│  │  │  internal/domain/  (领域层: 模型+接口)       │ │ │
│  │  └─────────────────────────────────────────────┘ │ │
│  └─────────────────────────────────────────────────┘ │
│  internal/infra/      (基础设施: 仓储实现, 缓存等)   │
└─────────────────────────────────────────────────────┘
  internal/platform/    (跨切面: 配置, 日志, 指标, 安全)
  internal/engine/      (Pipeline 引擎子系统)
  pkg/                  (公共库, 按职责分组)
```

**依赖规则**：
- `domain` 不依赖任何其他层（纯 Go）
- `case` 仅依赖 `domain`（通过接口）
- `adapter` 依赖 `case` 和 `domain`（类型）
- `infra` 依赖 `domain`（实现接口）
- `platform` 可被任何层使用
- `engine` 依赖 `domain`，被 `case` 调用

**替代方案**：六边形架构（端口 + 适配器纯粹形式）— 过度设计，Go 社区更习惯分层 + 接口的方式。

### 决策 2：有界上下文划分

基于现有 `internal/control/` 的 model/repo/service 分析，识别 5 个有界上下文：

| 上下文 | 包含的实体 | 当前来源 |
|--------|----------|---------|
| `identity` | User, UserExt, Role, Menu, UserRoleBinding, RoleMenuBinding, Identity, Team, TeamMember | model_user.go, model_role.go, model_menu.go, model_team.go, service: UserService, IdentityService, TeamService, RoleService, MenuService |
| `project` | Project, ProjectMember, ProjectTeamAccess, Secret, GeneralSettings | model_project.go, model_secret.go, service: ProjectService, SecretService, GeneralSettingsService |
| `pipeline` | Pipeline（定义态）, Step 定义, Plugin 配置 | model_pipeline.go, service 中 Pipeline 相关 |
| `execution` | StepRun, ExecutionRecord, Log | model_step_run.go, service: LogAggregator, StepRun 相关 |
| `agent` | Agent, Storage, Notification | model_agent.go, model_storage.go, service: AgentService, StorageService, UploadService |

**替代方案**：更细粒度拆分（如 notification 独立）— 现阶段实体较少，过度拆分增加复杂度，后续按需提取。

### 决策 3：领域层设计模式

每个有界上下文目录结构：

```
internal/domain/<context>/
├── model.go          // 领域实体、值对象（纯 Go struct，无 GORM tag）
├── repository.go     // 仓储接口定义
├── service.go        // 领域服务（纯领域逻辑，不含编排）
├── event.go          // 领域事件定义（结构体，暂不实现发布）
└── consts.go         // 领域常量、枚举
```

- 领域模型与持久化模型分离：`domain/<ctx>/model.go`（领域）vs `infra/persistence/<ctx>/model.go`（GORM 映射），通过转换函数互转
- 仓储接口在领域层定义，实现在 `infra/persistence/` 下

**替代方案**：共用一套模型（加 GORM tag）— 短期简单但违反 DDD 原则，领域模型被基础设施污染。权衡后选择分离，可在第一阶段先用同一 struct 加 tag，后续再拆。

### 决策 4：应用层（Use Case）设计

```
internal/case/<context>/
├── <use_case_name>.go    // 每个用例一个文件
└── dto.go                // 入参/出参 DTO
```

- 每个 Use Case 是一个结构体 + `Execute` 方法，通过构造函数注入领域仓储接口
- 跨上下文调用通过应用层编排，不允许领域服务直接调用另一个上下文
- 替代现有 `Services` 上帝对象：按上下文拆分，按用例组织

**替代方案**：保留 Service 聚合结构体 + 按上下文拆分 — 不够清晰，Use Case 模式更明确表达意图。

### 决策 5：适配器层设计

```
internal/adapter/
├── http/
│   ├── middleware/      // JWT、RBAC、CORS 等
│   ├── router/          // 按上下文分文件（router_user.go, router_agent.go 等）
│   ├── dto/             // HTTP 请求/响应 DTO
│   └── router.go        // 路由注册总入口
├── grpc/
│   ├── server.go        // gRPC Server 创建
│   └── <service>.go     // 各 gRPC 服务实现
├── mq/                  // MQ 消费者适配器
├── ws/                  // WebSocket 适配器
└── cron/                // 定时任务适配器
```

- HTTP Router 依赖 `case` 层的 Use Case，不直接依赖 `domain` 的 Service 或 Repo
- gRPC 从 `internal/pkg/grpc/` 迁入 `internal/adapter/grpc/`

### 决策 6：基础设施层设计

```
internal/infra/
├── persistence/
│   ├── <context>/       // 按上下文分，每个包含 repo 实现 + GORM 模型
│   ├── mysql/           // MySQL 特定实现
│   └── sqlite/          // SQLite 特定实现
├── cache/               // Redis 缓存实现
├── mq/
│   └── kafka/           // Kafka 生产者实现
├── telemetry/       // 链路追踪、指标适配
├── scm/                 // SCM 适配器实现
└── storage/             // 对象存储实现
```

### 决策 7：pkg/ 公共库重组

将 ~50 个扁平包按职责归入 8 个分组目录：

```
pkg/
├── foundation/       # 纯工具（零业务依赖）
│   ├── env/          # 环境变量
│   ├── id/           # ID 生成（snowflake 等）
│   ├── net/          # 网络工具
│   ├── num/          # 数字工具
│   ├── safe/         # goroutine 安全启动
│   ├── serde/        # 序列化/反序列化
│   ├── time/         # 时间工具
│   ├── util/         # 通用工具
│   ├── version/      # 版本信息
│   ├── retry/        # 重试
│   ├── parallel/     # 并行执行
│   ├── ringbuffer/   # 环形缓冲
│   ├── loop/         # 循环工具
│   ├── orderly/      # 有序执行
│   └── request/      # HTTP 请求工具
├── telemetry/    # 可观测性
│   ├── log/          # 日志（合并 log + logger）
│   ├── metrics/      # 指标采集
│   ├── trace/        # 链路追踪
│   └── pprof/        # 性能分析
├── store/            # 数据存储抽象
│   ├── database/     # 数据库抽象（GORM wrapper）
│   └── cache/        # 缓存抽象（Redis wrapper）
├── transport/        # 传输层
│   ├── http/         # HTTP 工具（jwt, middleware）
│   ├── ws/           # WebSocket
│   ├── auth/         # 认证工具
│   └── i18n/         # 国际化
├── message/        # 消息与事件
│   ├── mq/           # MQ 抽象（kafka, rocketmq）
│   ├── event/        # 事件总线
│   ├── outbox/       # 发件箱模式
│   └── nova/         # 任务队列/Broker
├── engine/           # CI/CD 引擎核心
│   ├── dag/          # DAG 引擎
│   ├── dispatch/     # 任务分发
│   ├── runner/       # 运行器
│   ├── sandbox/      # 沙箱执行
│   ├── statemachine/ # 状态机
│   ├── taskqueue/    # 任务队列
│   ├── logstream/    # 日志流
│   └── record/       # 运行记录
├── integration/      # 外部集成
│   ├── scm/          # SCM 适配（github/gitlab/gitea/gitee/bitbucket）
│   ├── sso/          # SSO 集成（ldap/oauth/oidc）
│   ├── plugin/       # 插件框架
│   ├── plugins/      # 内置插件（git/svn）
│   └── agent/        # Agent 通信
└── lifecycle/        # 生命周期管理
    ├── cron/         # 定时任务
    └── shutdown/     # 优雅关闭
```

**分组原则**：
- `foundation`：零外部依赖的纯工具函数，任何层都可以使用
- `telemetry`：日志/指标/追踪，横切关注点
- `store`：数据存储的接口抽象和通用实现（Wire ProviderSet 所在地）
- `transport`：HTTP/WS 协议相关工具和中间件
- `message`：异步消息传递和事件处理
- `engine`：CI/CD 流水线引擎的核心算法和数据结构
- `integration`：与外部系统（SCM、SSO、Agent）的集成
- `lifecycle`：应用生命周期管理

**替代方案 1**：保持扁平结构，仅用 README 说明分类 — 无法在代码层面强制职责边界，随着包数量继续增长会更加混乱。

**替代方案 2**：将部分 `pkg/` 包移入 `internal/` — `pkg/` 包被设计为多二进制（控制面 + Agent + CLI）共享，移入 `internal/` 需要拆分出多份副本，不合理。

**迁移策略**：`pkg/` 重组涉及全仓库 import 路径变更。采用 `goimports` + `sed` 批量替换，一个分组一个 PR，优先迁移依赖最少的 `foundation`。合并 `log` + `logger` 为统一的 `telemetry/log` 包。

### 决策 8：Wire 依赖注入重组

Wire ProviderSet 按层组织：

```go
// cmd/arcentra/wire.go
wire.Build(
    // 平台层
    platform.ProviderSet,
    // 基础设施层
    infra.ProviderSet,
    // 领域层（纯接口，无 provider）
    // 应用层
    usecase.ProviderSet,
    // 适配器层
    adapter.ProviderSet,
    // 引导
    bootstrap.NewApp,
)
```

每层内部再按子模块拆分 ProviderSet，避免单个巨大的 Build 调用。

## 风险 / 权衡

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| 大规模重命名导致合并冲突 | 高 | 分阶段迁移，每个上下文独立 PR；提前冻结 feature 分支 |
| 领域模型与持久化模型分离增加样板代码 | 中 | 第一阶段可暂用同一 struct（加 GORM tag），后续再拆；编写 codegen 工具辅助转换 |
| Wire 重组可能引入运行时 panic | 高 | 每次 Wire 变更后立即 `wire gen` + 编译验证；保持完整的集成测试 |
| 团队学习成本 | 中 | 编写架构 ADR 文档和示例；先迁移一个小上下文（如 agent）作为模板 |
| 性能影响（多层转换） | 低 | Go 的零分配转换和内联优化；仅在跨层边界进行模型转换 |
| 迁移期间两套代码并存 | 高 | 严格遵循"只新增不删除"的迁移原则，旧代码在新代码验证通过后再清理；使用 `Deprecated` 注释标记 |
| `pkg/` 重组导致全仓库 import 变更 | 高 | 使用 `gorename`/`goimports` + `sed` 批量替换；一个分组一个 PR；先迁移零依赖的 `foundation` 验证流程 |
| `log` + `logger` 合并可能有 API 不兼容 | 中 | 保留两者的公共 API，内部统一实现；通过 type alias 保持向后兼容 |
