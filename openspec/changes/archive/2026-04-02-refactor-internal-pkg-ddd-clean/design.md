## 上下文

当前仓库已在 `refactor/domain-event-arch` 分支上建立了标准 DDD 分层：

| 层 | 路径 | 职责 |
|----|------|------|
| Domain | `internal/domain/{agent,identity,execution,pipeline,project}` | 模型、仓储接口、领域服务、领域事件 |
| Use Case | `internal/case/{agent,identity,execution,pipeline,project}` | 应用用例编排，仅依赖 domain |
| Infrastructure | `internal/infra/{persistence,storage,cache,mq}` | 仓储实现、外部系统适配 |
| Adapter | `internal/adapter/{http,grpc,ws,cron,mq}` | 入站驱动（HTTP/gRPC/WS/Cron/MQ） |
| Composition Root | `cmd/arcentra/wire.go`, `cmd/arcentra-agent/wire.go` | Wire 注入与启动 |

但 `internal/pkg` 中混杂了大量应属于上述四层的代码：

- **executor**：事件发布（Kafka/多路）属基础设施，`PipelineAdapter` 属适配器，且工厂直接依赖 `internal/control/config.AppConfig`
- **storage**：`IStorage` 定义 + S3/OSS/MinIO/GCS/COS 实现 + Wire Provider 混在一个包中，且依赖 `internal/domain/agent.IStorageRepository`
- **notify**：自建 `INotificationChannelRepo`/`INotificationTemplateRepo` 模型与接口，与 domain 层重复；Manager 编排逻辑属用例层；渠道实现属基础设施
- **pipeline**：引擎运行时（Context/Executor/Runner/Task/Reconciler）包含领域逻辑；builtin handler 属基础设施

## 目标 / 非目标

**目标：**

- 将 `internal/pkg` 中错位的代码迁移到正确的 DDD 层级，消除跨层依赖违规
- 保持与现有 `internal/domain`/`internal/case`/`internal/infra`/`internal/adapter` 结构模式一致（每个 bounded context 五文件模式）
- executor 核心接口与 Manager 解耦 `AppConfig`，改为接受已解析的 DTO/Config 结构体
- `internal/pkg` 仅保留无领域语义的纯工具代码
- 所有迁移保持编译通过和测试通过

**非目标：**

- 不重新设计 executor/pipeline 引擎的内部逻辑
- 不修改 gRPC/HTTP API 接口定义
- 不变更数据库 schema
- 不引入新的限界上下文（复用现有 agent/execution/pipeline 等）
- 不处理 `pkg/`（顶层公共库）的重构

## 决策

### 决策 1：executor 事件/消息基础设施移至 `internal/infra/messaging`

**选择**：新建 `internal/infra/messaging/` 包，承接 `EventPublisher`、`MultiPublisher`、`KafkaPublisher`、`KafkaTopicPublisher`、`KafkaLogPublisher`、`LogPublisher` 的实现。

**替代方案**：
- A) 移入 `internal/infra/mq/kafka/` — 但 EventPublisher 不限于 Kafka，且 LogPublisher 也非 MQ
- B) 保留在 `internal/pkg/executor` 并通过接口解耦 — 不解决包位置问题

**理由**：messaging 是跨领域的基础设施关注点，独立建包便于后续替换消息中间件。

### 决策 2：executor 核心接口与 Manager 保留在 `internal/pkg/executor`，移除 `AppConfig` 依赖

**选择**：`Executor` 接口、`Manager`、`ExecutionRequest/Result` 等保留在 `internal/pkg/executor` 作为共享内核。`NewExecutorManagerWithDefaultsAndEvents` 和 `NewEventPublisherFromConfig` 等接受 `AppConfig` 的工厂函数移至 `internal/adapter` 或 `cmd/` 层的组装代码。

**替代方案**：
- A) 将 executor 接口移入 `internal/domain/execution` — 但 executor 是跨多个 bounded context 的运行时概念，放 domain 下某个 context 不自然
- B) 定义新的 `internal/domain/executor` context — 过度拆分

**理由**：executor 作为"共享内核"（Shared Kernel）的定位合理，关键是去除对 control/config 的直接依赖。通过将配置转换逻辑提取到组合根，executor 包只依赖自身类型和标准库。

### 决策 3：storage 接口移入 domain，实现移入 `internal/infra/storage`

**选择**：
- `IStorage` 接口 → `internal/domain/agent/repository.go`（已有 `IStorageRepository`，新增 `IStorage` 或重命名）
- S3/OSS/MinIO/GCS/COS 实现 → `internal/infra/storage/`（已有 `adapter.go`，扩展为完整包）
- `DbProvider`（依赖 `IStorageRepository` 的运行时切换）→ `internal/infra/storage/`
- Wire ProviderSet → `internal/infra/storage/provider.go`

**替代方案**：
- A) 新建 `internal/domain/storage` context — 但存储配置已在 agent context 中，独立拆会导致跨 context 仓储
- B) 保持在 `internal/pkg` 但抽接口 — 不解决依赖方向问题

**理由**：`IStorage` 是领域能力的端口，实现属基础设施。与现有 `internal/infra/storage/adapter.go` 自然合并。

### 决策 4：notify 按三层拆解

**选择**：
- 领域模型/接口 → 新建 `internal/domain/notification/`（model.go, repository.go, service.go, event.go, consts.go）
- Manager 编排 → 新建 `internal/case/notification/`
- 渠道实现 → 新建 `internal/infra/notification/`（Slack/Telegram/DingTalk/Discord/Webhook/Email/Feishu/Lark/WeCom）
- 模板引擎保留为共享工具 `internal/pkg/notify/template`（Engine/predefined 无领域耦合部分）或移入 `internal/infra/notification/template`

**替代方案**：
- A) 保持在 `internal/pkg` 但对齐接口到 domain — 半重构，位置仍混乱
- B) 整体移入 `internal/infra/notification` — 忽略了 Manager 的用例层属性

**理由**：通知是独立的限界上下文（有自己的仓储、模型、编排逻辑），应按标准五文件模式组织。

### 决策 5：pipeline 引擎核心类型定位为"共享内核"

**选择**：pipeline 运行时（Context/ContextPool/Executor/Runner/StepRunner/Task/TaskFramework/Reconciler）保留在 `internal/pkg/pipeline` 作为共享内核。`builtin/` action handler 移至 `internal/infra/pipeline/builtin`。`spec/`、`validation/`、`interceptor/` 保留在 `internal/pkg/pipeline/` 下。

**替代方案**：
- A) 全部移入 `internal/domain/execution` — 引擎运行时太重，与纯领域模型混在一起不合适
- B) 全部移入 `internal/domain/pipeline` — 同上
- C) 创建 `internal/engine/pipeline` — 新增层级，增加认知负担

**理由**：pipeline 引擎类似 workflow engine，是跨 bounded context 的基础设施框架。保留为共享内核最务实，但去除 builtin 中对外部系统的直接依赖。

### 决策 6：`internal/pkg` 保留为纯工具层

**迁移后 `internal/pkg` 保留的包：**
- `convert/` — JSON/YAML 转换
- `dsl/` — DSL 解析（依赖 pipeline/spec、validation）
- `executor/` — 核心接口与 Manager（已去 AppConfig 依赖）
- `pipeline/` — 引擎核心 + spec + validation + interceptor
- `prefixtree/` — 前缀树
- `sse/` — SSE Hub
- `gateway/` — 网关工具

## 风险 / 权衡

| 风险 | 缓解措施 |
|------|----------|
| 大量 import 路径变更导致编译错误 | 按包逐个迁移，每迁一个包确保 `go build ./...` 通过 |
| notify 拆到新 bounded context 可能与现有 identity/project context 有交叉 | notification 作为独立 context 只持有通知相关仓储；发送触发由其它 context 通过领域事件驱动 |
| executor 去除 AppConfig 依赖后，组合根需要更多配置转换代码 | 在 `cmd/` 或 `internal/control/bootstrap` 中集中处理，复杂度可控 |
| pipeline builtin 移到 infra 后，与 pipeline 核心跨包调用增多 | builtin 通过接口注册到引擎，不直接耦合 |
| 迁移过程中分支冲突风险 | 建议在当前 `refactor/domain-event-arch` 分支上连续完成，避免长期并行 |
