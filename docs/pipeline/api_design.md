# Pipeline API Design (MVP)

本文基于当前仓库实现与文档现状，给出可落地的 Pipeline API 一期方案，并明确二期扩展边界。

## Scope

- 主协议：`api/pipeline/v1/pipeline.proto`（Pipeline / StepRun 等 gRPC 契约）
- 运行模型：`internal/shared/pipeline/spec`（由 `api/pipeline/v1/pipeline_spec.proto` 生成的 `Spec` / `Job` / `Step` 等类型别名）
- 控制面用例：`internal/case/pipeline`（如 `ManagePipelineUseCase.TriggerRun`）
- HTTP 适配：`internal/adapter/http/router_pipeline.go`
- 持久化：`internal/infra/persistence/pipeline` 与 `internal/domain/pipeline`
- 执行引擎（库）：`internal/shared/pipeline`（`Runner` / `Executor` / `StepRunner` / `AgentManager`）；与触发链路的连接见 [`execution_inventory.md`](./execution_inventory.md)
- 数据库草案：`../database_schema_complete.sql`（若存在；以实际迁移为准）

## 对齐结果（proto/spec/model/sql）

### 冻结的 MVP 字段集

Pipeline（定义元数据）：

- `pipeline_id`
- `project_id`
- `name`
- `description`
- `repo_url`
- `default_branch`
- `pipeline_file_path`
- `save_mode` (`direct|pr`)
- `pr_target_branch`
- `metadata`
- `status`
- `created_by`
- `is_enabled`
- `last_sync_status`
- `last_sync_message`
- `last_synced_at`
- `last_editor`
- `last_commit_sha`
- `last_save_request_id`

PipelineRun（运行态）：

- `run_id`
- `pipeline_id`
- `pipeline_name`
- `branch`
- `commit_sha`
- `definition_commit_sha`
- `definition_path`
- `status`
- `trigger_type`
- `triggered_by`
- `variables`
- `total_jobs/completed_jobs/failed_jobs/running_jobs`
- `start_time/end_time/duration`

### 关键冲突与处理

- `namespace` 冲突：`spec.Pipeline.Namespace` 是运行 DSL 必填字段；外部资源主键统一为 `pipeline_id`，避免语义混淆。
- `branch` 命名：SQL 草案是 `branch`，MVP 模型采用 `default_branch`；迁移时增加列映射。
- `status` 枚举：已恢复 `PAUSED`，当前收敛为 `PENDING/RUNNING/PAUSED/SUCCESS/FAILED/CANCELLED`。
- `create_time/update_time` 与 `created_at/updated_at`：以当前 GORM `BaseModel` 为准，迁移脚本做兼容映射。
- `spec` 结构化字段（如 `dependsOn/retry/when/runOnAgent`）不直接平铺入 Pipeline 表；由定义文件承载，触发时解析。

## 统一语义（状态机/幂等/取消/分页/错误码）

### 状态机

- Pipeline：
  - `PENDING -> RUNNING/CANCELLED`
  - `RUNNING -> SUCCESS/FAILED/PAUSED/CANCELLED`
  - `PAUSED -> RUNNING/CANCELLED`
  - `FAILED -> RUNNING`（重试语义）
- PipelineRun：
  - 创建时 `PENDING`
  - 执行器推进到终态
  - `StopPipeline` 强制转 `CANCELLED`

### 幂等语义

- `SavePipelineDefinition`：`request_id` 幂等键。
  - 若 `request_id == last_save_request_id` 且 `last_commit_sha` 存在，返回已有结果，不重复提交。
- `TriggerPipeline`：`request_id` 作为幂等键落库（`pipeline_id + request_id`），重复请求返回已有 `run_id`。

### 并发与冲突

- `SavePipelineDefinition` 要求客户端提供 `expected_head_commit_sha`（可选但推荐）。
- 若与仓库当前 HEAD 不一致，返回 `conflict`（409）。

### 取消语义

- `StopPipeline(run_id)` 语义为“停止运行实例”，并将 `PipelineRun.status` 更新为 `CANCELLED`。
- Pipeline 主状态同步更新为 `CANCELLED`（MVP）。

### 分页语义

- `page <= 0` 默认 `1`
- `page_size <= 0` 默认 `20`
- `page_size > 100` 截断为 `100`

### 错误码分层

- `validation`：参数缺失、定义校验失败
- `not_found`：pipeline/project/run 不存在
- `conflict`：HEAD 冲突
- `internal`：git/IO/DB 内部错误

## API 契约（MVP）

### Pipeline 管理

- `CreatePipeline`
- `UpdatePipeline`
- `GetPipeline`
- `ListPipelines`
- `DeletePipeline`

### Pipeline 运行

- `TriggerPipeline`
- `StopPipeline`
- `GetPipelineRun`
- `ListPipelineRuns`

### 定义编辑与发布

- `GetPipelineDefinition`：从仓库读取定义 + `head_commit_sha`
- `ValidatePipelineDefinition`：JSON/YAML 校验
- `SavePipelineDefinition`：写入文件并推送（直推/PR 分支）

## 兼容策略

### 一期兼容

- 保留原 PipelineService 的核心 CRUD/Run 入口，新增定义编辑接口。
- 对旧客户端：未使用新增字段时按默认行为执行。

### 二期路线

- 真实 PR 创建（而非 URL 推导）已完成。
- Trigger 请求幂等落库已完成。
- 更完整 DSL 校验（与高级规则链对齐）已完成。
- `PAUSED` 状态及 Pause/Resume 全链路支持已完成。

## 最小实现蓝图

### 分层建议

- `PipelineService`（gRPC 协议适配层）
- `PipelineApplication`（应用编排层，承载语义）
- `PipelineRepo`（定义/运行数据访问）
- `DefinitionSource`（仓库读取与保存）
- `DefinitionValidator`（JSON/YAML + DSL）

### 关键流程

- `CreatePipeline`：校验 -> 项目补全仓库信息 -> 入库
- `GetPipelineDefinition`：读 Pipeline -> 读 Project 凭据 -> clone + read
- `SavePipelineDefinition`：读 HEAD -> 冲突检测 -> 写文件 -> commit -> push -> 同步元数据
- `TriggerPipeline`：读定义 -> 校验 -> 创建 Run -> 写入 `definition_commit_sha`

### 测试清单

- Repository:
  - `List/ListRuns` 分页边界与过滤
- Service:
  - `CreatePipeline` 默认分支回填
  - `ValidatePipelineDefinition` JSON/YAML 正反例
  - `SavePipelineDefinition` 幂等路径
  - `SavePipelineDefinition` HEAD 冲突
  - `SavePipelineDefinition` direct/pr 分支路径
  - `TriggerPipeline` 写入 `definition_commit_sha/definition_path`
  - `StopPipeline` 状态与时长更新

## 当前实现差距（与本设计对比）

- 当前二期目标项已全部落地：
  - `SavePipelineDefinition`：PR 模式使用托管平台 API 创建 PR/MR 并返回平台 URL；
  - `TriggerPipeline`：`request_id` 去重持久化（`pipeline_id + request_id` 幂等）；
  - `ValidatePipelineDefinition`：升级为高级规则链校验（timeout/retry/uses/agentSelector）；
  - 状态机：恢复 `PAUSED` 并支持 Pause/Resume。

- **HTTP Trigger 与幂等（需对齐）**：`POST /api/v1/pipelines/:pipelineId/trigger` 的请求体可携带 `requestId`，但当前用例层 `TriggerRunInput` 未传入该字段；若需与设计「`pipeline_id + request_id` 幂等」一致，应在 use case 与仓储层补全。**执行链**：触发后创建 `PENDING` Run 与「读定义 → `ProcessConfig` → `Runner`/`Executor`」之间仍存在缺口，见 [`execution_inventory.md`](./execution_inventory.md)。

## HTTP API 映射（用户操作）

与 [`http_api.md`](./http_api.md) 一致，流水线定义相关路径使用 **`/spec`**（而非 `/definition`）。

- `POST /api/v1/pipelines` -> `CreatePipeline`
- `PUT /api/v1/pipelines/:pipelineId` -> `UpdatePipeline`
- `GET /api/v1/pipelines/:pipelineId` -> `GetPipeline`
- `GET /api/v1/pipelines` -> `ListPipelines`
- `DELETE /api/v1/pipelines/:pipelineId` -> `DeletePipeline`
- `GET /api/v1/pipelines/:pipelineId/spec` -> `GetPipelineDefinition`（语义）
- `POST /api/v1/pipelines/:pipelineId/spec/validate` -> `ValidatePipelineDefinition`（语义）
- `POST /api/v1/pipelines/:pipelineId/spec/save` -> `SavePipelineDefinition`（语义）
- `POST /api/v1/pipelines/:pipelineId/trigger` -> `TriggerPipeline`
- `GET /api/v1/pipelines/:pipelineId/runs` -> `ListPipelineRuns`
- `GET /api/v1/pipelines/runs/:runId` -> `GetPipelineRun`
- `POST /api/v1/pipelines/:pipelineId/runs/:runId/stop` -> `StopPipeline`
- `POST /api/v1/pipelines/:pipelineId/runs/:runId/pause` -> `PausePipeline`
- `POST /api/v1/pipelines/:pipelineId/runs/:runId/resume` -> `ResumePipeline`
