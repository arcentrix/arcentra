# Pipeline 编排与 Agent 下发：决策备忘

本文档落实计划中的 **decide-orchestration** 与 **decide-agent-dispatch**：基于**当前仓库证据**给出推荐缺省与待团队盖章项，便于后续实现与文档统一。

---

## 1. 编排：Runner（并发组）vs Executor（DAG）

### 代码事实

- **Runner**（`internal/shared/pipeline/pipeline_runner.go`）：`job.concurrency` 相同 key 的 job **串行**，其余 **并行**；无 `depends_on` / DAG 语义。
- **Executor**（`internal/shared/pipeline/pipeline_executor.go` + `task.go` `BuildDAG`）：按 proto `Job.dependsOn`（YAML 常见写作 `depends_on` / `dependsOn`）建 **DAG**，`Reconciler` 调度；`TaskFramework` 负责每 job 生命周期与可选队列入队。

### 推荐（缺省）

| 场景 | 推荐引擎 | 理由 |
|------|----------|------|
| 常规 CI（独立 job、仅需互斥锁） | **Runner** | 与 GitLab「resource_group」式并发键接近，心智简单。 |
| 大数据 / 多阶段训练、显式依赖 | **Executor** | DAG 与 `dependsOn` 一致，便于表达特征→训练→推理等边。 |
| 控制面尚未 wiring | **先选定其一接入** | 见 [`execution_inventory.md`](./execution_inventory.md)：两引擎均未在控制面被调用。 |

### 待团队确认（单一真相 / 双轨）

- **方案 A（长期）**：只保留一条**默认**编排路径，另一条删或薄封装（计划 §7 方案 A）。
- **方案 B（过渡）**：文档写明「仅 Executor 支持 `dependsOn`」或「仅 Runner 支持某类 job」，避免同一 YAML 双引擎语义漂移。

---

## 2. Agent 下发：AgentManager（RPC 等待）vs TaskQueue（异步队列）

### 代码事实

- **AgentManager**：`StepRunner` 在 `RunOnAgent && AgentManager != nil` 时走 `executeOnAgent`，同步等待 StepRun 完成。
- **TaskQueue**：`TaskFramework.queue` 在 `TaskQueue != nil` 时对 `RunOnAgent` step 入队；与 `StepRunner` 内 Agent 路径**独立**，需避免同一 step **重复**下发。

### 推荐（缺省）

| 部署 | 推荐 |
|------|------|
| 控制面与 StepRun 服务强连接、可接受阻塞 | **AgentManager**，不配 `ExecutionContext.TaskQueue`（或 Executor 不用 `NewPipelineExecutorWithQueue`）。 |
| 解耦、峰值缓冲、Agent 自主拉取 | **TaskQueue**，且 **StepRunner 路径上不注入 AgentManager**（或 DSL 层约定不混用）。 |

### 待团队确认（显式模式）

- 在配置或 `ExecutionContext` 增加 **`agent_dispatch: rpc | queue`**（计划 §7 方案 C），并在文档中写清互斥条件。

---

## 3. 与计划 to-do 的对应关系

| 计划 to-do | 本文档 |
|------------|--------|
| decide-orchestration | §1 + 待确认项 |
| decide-agent-dispatch | §2 + 待确认项 |

正式决策可在 PR 或 ADR 中引用本节并勾选最终方案。
