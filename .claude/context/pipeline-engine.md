# 流水线执行引擎领域上下文

本文档供 Claude 在审查流水线执行 / 调度 / Job / Step 相关 PR 时复用。**不要在每次对话里重复解释这些概念。**

## 顶层抽象

| 概念 | 说明 | 主要代码位置 |
|------|------|--------------|
| Pipeline | 流水线定义（YAML / proto），含若干 Stage、若干 Job、若干 Step | `api/pipeline/v1/pipeline_spec.proto`、`internal/shared/pipeline/spec/` |
| PipelineRun | 一次流水线执行实例，拥有唯一 `runID`（UUID） | `internal/control/model/` + `internal/control/process/` |
| Stage | 流水线中的逻辑阶段，包含若干 Job | spec proto |
| Job | 调度单位，绑定到一个 Agent 执行 | `internal/shared/pipeline/task.go` |
| Step | Job 内的执行单元，由 Executor 执行（shell / plugin / http） | `internal/shared/executor/` |
| ExecutionContext | gin 风格的运行时对象，承载 Run / Job / Step 的状态机、DAG、参数、产物 | `internal/shared/pipeline/context.go` + `pipeline_execution_context.go` |
| Coordinator | 控制平面的运行协调器，每次 Run 一个 | `internal/control/process/coordinator.go` |
| Process | Run 入口，`Submit()` 创建 Coordinator | `internal/control/process/process.go` |
| Executor | 流水线执行循环，驱动 DAG 调度 | `internal/shared/pipeline/pipeline_executor.go` |
| Reconciler | DAG 调度器，决定下一批可执行 Job/Step | `internal/shared/pipeline/reconciler.go` |
| TaskFramework | Job 生命周期框架（prepare → create → start → queue → wait → backflow） | `internal/shared/pipeline/task_framework.go` |
| AgentManager | 选 Agent / 跟 Agent 通信的抽象 | `internal/shared/pipeline/agent_manager.go` |
| WorkspaceManager | Job 工作目录、产物挂载 | `internal/shared/pipeline/workspace_manager.go` |
| ApprovalManager | 人工审批节点 | `internal/shared/pipeline/approval_manager.go` |

## 执行链路（控制平面侧）

```
HTTP / Trigger → service.PipelineService.Run()
    → process.Process.Submit(spec, runID, params)
        → 落 t_pipeline_run（status=pending）
        → new Coordinator(runID)
        → Coordinator.Execute(ctx)
            ├── pipeline_execution_context.New(spec, run)
            ├── pipeline_executor.Run(execCtx)
            │       loop:
            │       ├── reconciler.NextRunnable()  // DAG 拓扑
            │       ├── 对每个 runnable Job：
            │       │     ├── task_framework.Prepare(job)
            │       │     ├── task_framework.Create(job) → 落 t_job_run
            │       │     ├── task_framework.Start(job) → 选 Agent
            │       │     ├── task_framework.Queue(job) → nova.Enqueue(TaskTypeJobRun)
            │       │     └── task_framework.Wait(job)  // 等 Agent 回调
            │       └── 收到回调 / 超时 → backflow → 更新状态 → 重新 reconcile
            ├── 全部完成 → 落终态、推 webhook、推 notify
            └── 清理：jobrun_store.Cleanup、artifact_cleanup
```

## Agent 侧执行链路

```
Kafka (nova topic) → agent.taskqueue.Worker
    ├── TaskTypeJobRun：
    │     ├── git clone（pkg/scm）
    │     ├── 下载上游产物（pkg/storage）
    │     ├── 依次执行所有 step（shared/executor.UnifiedExecutor）
    │     ├── 上传产物（pkg/storage）
    │     └── outbox 写状态事件（internal/agent/outbox）
    └── TaskTypeStepRun：
          └── 单步执行（容器内复用 Job 工作区时使用）

agent.outbox → 后台 goroutine → gRPC StreamJobStatus → 控制平面 → Coordinator.backflow
```

## Job / Step 状态机

Job 状态（`pkg/statemachine` 通用 FSM 承载）：

```
Pending → Queued → Running → Succeeded
                       ↓
                    Failed / Cancelled / Skipped / TimedOut
```

Step 状态镜像 Job，同时含 `WaitingApproval`、`Retrying`。

约束：

- 状态迁移必须经 FSM `Transition()`，不允许直接赋值
- FSM 钩子（OnEnter / OnExit）处理副作用（日志、metric、webhook、notify）
- Cancelled 必须能从任意非终态触发
- Retry 计数挂在 step 上，不在 Job 上聚合

## DAG 调度

- 使用 `pkg/dag` 通用 DAG：
  - 加载阶段：`dag.Build(nodes, edges)` 校验环、孤立节点
  - 调度阶段：`dag.NextReady(doneSet)` 返回下一批可执行节点
- Job 间依赖：spec 中 `needs: [...]`
- Step 间默认串行；Step 内 `parallel:` 走 `pkg/parallel`

## Executor 分发

| Executor | 用途 |
|----------|------|
| `ShellExecutor` | 本地 / 容器内执行 shell |
| `PluginExecutor` | 调插件（`pkg/plugin`），按 action 路由 |
| `HttpExecutor` | HTTP 出站 |
| `UnifiedExecutor` | 顶层分发：根据 step 类型路由到上面三个 |

所有 Executor 通过 `event_publisher` 向 Kafka 推 CloudEvents（`internal/shared/executor/publisher_kafka.go`）。

## 任务队列：Nova

- 代码：`pkg/nova/`
- 后端：Kafka（topic 按 `nova.topic_prefix` 分配）
- 支持：时间轮（延迟 / 定时）、优先级队列、批量聚合
- 任务类型：
  - `TaskTypeJobRun`
  - `TaskTypeStepRun`
  - `TaskTypeArtifactCleanup`
  - `TaskTypeAgentCancel`
- 入队：控制平面 `process.Coordinator` / `process.AgentCancel` / `process.ArtifactCleanup`
- 消费：Agent `taskqueue.Worker`

## 工作区与产物

- Workspace：每个 Job 一个隔离目录（`WorkspaceManager`），生命周期跟随 Job
- Cache：跨 Run 共享，按 `cacheKey` 命中
- Artifacts：Job 结束上传到 `pkg/storage`（S3 / MinIO / OSS / GCS / COS），下游 Job 通过 `needs.artifacts` 下载
- `process.ArtifactCleanup`：基于 Nova 定时任务清理过期产物

## 流水线规格

protobuf 定义：`api/pipeline/v1/pipeline_spec.proto`。
YAML / JSON 通过 `internal/shared/pipeline/spec/parse.go` 解析为 spec。
DSL 实现：`internal/shared/dsl/`，支持表达式 / 变量替换 / `${{ ... }}` 模板。
模板渲染：`internal/shared/pipeline/template/`。
触发器：`internal/shared/pipeline/trigger/`（手动 / 定时 / Webhook / 上游完成）。

## 控制平面与 Agent 的接口

| 用途 | 协议 | 定义 |
|------|------|------|
| Agent 注册 / 心跳 | gRPC | `api/agent/v1/*.proto` |
| 任务派发 | Kafka（nova） | `pkg/nova/` |
| 状态回流 | gRPC stream | `api/agent/v1/*.proto` |
| 日志流 | gRPC stream | `api/stream/v1/*.proto` + `pkg/logstream` |
| 步骤运行 | gRPC | `api/steprun/v1/*.proto` |
| API Gateway | HTTP / Fiber | `api/gateway/v1/*.proto`（OpenAPI） |

## 关键不变量

- **同一 `runID` 串行推进**：Coordinator 单 goroutine 推进状态机，DAG reconcile 不并发触发同一 Run
- **同一 `jobRunID` 幂等回调**：Agent 重连或重发状态事件不能改写已完成状态
- **outbox 至少一次投递**：Agent → 控制平面状态必须 outbox 持久化（`internal/agent/outbox/` 基于 WAL），允许重复但不丢
- **任务队列幂等消费**：Worker 处理任务时按 `taskID + attempt` 去重
- **Cancel 链路**：Run cancel → 所有未完成 Job 触发 `process.AgentCancel` → Nova 任务 → Agent 收到 → 中断 step
- **审计不在 hot path**：`process/audit.go` 通过 channel 异步写

## 设计目标

- **可恢复**：控制平面重启后从 DB 恢复活跃 Run，Coordinator 重建；Agent 重启后从 outbox 重放未发送事件
- **可观测**：所有状态迁移有日志 + metric + trace；CloudEvents 同步推 Kafka 供外部消费
- **可扩展**：插件机制（`pkg/plugin`）、Storage 抽象（`pkg/storage`）、通知通道（`internal/shared/notify`）均接口化
- **多租户**：组织 / 团队 / 项目 RBAC 在 `internal/control/authz/`

## 修改流水线引擎时需要同步检查

- 修改 `pipeline_spec.proto` → `make buf` + 更新 spec 解析 + 兼容老 spec
- 修改 Job / Step FSM → 同步更新 backflow / cancel / cleanup 路径
- 修改 Coordinator → 注意 Recovery 路径（控制平面重启）
- 修改 Nova 任务类型 → 同步 Worker dispatcher + 控制平面入队点
- 修改 Executor → 同步 CloudEvents schema + 下游消费者
