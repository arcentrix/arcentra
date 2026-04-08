# 流水线审批治理：Web UI、IM 与统一状态机

本文档落实 **approval-governance**：描述目标架构（**单一审批单、多通道呈现**），并与现有代码、事件类型对齐；**不要求**本文档即完整 OpenAPI。

---

## 1. 目标

- **一条流水线运行**在命中 DSL **approval 门禁**时，产生**唯一审批实例**（建议与 `PipelineRun` + 门禁锚点绑定，例如 `run_id` + `job_name` 或 `stage_name` + 单调 `gate_id`）。
- **Web UI** 与 **第三方 IM**（飞书/钉钉/企业微信/Slack 等）仅为 **通道**：展示上下文、收集「通过/拒绝」、可选评论与超时策略。
- **裁决**只写一次后端状态迁移，保证 **幂等**（重复回调、重试）与 **审计**（操作者、时间、来源 channel）。

---

## 2. DSL 与执行栈（现状）

- Schema / DSL：`approval` 块（`required`、`manual|auto`、可选 approval plugin）。见 [`schema.md`](./schema.md)、[`dsl.md`](./dsl.md)。
- **Job 级**：`JobRunner` 与 `TaskFramework` 均含 `handleApproval` 调用链；**Runner 与 Executor 双引擎**时须保证语义一致或文档标明差异（见 [`architecture_decisions.md`](./architecture_decisions.md)）。
- **事件**：`internal/shared/pipeline/approval_manager.go` 发射
  `arcentra.pipeline.approval.requested` / `approved` / `rejected`（常量见 `pkg/integration/plugin/cloudevent.go`）。

---

## 3. 控制面 API（建议拆分，待实现）

下列路由名为**建议**，与现有 Pause/Resume 协同设计；实际路径以 OpenAPI / `router_*` 为准。

| 能力 | 建议方向 |
|------|----------|
| 列出待审批 | `GET .../approvals?status=pending`（可按项目/pipeline/run 过滤） |
| 审批详情 | `GET .../approvals/:approvalId`（含 run 上下文、DSL 快照引用、过期时间） |
| Web 决策 | `POST .../approvals/:approvalId/approve` / `.../reject`（body：comment、操作者） |
| IM 回调 | `POST .../approvals/callbacks/:provider`（验签、映射 IM 用户 → 平台用户） |

**与运行态**：`PipelineRun` `PAUSED` / `RUNNING` 与「等待审批」应对齐一种主状态机，避免「暂停」与「等人批」两套并行语义（参见 [`api_design.md`](./api_design.md) 状态机）。

---

## 4. IM 集成要点

- 出站：订阅或使用 **`arcentra.pipeline.approval.requested`** 触发卡片/消息（见 [`plugin_events_mapping.md`](./plugin_events_mapping.md)）。
- 入站：回调 **同一** `approve`/`reject` 用例，**禁止** IM 模块直接改库绕过统一服务。
- 安全：回调 **签名校验**、**nonce**、**有效期**；操作映射需防止冒用机器人 token。

---

## 5. 与 `arcentra.task.approval.*` 的关系

- 文档草案 [`plugin_event_cloudevents_spec.md`](../plugin_event_cloudevents_spec.md) 使用 **task** 前缀；实现已用 **pipeline** 前缀。
- **短期**：IM/Web 订阅 **pipeline** 系列即可落地。
- **中期**：增加 **桥接发射** task 类型，或统一命名（需版本与兼容策略）。

---

## 6. 验收清单（迭代）

- [ ] 单 run 单门禁多通道决策只产生一条终态记录
- [ ] 重复回调不翻转已决状态
- [ ] 审计字段完整（who/when/channel）
- [ ] Runner 与 Executor 路径审批行为一致或有文档例外说明
