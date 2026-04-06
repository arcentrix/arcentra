## ADDED Requirements

### 需求:分阶段增量迁移

迁移必须分为 7 个阶段顺序执行，每个阶段完成后系统必须可编译、可测试、可部署。禁止在单次大爆炸式重构中完成所有迁移。

#### 场景:阶段定义

- **当** 开始迁移
- **那么** 必须按以下顺序执行：阶段 1 — 建立领域层骨架并迁移模型和接口；阶段 2 — 建立基础设施层并实现仓储；阶段 3 — 建立应用层 Use Case；阶段 4 — 建立适配器层并迁移 Router；阶段 5 — pkg/ 重组（按 foundation → telemetry → store → transport → message → engine → integration → lifecycle 顺序）；阶段 6 — Wire 重组与引导更新；阶段 7 — 清理旧代码与最终验证

#### 场景:每个阶段可独立验证

- **当** 完成任一迁移阶段
- **那么** 必须通过 `go build ./...` 编译检查和现有测试套件，禁止出现编译错误或测试回归

### 需求:先迁移一个上下文作为模板

必须首先选择一个最小的有界上下文（`agent`）完成全栈迁移（domain → infra → case → adapter），作为其他上下文迁移的参考模板。

#### 场景:Agent 上下文作为首个迁移目标

- **当** 开始阶段 1 的实施
- **那么** 必须先完成 `agent` 上下文的 `internal/domain/agent/`（model、repository 接口、service、event、consts）、`internal/infra/persistence/agent/`（仓储实现）、`internal/case/agent/`（Use Case）、`internal/adapter/http/router/router_agent.go`（HTTP Router）的完整迁移

#### 场景:模板审核后再迁移其他上下文

- **当** `agent` 上下文的全栈迁移完成
- **那么** 必须进行代码审核，确认目录结构、命名规范、依赖方向符合设计文档后，再继续迁移其他上下文

### 需求:旧代码兼容期

迁移期间必须保留 `internal/control/` 下的旧代码，新代码与旧代码并存。旧代码的移除必须在对应新代码验证通过后执行。迁移期间必须使用 `// Deprecated: use internal/domain/... instead` 注释标记已迁移的旧代码。

#### 场景:旧 repo 标记废弃

- **当** `internal/domain/agent/repository.go` 和 `internal/infra/persistence/agent/` 验证通过
- **那么** `internal/control/repo/repo_agent.go` 必须添加 `// Deprecated` 注释，但禁止立即删除，直到所有引用方迁移完成

#### 场景:旧代码最终清理

- **当** 所有 5 个上下文的迁移全部完成且验证通过
- **那么** 必须删除 `internal/control/model/`、`internal/control/repo/`、`internal/control/service/` 以及对应的旧测试文件

### 需求:Wire 渐进式迁移

Wire 依赖图必须渐进式更新。每迁移一个上下文，对应的 ProviderSet 必须从旧位置切换到新位置。禁止一次性重写整个 Wire 配置。

#### 场景:迁移 agent 上下文后更新 Wire

- **当** `agent` 上下文迁移完成
- **那么** `cmd/arcentra/wire.go` 必须将 agent 相关的 `repo.NewAgentRepo` 替换为 `infra/persistence/agent.NewAgentRepo`，将 `service.AgentService` 替换为 `case/agent` 下的 Use Case，其余上下文保持旧引用不变

### 需求:pkg 重组优先迁移零依赖分组

`pkg/` 重组必须从依赖最少的分组开始。`foundation`（纯工具，零 pkg 内部依赖）必须最先迁移，验证通过后再迁移 `telemetry`，依次类推。每个分组的迁移必须作为独立 PR，使用自动化工具批量替换 import 路径。

#### 场景:foundation 优先迁移

- **当** 开始 pkg/ 重组阶段
- **那么** 必须首先迁移 `pkg/foundation/`（含 env、id、safe、retry 等 15 个包），因为这些包不依赖 pkg 内其他包，迁移风险最低

#### 场景:自动化批量替换

- **当** 迁移某个 pkg 分组（如 `pkg/safe` → `pkg/foundation/safe`）
- **那么** 必须使用 `sed` 或 `goimports` 等工具批量替换全仓库的 import 路径，禁止手动逐文件修改

### 需求:持续集成验证

每个迁移 PR 必须通过 CI 管道的完整验证：编译检查（`go build ./...`）、静态分析（`golangci-lint`）、单元测试、Wire 生成验证（`wire check`）。

#### 场景:PR 提交后自动验证

- **当** 提交包含迁移变更的 PR
- **那么** CI 必须执行编译、lint、测试和 Wire 检查，任何一项失败必须阻止合并
