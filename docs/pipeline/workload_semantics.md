# Pipeline 工作负载语义：常规 CI、大数据与 AI

本文档落实 **workload-semantics**：在架构说明层区分三类工作负载在**超时、资源、数据局部性、运行态**上的常见需求，便于 DSL、Agent 选择与事件模型扩展时保持一致。

**关联**：执行栈组件见 [`execution_inventory.md`](./execution_inventory.md)；编排选择见 [`architecture_decisions.md`](./architecture_decisions.md)。

---

## 1. 常规 CI/CD

| 维度 | 典型语义 |
|------|----------|
| 时长 | 分钟级；`job.timeout` / `step.timeout` 以短–中为主。 |
| 资源 | CPU/内存为主；可选容器隔离。 |
| 依赖 | artifact、镜像层缓存；job 间常无硬 DAG，或 `concurrency` 互斥即可。 |
| 运行态 | `StepRun` 快速完成；日志流式即可。 |
| 事件 | `arcentra.job.*` / `arcentra.step.*` 足够支撑 UI。 |

---

## 2. 大数据任务

| 维度 | 典型语义 |
|------|----------|
| 时长 | 小时–天级；需 **可恢复**、**进度**（`progress` 类事件，见映射表 TBD）。 |
| 资源 | 数据局部性（同机房/同集群）、IO 带宽、队列位。 |
| 依赖 | 强依赖 **DAG**（多阶段 ETL）；倾向 **Executor + `dependsOn`**。 |
| 运行态 | 长时间 `RUNNING`；避免控制面阻塞；适合 **队列 + Agent** 或显式心跳。 |
| 事件 | 除完成态外，需要 **progress / artifact**（输出路径、统计量）。 |

---

## 3. AI 训练与推理

| 维度 | 典型语义 |
|------|----------|
| 时长 | 训练：长；推理：毫秒–分钟级（批量推理可很长）。 |
| 资源 | **GPU**、显存、多机；`agentSelector` / 标签应对齐「GPU 型号、驱动、池」。 |
| 依赖 | 数据准备 → 训练 → 评估 → 部署；DAG + 审批门禁常见。 |
| 运行态 | 训练需 **checkpoint、指标曲线**；与 `artifact` / 对象存储 URI 关联。 |
| 事件 | 与大数据类似，强调 **progress**；发布前常配合 **审批**（见 [`approval_governance.md`](./approval_governance.md)）。 |

---

## 4. DSL / 平台约定（建议）

- **`dependsOn`**：用于表达 job 级 DAG（与 `api/pipeline/v1` proto 字段一致）；YAML 书写见 [`schema.md`](./schema.md)。
- **`runOnAgent` + `agentSelector`**：表达资源与池；与 GitLab Runner tags 思路一致。
- **超时**：长任务应允许 **仅限制 step** 或 **可配置无上限 + 人工取消**，避免误杀训练。
- **版本**：平台发行版本（[`VERSION_MANAGEMENT.md`](../VERSION_MANAGEMENT.md)）与 step `uses` 插件版本 **不同维度**，文档与 UI 应分开展示。

---

## 5. 与事件模型的对齐

- 长任务所需 **`arcentra.task.progress` / `artifact` / `log`** 目前在流水线核心路径多为 **TBD**（见 [`plugin_events_mapping.md`](./plugin_events_mapping.md)）。
- 补齐时优先：**Agent 侧上报 → 控制面持久化 → CloudEvents 出口**，避免仅在内存日志中不可观测。
