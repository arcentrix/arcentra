# Pipeline Execution Review

## Purpose

审查 PR 是否正确实现 / 修改流水线执行链路：Process → Coordinator → Executor → Reconciler → TaskFramework → Agent backflow。

## Context

完整领域上下文见 `.claude/context/pipeline-engine.md`。涉及代码：

- 控制平面：`internal/control/process/`
- 共享引擎：`internal/shared/pipeline/`
- 执行器：`internal/shared/executor/`
- 协议：`api/pipeline/v1/pipeline_spec.proto`、`api/agent/v1/*`
- 任务队列：`pkg/nova/`
- 状态机：`pkg/statemachine/`
- DAG：`pkg/dag/`

## Task

请按以下阶段检查代码：

### 阶段 1：Run 提交

- `service.PipelineService.Run()` 是否生成 `runID`（UUID）
- 是否落库 `t_pipeline_run`（status=pending）后再调用 `process.Process.Submit()`
- 是否处理并发提交去重（同一 pipeline + 触发条件下不重复入队）

### 阶段 2：Coordinator

- 每个 Run 一个 `Coordinator`，单 goroutine 推进
- `Coordinator.Execute(ctx)` 是否响应 `ctx.Done()` 并把 run 置为 Cancelled
- 是否在退出时清理 `jobrun_store` / 取消未完成任务
- 是否将致命错误落 `t_pipeline_run.error`

### 阶段 3：ExecutionContext

- `pipeline_execution_context.New(spec, run)` 是否完整加载 spec、参数、上下文变量
- 是否复用 `context_pool` 避免每 Run 大对象分配
- 状态机钩子（OnEnter / OnExit）是否注册了日志 / metric / webhook

### 阶段 4：DAG 调度

- `reconciler.NextRunnable()` 是否正确返回当前可执行 Job/Step
- 环检测：spec 校验阶段是否调用 `dag.Build()` 报错
- 跳过条件（`if:` 表达式）是否在 reconcile 时计算
- 全 Job 完成判定是否考虑 Skipped / Failed-but-allowed-failure

### 阶段 5：TaskFramework（Job 生命周期）

按 prepare → create → start → queue → wait → backflow 检查：

- `Prepare`：参数渲染、Workspace 分配、Cache 命中
- `Create`：落 `t_job_run`，生成 `jobRunID`
- `Start`：通过 `AgentManager` 选 Agent（按 labels / 容量 / 标签）
- `Queue`：`nova.Enqueue(TaskTypeJobRun, payload)`，payload 含 `runID` / `jobRunID` / `attempt`
- `Wait`：等待 backflow（gRPC stream）；超时是否走 `process.AgentCancel`
- `Backflow`：状态机迁移 + 触发 reconcile

### 阶段 6：Agent Worker

- `taskqueue/worker.go` 是否按任务类型分发（`TaskTypeJobRun` / `TaskTypeStepRun` / `TaskTypeAgentCancel`）
- Job 执行流：git clone → 下载产物 → 顺序执行 step → 上传产物 → outbox 写状态
- 异常路径：clone 失败 / 步骤失败 / OOM 都要走 outbox 上报终态
- 是否响应 cancel 信号中断当前 step

### 阶段 7：Outbox 与状态回流

- 状态事件必须先写 WAL 再发 gRPC
- 控制平面 ack 后才删 WAL
- Agent 重启 replay 未 ack 项
- 控制平面侧 `backflow` 必须幂等（按 `jobRunID + attempt + status` 去重）

### 阶段 8：终态与清理

- Run 终态：Succeeded / Failed / Cancelled / TimedOut
- 触发 webhook（`pkg/dispatch`）
- 触发通知（`internal/shared/notify`）
- 调度 artifact cleanup（`process.ArtifactCleanup` → Nova 延迟任务）
- 清理临时 jobrun_store 内存项

### 跨阶段

- 状态机迁移必须经 `pkg/statemachine.Transition()`，禁止直接赋值
- 所有 IO（DB / Kafka / gRPC）必须接收 `ctx`
- 所有 goroutine（reconcile loop、wait goroutine、backflow consumer）走 `safe.SafelyGo`
- Coordinator 持有的 `runID` 应贯穿日志（`log.With("runID", ...)`）和 trace span

## Constraints

必须遵守：

- 同一 `runID` 串行推进，禁止并发改写状态
- 状态回流必须幂等（按 `jobRunID + attempt`）
- Cancel 链路覆盖所有非终态 Job
- 不要在 hot loop 里打 `Info` 日志
- 修改 `pipeline_spec.proto` 后必须 `make buf` 并保证旧 spec 兼容

禁止：

- 在 service / process 直接使用 `gorm.DB`（必须走 `repo.IXxxRepository`）
- 用 `go func` 启动 reconcile / wait / backflow goroutine
- 跨 Run 共享状态（除 cache / workspace）
- 用 `panic` 处理 spec 错误（应返回 error 并落 run.error）

## Output Format

1. 总体结论（业务对齐 / 部分对齐 / 偏离）
2. 缺失或错误的阶段（按阶段 1-8 列出）
3. 状态机 / DAG / 幂等 风险点
4. 修改建议（含示例代码片段）
5. 测试覆盖建议（建议补充哪些 table-driven 用例 / 哪些集成测试场景）
6. 是否需要更新 `.claude/context/pipeline-engine.md`

## Checklist

- [ ] Coordinator 单 goroutine 推进，响应 cancel
- [ ] DAG 加载阶段做环检测
- [ ] TaskFramework 各阶段错误均有日志 + metric + 落库
- [ ] Outbox 写 WAL 早于 gRPC 调用
- [ ] backflow 幂等
- [ ] Cancel 覆盖所有非终态 Job
- [ ] 修改 proto / FSM 后兼容老 Run
- [ ] 新增任务类型同步更新 Worker dispatcher
