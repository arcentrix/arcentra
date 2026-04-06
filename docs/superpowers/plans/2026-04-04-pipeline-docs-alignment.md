# Pipeline 文档对齐 — 最终执行版（可直接按序落地）

> **版本：** 最终执行版（2026-04-04 修订）。按 **Task 1 → Task 7** 顺序在同一功能分支完成；每个 Task 先写/跑失败用例再实现，通过验收命令后再提交。
> **Agent 执行：** 推荐 `superpowers:subagent-driven-development`（每 Task 独立子代理）或 `superpowers:executing-plans`（同会话检查点）；实现前按需加载 `superpowers:verification-before-completion`。

---

## 1. 目标

将 [docs/pipeline/](../../pipeline/) 与 [docs/plugin_event_cloudevents_spec.md](../../plugin_event_cloudevents_spec.md) 描述的能力与当前代码对齐：

- HTTP 触发：`requestId` 幂等、`variables` 进入执行上下文（与 [http_api.md](../../pipeline/http_api.md) 一致）。
- 「创建 Run」后接通 `internal/shared/pipeline` 执行栈（与 [execution_inventory.md](../../pipeline/execution_inventory.md) 一致）。
- 编排模式 `executor|runner` 与 Agent 下发 `rpc|queue` 由配置驱动并互斥注入（与 [architecture_decisions.md](../../pipeline/architecture_decisions.md) 一致）。
- 审批治理 REST MVP（与 [approval_governance.md](../../pipeline/approval_governance.md) 一致）。
- 取消等路径补齐 CloudEvents，并更新 [plugin_events_mapping.md](../../pipeline/plugin_events_mapping.md)。
- DSL 校验/保存/获取从占位改为真实 `dsl.Processor`（与 [dsl.md](../../pipeline/dsl.md)、[schema.md](../../pipeline/schema.md) 一致）。

**核心架构（实现时遵守）：**

- 用例层：`internal/case/pipeline` 负责触发与持久化。
- 新增窄接口 **`RunLauncher`**：`读 DSL 文本` → `dsl.Processor.ProcessConfig` → `NewPipelineExecutor` / `NewPipelineRunner` → 在**独立** `context` 中异步 `Execute` / `Run`。
- 持久化 Run 状态：一律使用 **`int(domain.PipelineStatus*)`**，禁止 `"cancelled"` 等裸字符串写入 `UpdateRun`。

---

## 2. 本仓库现状（只读事实，避免重复造轮子）

| 项 | 状态 |
|----|------|
| 领域仓储 `GetRunByRequestID(ctx, pipelineID, requestID)` | 已实现：[`internal/domain/pipeline/repository.go`](../../internal/domain/pipeline/repository.go)、[`internal/infra/persistence/pipeline/repo.go`](../../internal/infra/persistence/pipeline/repo.go) |
| `PipelineRun.RequestID` / DB `request_id` | 已存在 |
| `TriggerRun` | 当前**始终** `id.GetUUID()` 写入 `RequestID`，**未**做幂等、**未**使用 HTTP 的 `requestId` / `variables`：[`usecase.go`](../../internal/case/pipeline/usecase.go) |
| HTTP `triggerPipeline` | 已解析 `requestId`、`variables`，**未**传入 `TriggerRunInput`：[`internal/adapter/http/router_pipeline.go`](../../internal/adapter/http/router_pipeline.go)（约 L205–222） |
| `StopRun` / `PauseRun` / `ResumeRun` | 使用字符串 `"cancelled"` / `"paused"` / `"running"`，与领域整型枚举不一致：[`usecase_extended.go`](../../internal/case/pipeline/usecase_extended.go) |
| `ValidatePipelineSpec` / `SavePipelineSpec` | 占位返回：[`usecase_extended.go`](../../internal/case/pipeline/usecase_extended.go) |
| `ManagePipelineUseCase` 构造 | 仅注入 `repo`：[`provider.go`](../../internal/case/pipeline/provider.go) — RunLauncher 等需扩展 |

**路由路径约定：** 本工作区 Pipeline HTTP 在 [`internal/adapter/http/router_pipeline.go`](../../internal/adapter/http/router_pipeline.go)。若你所在分支已迁到 `internal/control/router`，用 `rg 'triggerPipeline|Post.*trigger' --glob '*.go'` 替换下表中的路径，逻辑不变。

---

## 3. 文件职责总表

| 路径 | 职责 |
|------|------|
| [`internal/case/pipeline/dto.go`](../../internal/case/pipeline/dto.go) | `TriggerRunInput` 增加 `RequestID`、`Variables` |
| [`internal/case/pipeline/usecase.go`](../../internal/case/pipeline/usecase.go) | `TriggerRun`：幂等查 `GetRunByRequestID`、空 `RequestID` 时生成 UUID、合并 variables（供 Launcher/ProcessConfig） |
| [`internal/case/pipeline/usecase_test.go`](../../internal/case/pipeline/usecase_test.go) | 新建：幂等、Stop 状态等单测 |
| [`internal/adapter/http/router_pipeline.go`](../../internal/adapter/http/router_pipeline.go) | `TriggerRunInput` 传入 `RequestID`、`Variables`（及现有字段） |
| [`internal/case/pipeline/usecase_extended.go`](../../internal/case/pipeline/usecase_extended.go) | `StopRun`/`PauseRun`/`ResumeRun` 使用 `int(domain.PipelineStatus*)`；Task 7 替换 Spec 占位实现 |
| [`internal/case/pipeline/config.go`](../../internal/case/pipeline/config.go) | **新建**：`OrchestrationMode`、`AgentDispatchMode`、`ExecutionOptions` |
| [`internal/case/pipeline/run_launcher.go`](../../internal/case/pipeline/run_launcher.go) | **新建**：`RunLauncher` 接口与实现 |
| [`internal/case/pipeline/provider.go`](../../internal/case/pipeline/provider.go) + [`cmd/arcentra/wire.go`](../../cmd/arcentra/wire.go) | 注入 Launcher、logger、plugin manager、可选 TaskQueue、可选 `AgentManager` |
| [`internal/shared/pipeline/*.go`](../../internal/shared/pipeline/) | Task 6：取消路径发射 `EventTypePipelineCancelled` 等 |
| 审批：`internal/domain/pipeline/approval.go`、`internal/infra/persistence/pipeline/approval_repo.go`、迁移 SQL、`approval_usecase.go`、HTTP router 新建并注册 | Task 5 |
| [`docs/pipeline/plugin_events_mapping.md`](../../pipeline/plugin_events_mapping.md) | Task 6 每类发射点更新「发射位置」 |
| [`docs/pipeline/architecture_decisions.md`](../../pipeline/architecture_decisions.md) | Task 3 后勾选「配置已暴露」一句 |

---

## Task 1：`TriggerRun` 幂等（`pipeline_id` + `request_id`）

**验收：** 相同 `requestId` 两次触发返回同一 `runID`；请求体未带 `requestId` 时行为与现网一致（每次新 Run）。

**步骤：**

1. 新建 `usecase_test.go`，实现内存版 `IPipelineRepository`（须实现 `GetRunByRequestID`，`CreateRun` 时若 `RequestID` 非空则记入 `byReq` 索引）。测试：`TestTriggerRun_IdempotentRequestID` — 两次 `TriggerRun` 相同 `RequestID` 断言 `RunID` 相同。先运行：`go test ./internal/case/pipeline/... -run TestTriggerRun_IdempotentRequestID -v` **预期 FAIL**。
2. `dto.go`：`TriggerRunInput` 增加 `RequestID string`、`Variables map[string]string`。
3. `usecase.go`：`TriggerRun` 在 `Get` 到 `pl` 之后：
   - `reqID := strings.TrimSpace(in.RequestID)`
   - 若 `reqID != ""`，`GetRunByRequestID` 命中则**直接返回**该 run
   - 创建 run 时：`RequestID: reqID`（若 `reqID == ""` 则 `id.GetUUID()`）
   - `variables`：存入 run 的可扩展字段或交给 Task 4 Launcher（若尚无字段，可先挂在 `TriggerRun` 末尾仅传给 `StartRun` 的 `env` 合并策略在 Task 4 统一说明）
4. `router_pipeline.go`：`TriggerRunInput` 增加 `RequestID: strings.TrimSpace(req.RequestID)`、`Variables: req.Variables`（补全 `Branch`、`CommitSha`、`TriggerType` 若 HTTP 已有则一并传入）。
5. 再跑：`go test ./internal/case/pipeline/... -run TestTriggerRun_IdempotentRequestID -v` **预期 PASS**。

**建议提交：** `fix(pipeline): honor trigger requestId for idempotent runs`

---

## Task 2：`StopRun` / `PauseRun` / `ResumeRun` 状态与领域枚举一致

**验收：** `UpdateRun` 的 `updates["status"]` 为 **`int`**，取值分别为 `PipelineStatusCancelled` / `PipelineStatusPaused` / `PipelineStatusRunning`。

**步骤：**

1. `usecase_test.go`：spy repo 记录 `UpdateRun` 的 `updates["status"]`，`TestStopRun_UsesNumericStatus`（及 pause/resume 可选同文件）。先跑确认若当前为字符串则 **FAIL**。
2. `usecase_extended.go`：`import domain "github.com/arcentrix/arcentra/internal/domain/pipeline"`，三处改为 `int(domain.PipelineStatusCancelled)` 等。
3. `go test ./internal/case/pipeline/... -v` **PASS**。

**建议提交：** `fix(pipeline): persist run status as numeric enum in pause/stop/resume`

---

## Task 3：编排与 Agent 下发配置

**验收：** 配置可从 `conf.d/config.toml`（或项目选用的 config 结构体）读取；`queue` 与 `rpc` 互斥语义在 Task 4 构造 Launcher 时强制（Task 3 可先定义类型 + 绑定 + 单测或编译期约束文档）。

**步骤：**

1. 新建 `internal/case/pipeline/config.go`：

```go
type OrchestrationMode string

const (
	OrchestrationExecutor OrchestrationMode = "executor"
	OrchestrationRunner   OrchestrationMode = "runner"
)

type AgentDispatchMode string

const (
	AgentDispatchRPC   AgentDispatchMode = "rpc"
	AgentDispatchQueue AgentDispatchMode = "queue"
)

type ExecutionOptions struct {
	Orchestration OrchestrationMode
	AgentDispatch AgentDispatchMode
	WorkspaceRoot string
}
```

2. 在现有控制面配置中增加小节（命名与项目一致即可，如 `pipeline.execution`），并注入到 Launcher 依赖。
3. 更新 [`docs/pipeline/architecture_decisions.md`](../../pipeline/architecture_decisions.md) §3：注明「已在配置暴露」。

**建议提交：** `feat(pipeline): add execution orchestration and agent dispatch config`

---

## Task 4：`RunLauncher` — 触发后启动执行栈

**验收：** `POST .../trigger` 后 Run 能从 `PENDING` 进入执行态或明确 `FAILED`（带原因）；**禁止**长期用空 DSL 调用 `ProcessConfig`。

**接口（最小）：**

```go
type RunLauncher interface {
	StartRun(ctx context.Context, run *domain.PipelineRun, pl *domain.Pipeline, env map[string]string) error
}
```

**实现要点：**

1. **读取 DSL**：与 `GetPipelineSpec`/SCM 路线一致（DB 缓存、对象存储或 `git clone` 之一），在一期文档中写清选用方案并实现。
2. `processor := dsl.NewDSLProcessor(logger)`；`ProcessConfig(ctx, dslText, pluginMgr, workspaceDir, env)`。
3. `AgentDispatchQueue`：`execCtx.SetTaskQueue(q)` 且**不**设置 RPC `AgentManager`；`AgentDispatchRPC`：注入 `AgentManager`，`TaskQueue == nil`。
4. `go func() { ... }()` 内按 `OrchestrationMode` 调用 `Execute` 或 `Run`；结束更新 Run `status` / `duration`；启动失败推荐 `UpdateRun` 为 `FAILED` 并写 message。
5. `usecase.go`：`TriggerRun` 在 `CreateRun` 成功后调用 `launcher.StartRun`（错误策略按上条）。
6. Wire：`provider.go`、`cmd/arcentra/wire.go`；测试可注入 noop Launcher。

**建议提交：** `feat(pipeline): launch shared executor after trigger`

---

## Task 5：审批治理 HTTP API（MVP）

**验收：** 路由与 [approval_governance.md](../../pipeline/approval_governance.md) §3 一致；`Approve` 幂等（重复不改变终态）；callback 路由验签可占位。

**路由清单：**

- `GET /api/v1/pipelines/approvals?status=pending&pipelineId=`
- `GET /api/v1/pipelines/approvals/:approvalId`
- `POST /api/v1/pipelines/approvals/:approvalId/approve`
- `POST /api/v1/pipelines/approvals/:approvalId/reject`
- `POST /api/v1/pipelines/approvals/callbacks/:provider`

**建议提交：** `feat(pipeline): add approval REST API MVP`

---

## Task 6：CloudEvents 缺口

**验收：** 取消 Run 时至少一条 `pipeline.cancelled`（或 spec 中等价类型）经现有 emitter 发出；映射表 TBD 行被替换为具体包/函数路径。

**涉及：** [`internal/shared/pipeline`](../../internal/shared/pipeline/) 中 `StopPipeline`/context cancel；可选 `job`/`step` cancelled；可选 `task_cloudevent_bridge`。

**建议提交：** `feat(events): emit pipeline and job cancel cloud events`

---

## Task 7：DSL / Schema 校验与保存

**验收：** `ValidatePipelineSpec` 对非法 DSL 返回 `valid: false` 与错误信息；`SavePipelineSpec`/`GetPipelineSpec` 非占位，与 Task 4 的 **DefinitionSource** 一致。

**建议提交：** `feat(pipeline): wire spec validate/save to dsl processor`

---

## 4. 规格自检表（完成后勾选）

| 文档 | 对应 Task |
|------|-----------|
| [http_api.md](../../pipeline/http_api.md) Trigger 体 | 1 |
| [api_design.md](../../pipeline/api_design.md) 幂等、状态机 | 1、2 |
| [execution_inventory.md](../../pipeline/execution_inventory.md) | 4 |
| [architecture_decisions.md](../../pipeline/architecture_decisions.md) | 3、4 |
| [approval_governance.md](../../pipeline/approval_governance.md) | 5 |
| [plugin_events_mapping.md](../../pipeline/plugin_events_mapping.md) | 6 |
| [dsl.md](../../pipeline/dsl.md) / [schema.md](../../pipeline/schema.md) | 7 |
| [workload_semantics.md](../../pipeline/workload_semantics.md) | `task.progress` 等可独立迭代 |

---

## 5. 全局验证命令

```bash
go test ./internal/case/pipeline/... -v
go test ./...   # 或 make test
```

---

## 6. 硬约束（违反即视为未完成）

- **DSL 来源不得为空：** Task 4 必须选定并实现读取路径，不得长期 `ProcessConfig("", ...)`。
- **状态类型：** `UpdateRun` 的 `status` 与领域一致，使用 **`int(domain.PipelineStatus*)`**。
- **互斥：** `agent_dispatch=queue` 时不得同时注入 RPC `AgentManager`（与 architecture_decisions 一致）。

---

## 7. 附录 A：Task 1 完整单测（粘贴至 `internal/case/pipeline/usecase_test.go`）

若 `IPipelineRepository` 方法集有增减，对 `memRepo` 补全 stub 直至编译通过。

```go
package pipeline_test

import (
	"context"
	"errors"
	"testing"

	casepl "github.com/arcentrix/arcentra/internal/case/pipeline"
	domain "github.com/arcentrix/arcentra/internal/domain/pipeline"
)

type memRepo struct {
	pipelines map[string]*domain.Pipeline
	runs      map[string]*domain.PipelineRun
	byReq     map[string]*domain.PipelineRun // key: pipelineID + "\x00" + requestID
}

func newMemRepo() *memRepo {
	return &memRepo{
		pipelines: map[string]*domain.Pipeline{},
		runs:      map[string]*domain.PipelineRun{},
		byReq:     map[string]*domain.PipelineRun{},
	}
}

func (m *memRepo) Create(ctx context.Context, p *domain.Pipeline) error {
	m.pipelines[p.PipelineID] = p
	return nil
}

func (m *memRepo) Update(ctx context.Context, pipelineID string, updates map[string]any) error {
	return nil
}

func (m *memRepo) Get(ctx context.Context, pipelineID string) (*domain.Pipeline, error) {
	p, ok := m.pipelines[pipelineID]
	if !ok {
		return nil, errors.New("not found")
	}
	return p, nil
}

func (m *memRepo) List(ctx context.Context, query *domain.PipelineQuery) ([]*domain.Pipeline, int64, error) {
	return nil, 0, nil
}

func (m *memRepo) CreateRun(ctx context.Context, run *domain.PipelineRun) error {
	m.runs[run.RunID] = run
	if run.RequestID != "" {
		m.byReq[run.PipelineID+"\x00"+run.RequestID] = run
	}
	return nil
}

func (m *memRepo) GetRun(ctx context.Context, runID string) (*domain.PipelineRun, error) {
	return m.runs[runID], nil
}

func (m *memRepo) UpdateRun(ctx context.Context, runID string, updates map[string]any) error {
	return nil
}

func (m *memRepo) GetRunByRequestID(ctx context.Context, pipelineID, requestID string) (*domain.PipelineRun, error) {
	r, ok := m.byReq[pipelineID+"\x00"+requestID]
	if !ok {
		return nil, errors.New("not found")
	}
	return r, nil
}

func (m *memRepo) ListRuns(ctx context.Context, query *domain.PipelineRunQuery) ([]*domain.PipelineRun, int64, error) {
	return nil, 0, nil
}

func TestTriggerRun_IdempotentRequestID(t *testing.T) {
	repo := newMemRepo()
	_ = repo.Create(context.Background(), &domain.Pipeline{
		PipelineID:       "p1",
		Name:             "n",
		LastCommitSha:    "deadbeef",
		PipelineFilePath: ".arcentra/pipeline.yaml",
	})
	uc := casepl.NewManagePipelineUseCase(repo)
	in := casepl.TriggerRunInput{
		PipelineID:  "p1",
		TriggeredBy: "u1",
		RequestID:   "req-same",
	}
	r1, err := uc.TriggerRun(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := uc.TriggerRun(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if r1.RunID != r2.RunID {
		t.Fatalf("expected same run, got %q vs %q", r1.RunID, r2.RunID)
	}
}
```

---

**计划文件路径：** `docs/superpowers/plans/2026-04-04-pipeline-docs-alignment.md`（本文件即为唯一权威执行版）。
