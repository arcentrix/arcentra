# Pipeline 文档索引

本目录集中存放 **流水线（Pipeline）** 相关说明：DSL、Schema、HTTP/API 设计、执行栈盘点、编排决策、工作负载语义、审批治理及事件映射。

| 文档 | 说明 |
|------|------|
| [dsl.md](./dsl.md) | Pipeline DSL 结构规范 |
| [schema.md](./schema.md) | Pipeline YAML Schema 草案 |
| [http_api.md](./http_api.md) | Pipeline HTTP API |
| [api_design.md](./api_design.md) | Pipeline API 设计（MVP，与 gRPC 语义对齐） |
| [execution_inventory.md](./execution_inventory.md) | 执行栈组件、Agent/队列路径、触发链缺口 |
| [architecture_decisions.md](./architecture_decisions.md) | Runner vs Executor、AgentManager vs TaskQueue 决策备忘 |
| [workload_semantics.md](./workload_semantics.md) | 常规 CI、大数据、AI 训练/推理语义 |
| [approval_governance.md](./approval_governance.md) | Web UI / IM 审批与统一状态机 |
| [plugin_events_mapping.md](./plugin_events_mapping.md) | CloudEvents 与流水线发射点对照 |

**仓库内其它相关文档**（仍在 `docs/` 根目录）：

- [`plugin_event_cloudevents_spec.md`](../plugin_event_cloudevents_spec.md) — CloudEvents 事件草案
- [`VERSION_MANAGEMENT.md`](../VERSION_MANAGEMENT.md) — 制品版本规则（与 DSL `uses` 版本不同维度）
