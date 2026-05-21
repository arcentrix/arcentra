# Proto / gRPC / Kafka Compatibility Review

## Purpose

审查 proto 修改、新增 / 修改 gRPC 服务、新增 Kafka topic 或 CloudEvents 类型时的兼容性与下游契约。

## Context

- Proto 根目录：`api/`
  - `api/agent/v1/` — Agent ↔ 控制平面（注册 / 心跳 / 状态回流 / 步骤运行）
  - `api/pipeline/v1/` — 流水线规格 + 控制平面 API
  - `api/steprun/v1/` — Step 执行协议
  - `api/stream/v1/` — 日志流 / 事件流
  - `api/gateway/v1/` — 对外网关 API（OpenAPI）
- 生成代码：`api/**/*.pb.go`、`api/**/*_grpc.pb.go`（**不可手编辑**，执行 `make buf`）
- Buf 配置：`api/buf.yaml`、`api/buf.gen.yaml`
- gRPC 服务实现：控制平面在 `internal/control/router/`，Agent 在 `internal/agent/router/` 或 service 层
- Kafka publisher：`internal/shared/executor/publisher_kafka.go`（CloudEvents）
- 任务队列（Kafka）：`pkg/nova/` 任务 payload 用 proto 序列化

## Task

### 1. Proto 字段修改

- 新增字段：是否使用新的 tag number，不复用历史 tag？
- 删除字段：是否标记为 `reserved <tag>` 和 `reserved "<name>"`？
- 修改类型：是否会破坏 wire 兼容性？（`int32` ↔ `int64` 不兼容、`string` ↔ `bytes` 危险）
- enum：新增值是否在末尾追加？是否保留 0 值为 `XXX_UNSPECIFIED`？
- oneof：tag number 是否独立、是否与现有字段冲突？
- map：是否考虑过 key 类型限制？
- `repeated` 字段：是否避免修改 packed 表示？

### 2. gRPC 服务

- 新增 RPC：是否更新 `internal/shared/grpc/` 客户端封装？
- 流式 RPC：是否处理客户端 / 服务端单向断开、心跳超时、`io.EOF`？
- 错误码：是否使用 `status.Error(codes.NotFound / InvalidArgument / Internal / ...)` 而非裸 error？
- 大消息：是否需要调整 `MaxRecvMsgSize` / `MaxSendMsgSize`？
- 鉴权：是否通过 `internal/shared/grpc/` 的 auth 拦截器？
- 拦截器顺序：tracing → auth → recover → log → metrics 是否保持？
- Agent 调控制平面：是否带 `agentID` / `trace-id` 元数据？

### 3. Kafka topic / 消息

- 新增 topic：是否在 `conf.d/` 默认配置中登记？是否有 `topic_prefix` 命名规范（`arcentra.<env>.<domain>`）？
- 消息 key：是否按 `runID` / `jobRunID` / `agentID` 选 key，保证同分区有序？
- 消息体：是否使用 proto 二进制（不要 JSON）？
- 消费者：是否处理重复消费（幂等去重）？
- 兼容性：旧消费者能否优雅跳过未知字段？

### 4. CloudEvents（`internal/shared/executor/publisher_kafka.go`）

- 新增事件类型：常量是否落在统一位置（如 `executor/event_*.go`）？
- type 命名：是否 `arcentra.<domain>.<action>`（snake_case 或 dot-case 统一）？
- source / subject / id 字段是否填充正确？
- 是否带 traceparent / runID / jobRunID 在 extension？
- 下游消费者契约更新没有？

### 5. Nova 任务 payload

- 新增 `TaskType` 常量：是否同步在 Agent `taskqueue/worker.go` dispatcher 中处理？
- payload proto 是否定义在 `api/`？
- 是否带 `attempt` / `runID` / `jobRunID` 用于幂等？
- 优先级 / 延迟 / 批量参数是否合理？

### 6. 文档与下游服务

- 修改 proto 是否同步更新 `.claude/context/pipeline-engine.md` 或 `control-plane-agent.md`？
- PR 描述是否列出影响的服务（控制平面 / Agent / 网关 / 外部消费者）？
- 是否需要发版 / 灰度策略？

## Constraints

必须遵守：

- 永远不重用 / 删除 proto tag number，删除字段必须 `reserved`
- 新增字段在末尾追加 tag number，不在中间插入
- enum 0 值固定为 `*_UNSPECIFIED`
- gRPC 错误必须用 `status.Error(codes.*, ...)`
- Kafka 消息体用 proto，不用 JSON
- 修改 `.proto` 后必须 `make buf` 并提交生成代码
- 生成代码（`*.pb.go` / `*_grpc.pb.go`）不允许手编辑

禁止：

- 修改已有字段的 tag number
- 修改已有字段的类型
- 在 oneof 之外把字段改为 oneof（破坏 wire）
- 在 hot path 反复 marshal / unmarshal 大 proto
- 让下游消费者必须升级才能解码（除非有明确灰度方案）

## Output Format

1. 总体结论（兼容 / 需要灰度 / 破坏性）
2. 字段级 wire 兼容性问题（按文件:行号）
3. gRPC 服务 / 流式 / 错误码问题
4. Kafka / CloudEvents 契约问题
5. 下游影响列表
6. 推荐的灰度 / 版本策略
7. 是否需要更新 `.claude/context/*.md`

## Checklist

- [ ] tag number 仅追加，未重用
- [ ] 删除字段已 `reserved`
- [ ] enum 0 值为 `*_UNSPECIFIED`
- [ ] `make buf` 已执行
- [ ] 生成代码未手编辑
- [ ] gRPC 错误使用 `status.Error`
- [ ] Kafka topic 命名 / 分区 key 合理
- [ ] CloudEvents type / source / id 完整
- [ ] Nova 新 TaskType 已加 Worker dispatcher
- [ ] 下游消费者契约已沟通
