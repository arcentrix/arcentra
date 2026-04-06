## 为什么

当前控制面的核心业务逻辑集中在 `internal/control/` 下的经典分层结构（model → repo → service → router），所有领域实体和服务聚合在两个"上帝对象"（`Repositories` 和 `Services`）中，缺乏明确的领域边界。随着 Pipeline、Agent、Identity、Project 等业务域不断膨胀，代码耦合日益严重：Service 直接暴露 Repo、跨域引用无约束、基础设施与业务逻辑交织。同时，`pkg/` 公共库已膨胀到 ~50 个扁平包（env、id、safe、database、cache、log、metrics、dag、scm、sso、nova 等），缺乏分类组织，新包随意添加导致职责边界模糊、难以导航。分支 `refactor/domain-event-arch` 已经预留了 `internal/domain/`、`internal/adapter/`、`internal/case/`、`internal/engine/`、`internal/infra/`、`internal/platform/` 等空目录结构，但尚未有代码落地。现在是时候将现有的 `internal/control/` 逻辑按有界上下文拆分到 DDD + 分层架构中，同时重组 `pkg/` 为分层分组结构，消除技术债务，为后续领域事件和微服务拆分奠定基础。

## 变更内容

- 将 `internal/control/model/`、`repo/`、`service/` 中的业务逻辑按有界上下文拆分到 `internal/domain/{agent,execution,identity,pipeline,project}/` 下，每个上下文包含 `model`、`repo`（接口）、`service`（领域服务）、`event`（领域事件定义）、`consts`
- 引入 `internal/case/` 作为应用层（Use Case / Application Service），编排跨域操作，替代现有 `Services` 结构体中的跨域直接调用
- 将 `internal/control/router/`（HTTP）、`internal/pkg/grpc/`（gRPC）等适配器迁移到 `internal/adapter/{http,grpc,mq,ws,cron}/`，实现端口与适配器模式
- 将 `internal/control/platform/` 和 `pkg/` 中的基础设施实现迁移到 `internal/infra/{persistence,cache,mq,telemetry,scm,storage}/`，仓储接口在 domain 层定义、实现在 infra 层
- 将 `internal/engine/` 作为 Pipeline 引擎的核心子系统，包含 DAG、调度、DSL 解析、运行时等
- 将 `internal/platform/` 作为跨切面平台能力层（配置、日志、指标、安全、链路追踪）
- 将 `pkg/` 从 ~50 个扁平包重组为 8 个分组：`pkg/foundation/`（纯工具）、`pkg/telemetry/`（日志/指标/追踪）、`pkg/store/`（数据库/缓存）、`pkg/transport/`（HTTP/WS）、`pkg/message/`（MQ/事件/Outbox/Nova）、`pkg/engine/`（DAG/调度/运行器/沙箱）、`pkg/integration/`（SCM/SSO/插件）、`pkg/lifecycle/`（Cron/优雅关闭）
- **BREAKING**：`internal/control/` 和 `pkg/` 包路径变更，所有 import 路径需要更新；Wire 依赖图需要重新组织
- 消除 `Services` 结构体中直接暴露 Repo 的反模式（如 `ProjectMemberRepo`、`StepRunRepo`）

## 功能 (Capabilities)

### 新增功能
- `domain-bounded-contexts`: 按有界上下文（agent/execution/identity/pipeline/project）组织领域模型、仓储接口、领域服务和领域事件
- `application-use-cases`: 应用层 Use Case 编排，替代 Services 上帝对象，协调跨域操作
- `adapter-ports`: 端口与适配器层，将 HTTP/gRPC/MQ/WS/Cron 入站适配器与业务逻辑解耦
- `infrastructure-implementations`: 基础设施层实现，包含持久化（MySQL/SQLite）、缓存、消息队列、可观测性等
- `pkg-restructuring`: 将 pkg/ 从扁平结构重组为按职责分组的层级结构，减少顶层包数量，明确包的分类与边界
- `migration-strategy`: 增量迁移策略，定义从 internal/control 到新架构的分阶段迁移路径，确保系统始终可运行

### 修改功能

## 影响

- **代码**：`internal/control/` 下所有文件将被重组；`pkg/` 下 ~50 个包重新归类到 8 个分组目录；`cmd/arcentra/wire.go` 和 `cmd/arcentra-agent/wire.go` 的依赖注入图需要重构；`internal/pkg/` 部分代码需迁移
- **API**：外部 API 行为不变，但内部 Go 包路径（import path）全部变更（含 `pkg/` 路径）
- **依赖**：Wire 依赖注入的 ProviderSet 需要按新的分层重新组织；所有引用 `pkg/` 的代码需更新 import
- **系统**：Agent 端 `internal/agent/` 保持相对独立，但需要适配新的 domain、infra 和 pkg 包路径
- **构建**：`Makefile`、`wire_gen.go` 需要重新生成；CI pipeline 可能需要更新路径
