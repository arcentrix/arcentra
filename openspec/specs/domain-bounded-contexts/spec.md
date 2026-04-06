# domain-bounded-contexts 规范

## 目的
待定 - 由归档变更 migrate-to-ddd-layered-arch 创建。归档后请更新目的。
## 需求
### 需求:领域模型与持久化模型分离

系统必须在 `internal/domain/<context>/model.go` 中定义纯 Go 领域实体（无 GORM tag、无框架依赖），持久化模型必须独立存放在 `internal/infra/persistence/<context>/` 下，两者之间必须通过显式转换函数互转。

#### 场景:领域模型不含基础设施依赖

- **当** 检查 `internal/domain/` 下的任何 `.go` 文件的 import 列表
- **那么** 禁止出现 `gorm.io`、`github.com/gofiber`、`google.golang.org/grpc` 等基础设施包的引用

#### 场景:持久化模型包含 GORM 映射

- **当** 查看 `internal/infra/persistence/<context>/model.go`
- **那么** 必须包含 GORM struct tag 和 `TableName()` 方法，且必须提供 `ToDomain()` 和 `FromDomain()` 转换方法

### 需求:有界上下文目录结构规范

每个有界上下文必须在 `internal/domain/<context>/` 下包含以下文件：`model.go`（领域实体与值对象）、`repository.go`（仓储接口）、`service.go`（领域服务）、`event.go`（领域事件定义）、`consts.go`（常量与枚举）。系统必须为以下 5 个上下文建立结构：`agent`、`execution`、`identity`、`pipeline`、`project`。

#### 场景:identity 上下文包含完整结构

- **当** 检查 `internal/domain/identity/` 目录
- **那么** 必须存在 `model.go`（包含 User、Role、Menu 等实体定义）、`repository.go`（包含 IUserRepository 等接口）、`service.go`、`event.go`、`consts.go`

#### 场景:上下文之间无直接 import

- **当** 检查任一上下文（如 `internal/domain/pipeline/`）的 import
- **那么** 禁止直接导入另一个上下文的包（如 `internal/domain/identity/`），跨上下文引用必须通过应用层编排

### 需求:仓储接口在领域层定义

所有数据访问接口必须在 `internal/domain/<context>/repository.go` 中定义。接口方法的参数和返回值必须使用领域模型类型，禁止使用 GORM 类型（如 `*gorm.DB`）。

#### 场景:Pipeline 仓储接口定义

- **当** 查看 `internal/domain/pipeline/repository.go`
- **那么** 必须定义 `IPipelineRepository` 接口，方法签名仅使用 `context.Context`、领域模型类型和标准 Go 错误类型

#### 场景:仓储接口不依赖具体实现

- **当** 编译 `internal/domain/` 包
- **那么** 禁止产生对 `internal/infra/` 的依赖

### 需求:领域事件结构定义

每个上下文必须在 `event.go` 中定义该上下文可能产生的领域事件结构体。事件必须包含 `EventType` 常量、`OccurredAt` 时间戳和相关实体 ID。此阶段仅定义结构，不要求实现事件发布机制。

#### 场景:Agent 上下文领域事件

- **当** 查看 `internal/domain/agent/event.go`
- **那么** 必须定义 `AgentRegistered`、`AgentStatusChanged` 等事件结构体，每个结构体必须包含 `EventType() string` 方法和 `OccurredAt time.Time` 字段

### 需求:领域服务仅包含纯领域逻辑

领域服务（`internal/domain/<context>/service.go`）必须仅包含不属于任何单一实体的领域逻辑。领域服务禁止依赖基础设施接口（如 HTTP Client、MQ Producer），仅允许依赖本上下文的仓储接口和领域模型。

#### 场景:Identity 领域服务的密码校验

- **当** `IdentityDomainService` 需要验证密码强度
- **那么** 必须在领域服务中实现校验逻辑，不依赖外部服务；密码哈希的存储操作通过仓储接口完成

