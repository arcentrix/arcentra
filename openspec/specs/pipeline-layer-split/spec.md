# pipeline-layer-split 规范

## 目的
待定 - 由归档变更 refactor-internal-pkg-ddd-clean 创建。归档后请更新目的。
## 需求
### 需求:Pipeline builtin handler 移至基础设施层

`internal/pkg/pipeline/builtin/` 中的所有 action handler（shell、stdout、artifacts、reports、scm 以及 Manager）必须移至 `internal/infra/pipeline/builtin/`。handler 通过接口注册到 pipeline 引擎核心。

#### 场景:builtin handler 位于 infra 层
- **当** 查找 `internal/infra/pipeline/builtin/` 目录
- **那么** 存在 `manager.go`、`shell.go`、`stdout.go`、`artifacts.go`、`reports.go`、`scm.go`、`types.go` 等文件

#### 场景:pipeline 引擎核心不包含 builtin
- **当** 检查 `internal/pkg/pipeline/` 目录
- **那么** 不存在 `builtin/` 子目录

#### 场景:builtin Manager 通过接口注册
- **当** pipeline 引擎启动时注册 builtin action handler
- **那么** 使用 `ActionHandler` 接口注册，不直接依赖 builtin 实现的具体类型

### 需求:Pipeline 引擎核心保留为共享内核

`internal/pkg/pipeline/` 中的引擎核心类型（`Context`、`ContextPool`、`Executor`、`Runner`、`JobRunner`、`StepRunner`、`Task`、`TaskFramework`、`TaskNode`、`Reconciler`、`AgentManager`、`ApprovalManager`、`WorkspaceManager`、`ExecutionContext`）必须保留在 `internal/pkg/pipeline/`。

#### 场景:引擎核心保留在 pkg
- **当** 查找 `Context`、`Executor`、`StepRunner`、`Reconciler` 的定义
- **那么** 它们位于 `internal/pkg/pipeline/` 目录下

#### 场景:spec/validation/interceptor 保留在 pkg
- **当** 检查 `internal/pkg/pipeline/spec/`、`internal/pkg/pipeline/validation/`、`internal/pkg/pipeline/interceptor/`
- **那么** 这些子目录及其内容保持不变

### 需求:Pipeline definition 子包位置确认

`internal/pkg/pipeline/definition/` 如果包含领域层的流水线定义模型，必须确认其与 `internal/domain/pipeline` 的关系，避免重复定义。

#### 场景:definition 与 domain 不冲突
- **当** 检查 `internal/pkg/pipeline/definition/` 的导出类型
- **那么** 不与 `internal/domain/pipeline/model.go` 中的类型名或语义重复

