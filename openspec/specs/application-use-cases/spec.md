# application-use-cases 规范

## 目的
待定 - 由归档变更 migrate-to-ddd-layered-arch 创建。归档后请更新目的。
## 需求
### 需求:Use Case 结构与职责

应用层必须位于 `internal/case/<context>/`，每个 Use Case 必须是一个独立的结构体，通过构造函数注入领域仓储接口。Use Case 必须负责：事务协调、跨上下文编排、输入验证、权限检查委托。禁止在 Use Case 中实现核心领域逻辑。

#### 场景:创建项目用例

- **当** 调用 `CreateProjectUseCase.Execute(ctx, input)`
- **那么** 必须通过 `project` 上下文的仓储接口创建项目，通过 `identity` 上下文的仓储接口验证用户权限，并返回领域模型或错误

#### 场景:Use Case 不直接访问数据库

- **当** 检查 `internal/case/` 下任何文件的 import
- **那么** 禁止出现 `gorm.io`、`database/sql` 或 `internal/infra/` 的直接引用

### 需求:消除 Services 上帝对象

现有 `internal/control/service/services.go` 中的 `Services` 结构体必须被拆解。每个服务方法必须迁移到对应上下文的 Use Case 中。`Services` 结构体中直接暴露的 Repo 字段（如 `ProjectMemberRepo`、`StepRunRepo`）必须被移除，相关操作必须封装为 Use Case。

#### 场景:Pipeline 相关操作迁移

- **当** 原 `Services.PipelineRepo` 被 router 直接使用
- **那么** 必须创建对应的 Use Case（如 `ListPipelinesUseCase`、`GetPipelineUseCase`），由适配器层调用 Use Case 而非直接调用 Repo

#### 场景:Services 结构体最终移除

- **当** 所有迁移阶段完成后
- **那么** `internal/control/service/services.go` 必须被删除，不允许残留代理调用

### 需求:跨上下文通信规范

跨有界上下文的操作必须在应用层（`internal/case/`）中编排，禁止领域层直接跨上下文调用。跨上下文的数据传递必须使用 DTO 或领域事件，禁止直接传递另一个上下文的领域模型。

#### 场景:执行流水线时需要项目信息

- **当** `ExecutePipelineUseCase` 需要获取项目的 SCM 配置
- **那么** 必须通过 `project` 上下文的仓储接口获取所需数据，在应用层进行数据组装，禁止在 `execution` 领域服务中直接导入 `project` 领域包

### 需求:DTO 定义与转换

每个上下文的应用层必须在 `internal/case/<context>/dto.go` 中定义输入/输出 DTO。DTO 必须是简单的 Go struct，用于应用层与适配器层之间的数据传递。必须提供领域模型与 DTO 之间的转换方法。

#### 场景:Agent 注册 DTO

- **当** HTTP Router 调用 `RegisterAgentUseCase`
- **那么** 必须传入 `RegisterAgentInput` DTO，Use Case 返回 `RegisterAgentOutput` DTO，禁止直接传递 HTTP 请求结构体或领域模型给适配器

