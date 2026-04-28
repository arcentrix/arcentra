# CLAUDE.md

本文件为 Claude Code（claude.ai/code）在此仓库中工作时提供指导。

## 构建与开发命令

| 命令 | 说明 |
|---|---|
| `make deps` | 整理并验证 Go 模块依赖 |
| `make build` | 代码生成（buf + wire）+ `go build`（带 ldflags），覆盖所有 cmd/* 目标 |
| `make run` | 代码生成 + `go run` |
| `make test` | `go test -race -count=1 ./...` |
| `make lint` | 安装并运行 `golangci-lint` v2（配置见 `.golangci.yml`） |
| `make fmt-check` | 使用 `gofmt` 检查格式，跳过生成文件 |
| `make codegen` | 运行 buf protobuf 生成 + wire DI 代码生成（所有目标） |
| `make buf` | 仅运行 buf proto 生成 |
| `make wire` | 仅运行 wire DI 代码生成 |
| `make staticcheck` | 运行 `staticcheck ./...` |
| `make build-target` | 发布构建（跳过代码生成，启用 trimpath + strip） |

运行单个测试：`go test -race -run TestName ./path/to/package`

## 架构概览

Arcentra 是一个云原生 CI/CD 控制平面，由 **两个进程** 组成 —— 中央控制平面（`cmd/arcentra`）和分布式 Agent（`cmd/arcentra-agent`）。两者均使用 Google Wire 进行编译时依赖注入（`wire.go` + 生成的 `wire_gen.go`）。

### 控制平面（`internal/control/`）

分层架构，由 `internal/control/bootstrap/bootstrap.go` 引导启动：

- **`model/`** — 映射到 MySQL 表的 GORM 结构体。每个结构体嵌入 `BaseModel`（ID、CreatedAt、UpdatedAt）。表名前缀为 `t_xxx`，业务 ID 为 UUID 字符串（`xxx_id`），使用 `is_enabled`/`is_deleted` 软删除，排序规则为 `utf8mb4_0900_ai_ci`。
- **`repo/`** — 封装 GORM 的 Repository 接口（`IPipelineRepository`、`IAgentRepository` 等）。聚合在 `repo.Repositories` 中。Service 层严禁直接使用 GORM。
- **`service/`** — 业务逻辑服务，聚合在 `service.Services` 中。`IPipelineEngine` 接口桥接到流水线执行引擎。
- **`router/`** — Fiber v2 HTTP 路由，位于 `/api/v1/` 下。中间件链：RealIP → RequestID → Trace → Recover → Metrics → CORS → UnifiedResponse → AccessLog → i18n → pprof。按路由组应用 JWT 鉴权中间件。
- **`process/`** — 流水线编排引擎。`Process.Submit()` 为每次运行创建 `Coordinator`，构建带 DAG 调度的 `ExecutionContext`。`Coordinator.Execute()` 通过状态机推进运行状态，并通过 Nova 任务队列将 Agent 绑定作业入队到 Kafka。

### Agent（`internal/agent/`）

- 由 `internal/agent/bootstrap/bootstrap.go` 引导启动。通过 gRPC 向控制平面注册，发送心跳，并从 Kafka 消费任务。
- **`taskqueue/worker.go`** — Nova 任务队列消费者。分发 `TaskTypeJobRun`（克隆 → 下载产物 → 执行所有步骤）和 `TaskTypeStepRun`（单步执行）。通过 gRPC 上报状态。
- **`outbox/`** — 基于 WAL 的发件箱模式，用于 Agent 到控制平面的可靠事件投递。

### 共享代码（`internal/shared/`）

- **`pipeline/`** — 核心流水线执行引擎：`Context`（gin 风格的运行时对象，含状态机）、`Executor`（DAG 协调循环）、`Task`/`TaskFramework`（作业生命周期：prepare → create → start → queue → wait → backflow）、`Reconciler`（基于 DAG 的调度）。流水线规格通过 protobuf（`api/pipeline/v1/pipeline_spec.proto`）定义。
- **`executor/`** — 步骤执行：`ShellExecutor`、`PluginExecutor`、`UnifiedExecutor`（本地 vs 远程分发）。将 CloudEvents 发布到 Kafka。
- **`storage/`** — 多云对象存储（S3、MinIO、OSS、GCS、COS），统一使用 `IStorage` 接口。
- **`notify/`** — 11 通道通知系统（邮件、Slack、钉钉、企微、飞书/Lark、Webhook 等），支持 Go 模板渲染。
- **`grpc/`** — gRPC 服务端/客户端封装，带 tracing、鉴权、日志、恢复拦截器。

### 关键库（`pkg/`）

- **`pkg/nova/`** — 基于 Kafka 的任务队列，支持时间轮、优先级队列、延迟调度和批量聚合。
- **`pkg/plugin/`** — 插件框架，包含生命周期管理、12 种插件类型、Action 路由和 TOML 配置热加载。
- **`pkg/cron/`** — 分布式 Cron 调度器，基于 Redis 去重（`SET NX`）。
- **`pkg/dag/`** — 通用 DAG，支持环检测和拓扑排序。
- **`pkg/statemachine/`** — 通用线程安全状态机，支持钩子和历史记录。

## 编码规范

- **所有导出标识符必须有 GoDoc 的中文注释，禁止中文的句号（。）结尾。** `init` 之外禁止 `panic`；始终返回 `error`。
- **所有 IO/RPC/DB 调用必须传入 `context.Context`。** 传递而非存储在结构体上。
- **使用 `sonic` 处理 JSON**，而非 `encoding/json`。JSON 字段使用驼峰命名。
- **Repository 接口以 `I` 为前缀**（如 `IPipelineRepository`）。暴露领域查询方法（按业务 ID），禁止按数据库主键暴露原始查询。
- **所有 goroutine 通过 `safe.SafelyGo` 启动** —— 禁止直接使用 `go` 或 `go func`。
- **禁止手写 SQL** —— 使用 GORM 并明确指定查询字段。禁止 `SELECT *`。
- **`pkg/` 包不得直接访问数据库。** 在 `pkg/` 中定义接口，在 `internal/control/repo/` 中实现。
- **ID 使用 `ID`**（非 `Id`）。业务标识符为 UUID 字符串，存储在 `xxx_id` 列中。
- **提交信息格式**：`[type] 描述`，类型为 `feat`、`fix`、`refactor`、`chore`、`docs`、`style`、`test`。
- 每个包在 `provider.go` 中定义 Wire `ProviderSet` 变量用于依赖注入。
