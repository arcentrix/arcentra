## 1. 领域层骨架 — Agent 上下文（模板）

- [x] 1.1 在 `internal/domain/agent/model.go` 中定义 Agent、Storage 领域实体（纯 Go struct，无 GORM tag），从 `internal/control/model/model_agent.go` 和 `model_storage.go` 提取
- [x] 1.2 在 `internal/domain/agent/repository.go` 中定义 `IAgentRepository`、`IStorageRepository` 接口，方法签名仅使用领域模型类型
- [x] 1.3 在 `internal/domain/agent/service.go` 中定义 Agent 领域服务（纯领域逻辑，如状态转换规则）
- [x] 1.4 在 `internal/domain/agent/event.go` 中定义 `AgentRegistered`、`AgentStatusChanged` 等领域事件结构体
- [x] 1.5 在 `internal/domain/agent/consts.go` 中定义 Agent 相关常量和枚举
- [x] 1.6 验证 `internal/domain/agent/` 包可独立编译，无基础设施依赖

## 2. 领域层骨架 — 其他上下文

- [x] 2.1 在 `internal/domain/identity/` 下创建 model.go（User、Role、Menu、Team 等）、repository.go、service.go、event.go、consts.go
- [x] 2.2 在 `internal/domain/project/` 下创建 model.go（Project、Secret、GeneralSettings 等）、repository.go、service.go、event.go、consts.go
- [x] 2.3 在 `internal/domain/pipeline/` 下创建 model.go（Pipeline 定义态）、repository.go、service.go、event.go、consts.go
- [x] 2.4 在 `internal/domain/execution/` 下创建 model.go（StepRun、ExecutionRecord 等）、repository.go、service.go、event.go、consts.go
- [x] 2.5 验证所有 `internal/domain/` 包可独立编译且无跨上下文 import

## 3. 基础设施层 — Agent 上下文仓储实现（模板）

- [x] 3.1 在 `internal/infra/persistence/agent/` 下创建 GORM 持久化模型（带 tag），提供 `ToDomain()`/`FromDomain()` 转换方法
- [x] 3.2 实现 `AgentRepo` 结构体，满足 `domain/agent.IAgentRepository` 接口，从 `internal/control/repo/repo_agent.go` 迁移逻辑
- [x] 3.3 实现 `StorageRepo` 结构体，满足 `domain/agent.IStorageRepository` 接口
- [x] 3.4 添加 `var _ domain.IAgentRepository = (*AgentRepo)(nil)` 接口实现声明
- [x] 3.5 创建 `internal/infra/persistence/agent/provider.go`，提供 Wire ProviderSet（含 `wire.Bind`）
- [x] 3.6 验证编译通过并确保仓储接口完全实现

## 4. 基础设施层 — 其他上下文仓储实现

- [x] 4.1 实现 `internal/infra/persistence/identity/`：User、Role、Menu、Team 等仓储（从 `control/repo/repo_user.go`、`repo_role.go`、`repo_menu.go`、`repo_team.go` 迁移）
- [x] 4.2 实现 `internal/infra/persistence/project/`：Project、Secret、GeneralSettings 等仓储
- [x] 4.3 实现 `internal/infra/persistence/pipeline/`：Pipeline 仓储
- [x] 4.4 实现 `internal/infra/persistence/execution/`：StepRun 仓储
- [x] 4.5 创建 `internal/infra/persistence/provider.go`，聚合所有上下文的 Wire ProviderSet
- [x] 4.6 迁移缓存逻辑到 `internal/infra/cache/`，封装 Redis 缓存操作
- [x] 4.7 迁移 MQ 生产者到 `internal/infra/mq/kafka/`

## 5. 应用层 — Agent 上下文 Use Case（模板）

- [x] 5.1 在 `internal/case/agent/` 下创建 `register_agent.go`（RegisterAgentUseCase），从 `service.AgentService` 提取逻辑
- [x] 5.2 创建 `list_agents.go`、`get_agent.go`、`update_agent.go` 等 Use Case
- [x] 5.3 创建 `dto.go`，定义输入/输出 DTO
- [x] 5.4 创建 `upload.go`（UploadUseCase），从 `service.UploadService` 迁移
- [x] 5.5 创建 `provider.go`，提供 Wire ProviderSet
- [x] 5.6 验证 Use Case 仅依赖 `domain/` 接口，无 infra 直接引用

## 6. 应用层 — 其他上下文 Use Case

- [x] 6.1 在 `internal/case/identity/` 下创建 User、Role、Menu、Team、Identity 相关 Use Case（从 `service.UserService`、`IdentityService`、`TeamService`、`RoleService`、`MenuService` 迁移）
- [x] 6.2 在 `internal/case/project/` 下创建 Project、Secret、GeneralSettings、Scm 相关 Use Case
- [x] 6.3 在 `internal/case/pipeline/` 下创建 Pipeline 管理相关 Use Case
- [x] 6.4 在 `internal/case/execution/` 下创建 StepRun、LogAggregator 相关 Use Case
- [x] 6.5 为每个上下文创建 `dto.go` 和 `provider.go`

## 7. 适配器层 — HTTP

- [x] 7.1 创建 `internal/adapter/http/router.go`，设置 Fiber 应用和路由注册框架
- [x] 7.2 迁移中间件到 `internal/adapter/http/middleware/`（JWT、RBAC、CORS、错误处理、i18n），从 `internal/control/router/` 和 `pkg/http/middleware/` 整合
- [x] 7.3 迁移 Agent Router：`internal/control/router/router_agent.go` → `internal/adapter/http/router/router_agent.go`，改为依赖 `case/agent` Use Case
- [x] 7.4 迁移 Identity Router：`router_user.go`、`router_identity.go` → `internal/adapter/http/router/`
- [x] 7.5 迁移 Project Router：`router_project.go`、`router_secret.go`、`router_scm.go`、`router_general_settings.go` → `internal/adapter/http/router/`
- [x] 7.6 迁移 Pipeline Router：`router_pipeline.go` → `internal/adapter/http/router/`
- [x] 7.7 迁移 Execution Router：`router_ws.go`（日志流） → `internal/adapter/http/router/` 和 `internal/adapter/ws/`
- [x] 7.8 迁移 Team/Role/Storage/UserExt Router → `internal/adapter/http/router/`
- [x] 7.9 创建 `internal/adapter/http/provider.go`，提供 Wire ProviderSet

## 8. 适配器层 — gRPC / MQ / Cron

- [x] 8.1 将 `internal/pkg/grpc/grpc_server.go` 迁移到 `internal/adapter/grpc/server.go`
- [x] 8.2 将各 gRPC 服务实现（AgentServiceImpl、PipelineServiceImpl 等）迁移到 `internal/adapter/grpc/`，改为依赖应用层 Use Case
- [x] 8.3 创建 `internal/adapter/mq/` 消费者 Router
- [x] 8.4 将 `bootstrap.go` 中的 cron 任务（SCM 轮询）迁移到 `internal/adapter/cron/scm_poll.go`
- [x] 8.5 创建适配器层聚合 Wire ProviderSet

## 9. pkg/ 重组 — foundation + telemetry（零/低依赖优先）

- [x] 9.1 创建 `pkg/foundation/` 目录，将 `pkg/{env,id,net,num,safe,serde,time,util,version,retry,parallel,ringbuffer,loop,orderly,request}` 移入
- [x] 9.2 使用 `sed` / `goimports` 批量替换全仓库 import 路径（如 `pkg/safe` → `pkg/foundation/safe`）
- [x] 9.3 运行 `go build ./...` 验证 foundation 迁移无编译错误
- [x] 9.4 创建 `pkg/telemetry/` 目录，将 `pkg/{metrics,trace,pprof}` 移入
- [x] 9.5 合并 `pkg/log/` 和 `pkg/logger/` 为 `pkg/telemetry/log/`，保留两者的公共 API（通过 type alias 兼容）
- [x] 9.6 批量替换 `pkg/log` → `pkg/telemetry/log`、`pkg/logger` → `pkg/telemetry/log`、`pkg/metrics` → `pkg/telemetry/metrics`、`pkg/trace` → `pkg/telemetry/trace` 的 import
- [x] 9.7 运行 `go build ./...` 验证 telemetry 迁移无编译错误

## 10. pkg/ 重组 — store + transport + message

- [x] 10.1 创建 `pkg/store/`，将 `pkg/{database,cache}` 移入，批量替换 import
- [x] 10.2 创建 `pkg/transport/`，将 `pkg/{http,ws,auth,i18n}` 移入，批量替换 import
- [x] 10.3 创建 `pkg/message/`，将 `pkg/{mq,event,outbox,nova}` 移入，批量替换 import
- [x] 10.4 运行 `go build ./...` 验证三个分组迁移无编译错误

## 11. pkg/ 重组 — engine + integration + lifecycle

- [x] 11.1 创建 `pkg/engine/`，将 `pkg/{dag,dispatch,runner,sandbox,statemachine,taskqueue,logstream,record}` 移入，批量替换 import
- [x] 11.2 创建 `pkg/integration/`，将 `pkg/{scm,sso,plugin,plugins,agent}` 移入，批量替换 import
- [x] 11.3 创建 `pkg/lifecycle/`，将 `pkg/{cron,shutdown}` 移入，批量替换 import
- [x] 11.4 验证 `pkg/` 顶层无残留散包，仅存在 8 个分组目录
- [x] 11.5 运行 `go build ./...`、`go test ./...` 完整验证

## 12. Wire 重组与引导

- [x] 12.1 更新 `cmd/arcentra/wire.go`，按新分层组织 ProviderSet：platform → infra → case → adapter → bootstrap，使用新的 `pkg/` 路径
- [x] 12.2 运行 `wire gen ./cmd/arcentra` 重新生成 `wire_gen.go`
- [x] 12.3 更新 `internal/control/bootstrap/bootstrap.go` → 迁移到新位置或重构 `App` 结构体以使用新的层级
- [x] 12.4 更新 `cmd/arcentra-agent/wire.go`（适配新的 domain/infra/pkg 包路径）
- [x] 12.5 验证 `go build ./cmd/arcentra` 和 `go build ./cmd/arcentra-agent` 编译通过

## 13. 清理与验证

- [x] 13.1 对 `internal/control/model/`、`repo/`、`service/` 中已迁移的代码添加 `// Deprecated` 注释
- [x] 13.2 确认所有 Use Case 和 Router 已切换到新包路径，旧 `internal/control/` 无活跃引用
- [x] 13.3 删除 `internal/control/model/`、`internal/control/repo/`、`internal/control/service/`
- [x] 13.4 删除 `internal/control/router/`（已迁移到 `internal/adapter/http/`）
- [x] 13.5 删除 `internal/pkg/grpc/`（已迁移到 `internal/adapter/grpc/`）
- [x] 13.6 确认 `pkg/` 顶层无残留散包，旧的扁平包目录已全部清除
- [x] 13.7 运行 `go build ./...`、`golangci-lint run`、`go test ./...` 完整验证
- [x] 13.8 更新 `docs/ARCHITECTURE_ISSUES.md` 和相关文档，反映新架构（含 pkg 分组说明）
