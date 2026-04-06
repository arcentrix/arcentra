# CloudEvents / 插件事件与流水线发射点对照

本文档落实 **cloudevents-map**：将 [`plugin_event_cloudevents_spec.md`](../plugin_event_cloudevents_spec.md) 中的 **`arcentra.task.*`** 与仓库内 **已定义常量**、**已发射事件** 对齐；缺口标 **TBD**。

常量定义：`pkg/integration/plugin/cloudevent.go`。

---

## 1. `arcentra.task.*`（草案文档主生命周期）

| type（文档/常量） | 代码常量 | 当前流水线发射点 |
|-------------------|----------|------------------|
| arcentra.task.submitted | `EventTypeTaskSubmitted` | **TBD**（未在 `internal/shared/pipeline` 检索到） |
| arcentra.task.scheduled | `EventTypeTaskScheduled` | **TBD** |
| arcentra.task.started | `EventTypeTaskStarted` | **TBD**（与 `arcentra.job.started` 并存时需界定粒度） |
| arcentra.task.progress | `EventTypeTaskProgress` | **TBD** |
| arcentra.task.log | `EventTypeTaskLog` | **TBD** |
| arcentra.task.artifact | `EventTypeTaskArtifact` | **TBD** |
| arcentra.task.succeeded | `EventTypeTaskSucceeded` | **TBD** |
| arcentra.task.failed | `EventTypeTaskFailed` | **TBD** |
| arcentra.task.finished | `EventTypeTaskFinished` | **TBD** |
| arcentra.task.approval.requested | `EventTypeTaskApprovalRequested` | **TBD**（流水线侧见 `arcentra.pipeline.approval.*`） |
| arcentra.task.approval.approved | `EventTypeTaskApprovalApproved` | **TBD** |
| arcentra.task.approval.rejected | `EventTypeTaskApprovalRejected` | **TBD** |
| arcentra.task.rollback.* | `EventTypeTaskRollback*` | **TBD** |
| arcentra.task.retry.* | `EventTypeTaskRetry*` | **TBD** |

**说明**：草案以 **task** 为统一粒度；实现中已大量使用 **`arcentra.pipeline.*` / `arcentra.job.*` / `arcentra.step.*`**。后续可选：在发射层做 **1:1 桥接**（pipeline/job/step → task CloudEvent），或统一对外只暴露 task 命名空间。

---

## 2. `arcentra.pipeline.*`（已实现发射）

| type | 代码常量 | 发射位置 |
|------|----------|----------|
| arcentra.pipeline.started | `EventTypePipelineStarted` | `pipeline_executor.go` `Execute` |
| arcentra.pipeline.completed | `EventTypePipelineCompleted` | 同上 |
| arcentra.pipeline.failed | `EventTypePipelineFailed` | 同上 |
| arcentra.pipeline.cancelled | `EventTypePipelineCancelled` | **TBD**（需与 Stop/Cancel 路径对齐） |
| arcentra.pipeline.approval.requested | `EventTypePipelineApprovalRequested` | `approval_manager.go` |
| arcentra.pipeline.approval.approved | `EventTypePipelineApprovalApproved` | `approval_manager.go` |
| arcentra.pipeline.approval.rejected | `EventTypePipelineApprovalRejected` | `approval_manager.go` |
| arcentra.pipeline.rollback.* | `EventTypePipelineRollback*` | **TBD**（常量已定义） |

---

## 3. `arcentra.job.*` / `arcentra.step.*`（TaskFramework / StepRunner 路径）

| type | 代码常量 | 发射位置 |
|------|----------|----------|
| arcentra.job.started | `EventTypeJobStarted` | `task_framework.go` |
| arcentra.job.completed | `EventTypeJobCompleted` | `task_framework.go` |
| arcentra.job.failed | `EventTypeJobFailed` | `task_framework.go` |
| arcentra.job.cancelled | `EventTypeJobCancelled` | **TBD** |
| arcentra.step.started | `EventTypeStepStarted` | `task_framework.go` `executeStepOnce` |
| arcentra.step.completed | `EventTypeStepCompleted` | 同上 |
| arcentra.step.failed | `EventTypeStepFailed` | 同上 |
| arcentra.step.cancelled | `EventTypeStepCancelled` | **TBD** |

**Runner-only 路径**：`JobRunner` + `StepRunner` 若未经过 `TaskFramework`，部分 job/step 事件可能**仅**在插件层出现；需在统一事件总线设计里补齐或注明差异。

---

## 4. 审批：Web / IM 订阅建议

- 控制面与 IM 机器人可优先订阅 **`arcentra.pipeline.approval.*`**（已与 `approval_manager` 对齐）。
- 若对外只暴露 **`arcentra.task.approval.*`**，需增加 **映射层** 或 改发射类型字符串（Breaking，需版本策略）。

---

## 5. 维护方式

- 新增发射点时：更新本表「发射位置」列。
- 与 [`plugin_event_cloudevents_spec.md`](../plugin_event_cloudevents_spec.md) 冲突时：**以本仓库常量名为准**修订草案，或实现桥接后注明「对外 type」。
