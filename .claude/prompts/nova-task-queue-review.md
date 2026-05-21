# Nova Task Queue Review

## Purpose

审查 PR 是否正确使用 / 修改 Nova 任务队列（Kafka 后端 + 时间轮 + 优先级 + 批量聚合 + 延迟调度）。

## Context

- Nova 实现：`pkg/nova/`
- 底层抽象：`pkg/taskqueue/`、`pkg/mq/`
- 控制平面入队点：`internal/control/process/`（Coordinator、AgentCancel、ArtifactCleanup）
- Agent 消费：`internal/agent/taskqueue/worker.go`
- 配置：`conf.d/`（nova 段）

参考流程见 `.claude/context/pipeline-engine.md`「任务队列：Nova」与「Agent 侧执行链路」。

## Task

### 1. 任务类型注册

- 新增 `TaskType` 是否落在统一位置（`pkg/nova/types.go` 或对应 dispatcher）？
- payload 是否定义为 proto 消息（在 `api/` 下）？
- Worker dispatcher 是否同步加分支处理（`internal/agent/taskqueue/worker.go`）？
- 控制平面是否新增对应 `process.Xxx` 入队方法？

### 2. 入队 API

- 是否提供合理的 `nova.Enqueue(ctx, TaskType, payload, opts...)` 调用？
- 优先级 `WithPriority(...)` 是否合理（关键任务 high，清理任务 low）？
- 延迟 `WithDelay(...)` / `WithRunAt(...)` 是否合理？
- 批量 `WithBatch(...)` 是否合理（同 key 聚合，避免雪崩）？
- `TaskID` / `Idempotency Key` 是否带，避免重复消费？
- 入队上下文是否传 `ctx`，便于 trace 透传？

### 3. 调度

- 时间轮：延迟 / 定时任务是否正确进入时间轮，到期出队？
- 优先级队列：高优先级是否能抢占低优先级（在同分区内）？
- 严格优先级（StrictPriority）配置：是否避免低优先级饿死（按需打开公平调度）？
- 批量聚合：聚合窗口 / 最大批大小是否合理？

### 4. 分区与有序性

- Kafka 消息 key 是否按 `runID` / `jobRunID` 选，保证同 Run 内有序？
- 跨分区不保证全局有序，业务逻辑是否容忍？
- 重新分区（topic 扩缩容）是否会破坏 key→partition 映射？

### 5. 消费者

- Worker 是否按 `TaskType` dispatch，未知类型走默认（ack 但告警）？
- 是否记录 `attempt` 并实现幂等（按 `taskID + attempt` 去重）？
- 失败重试策略：是否区分 transient 错误（重试）与 permanent 错误（不重试）？
- 最大重试次数到达后是否走死信 topic 或落 `t_xxx_failed`？
- 长任务（Job 执行）是否定期续约 / 心跳，避免 broker 误判断开？
- 是否响应 `ctx.Done()`（来自 `TaskTypeAgentCancel`）中断当前任务？

### 6. 资源与并发

- Worker goroutine 是否走 `safe.SafelyGo`？
- 并发度 `concurrency` 是否可配（按 Agent 容量）？
- 是否在 Worker 退出时优雅 drain 当前任务（不要丢任务）？
- 是否避免在 Worker 内创建大量短 goroutine？

### 7. 观测

- 入队 / 出队 / 失败 / 重试 是否有 metric（counter / histogram）？
- 是否有 trace 透传（traceparent 在消息 header）？
- 日志是否含 `taskID` / `taskType` / `attempt` / `runID` / `jobRunID`？
- 死信 / 重试 N 次失败 是否触发告警？

### 8. 配置与默认值

- Nova 配置项是否在 `conf.d/` 提供合理默认（topic_prefix / partitions / replicas / concurrency / batch_size / poll_interval）？
- 是否区分 dev / staging / prod 不同默认？

## Constraints

必须遵守：

- 同一 Run 内任务用相同 Kafka key，保证有序
- 所有任务必须幂等
- 消费者必须响应 ctx cancel
- Worker goroutine 一律 `safe.SafelyGo`
- 任务 payload 用 proto，不用 JSON
- 入队 / 消费日志含 `taskID` / `taskType` / `attempt`

禁止：

- 用裸 `go func` 启动 Worker 子 goroutine
- 在 Worker 内同步阻塞调 DB 大查询（应该走 service / repo）
- 在 hot path（步骤执行循环）反复入队大量小任务
- 修改 `TaskType` 常量值（破坏向后兼容）
- 任意删除已有 `TaskType`（必须保留兼容 + 灰度迁移）

## Output Format

1. 总体结论
2. 任务定义 / payload 兼容性问题
3. 入队 API 用法问题（优先级 / 延迟 / 批量 / 幂等）
4. 消费者实现问题（dispatch / 重试 / 续约 / cancel）
5. 分区 / 有序性 风险
6. 观测性缺失
7. 配置默认值建议
8. 推荐 patch / 代码示例

## Checklist

- [ ] 新 `TaskType` 已加入 Worker dispatcher
- [ ] payload 是 proto
- [ ] 入队带 `taskID` / 业务 key 用于幂等
- [ ] 同 Run 任务用一致 Kafka key
- [ ] Worker 响应 ctx cancel
- [ ] 重试 / 死信策略明确
- [ ] metric + trace + 日志含 `taskID` / `attempt`
- [ ] 配置项有默认值
