# adapter-ports 规范

## 目的
待定 - 由归档变更 migrate-to-ddd-layered-arch 创建。归档后请更新目的。
## 需求
### 需求:HTTP 适配器统一入口

所有 HTTP Router 必须从 `internal/control/router/` 迁移到 `internal/adapter/http/`。Router 必须仅依赖应用层的 Use Case，禁止直接依赖领域服务或仓储接口。路由注册必须集中在 `internal/adapter/http/router.go` 中。

#### 场景:用户相关 HTTP 路由迁移

- **当** `router_user.go` 迁移到 `internal/adapter/http/router/router_user.go`
- **那么** Router 结构体必须注入 `case/identity` 下的 Use Case（如 `CreateUserUseCase`、`GetUserUseCase`），禁止注入 `*service.Services` 或 `*repo.Repositories`

#### 场景:HTTP 中间件独立组织

- **当** 查看 `internal/adapter/http/middleware/`
- **那么** 必须包含 JWT 认证、RBAC 授权、CORS、请求日志、错误处理等中间件，这些中间件从 `internal/control/router/` 和 `pkg/http/middleware/` 中整合而来

### 需求:gRPC 适配器迁移

gRPC 服务实现必须从 `internal/pkg/grpc/` 迁移到 `internal/adapter/grpc/`。每个 gRPC Service 实现必须依赖应用层 Use Case，禁止直接操作 `*service.Services`。Proto 生成代码（`api/*/v1`）保持不变。

#### 场景:Agent gRPC 服务迁移

- **当** `AgentServiceImpl` 从 `internal/pkg/grpc/` 迁移到 `internal/adapter/grpc/`
- **那么** 必须依赖 `case/agent` 下的 Use Case，gRPC 方法负责 proto 类型与 DTO 之间的转换

#### 场景:gRPC Server 启动配置

- **当** 启动 gRPC 服务
- **那么** `internal/adapter/grpc/server.go` 必须创建和配置 gRPC Server，注册所有服务实现和拦截器

### 需求:MQ 消费者适配器

消息队列的消费者逻辑必须组织在 `internal/adapter/mq/` 下。每个消费者 Router 必须调用应用层 Use Case 处理消息，禁止直接操作数据库。

#### 场景:MQ 消费者处理 Agent 心跳

- **当** 从 Kafka 接收到 Agent 心跳消息
- **那么** MQ 消费者 Router 必须反序列化消息后调用 `UpdateAgentHeartbeatUseCase`，禁止在 Router 中直接执行数据库操作

### 需求:WebSocket 适配器

WebSocket 连接管理和消息推送必须组织在 `internal/adapter/ws/` 下。WebSocket 适配器必须仅负责连接生命周期管理和消息序列化/反序列化，业务逻辑委托给应用层。

#### 场景:日志流推送

- **当** 客户端建立 WebSocket 连接订阅步骤运行日志
- **那么** `internal/adapter/ws/` 必须管理连接，从应用层获取日志流数据并推送给客户端

### 需求:Cron 适配器

定时任务的触发逻辑必须组织在 `internal/adapter/cron/` 下。当前 `bootstrap.go` 中硬编码的 cron 任务（如 SCM 轮询）必须迁移到此处。

#### 场景:SCM 轮询定时任务

- **当** 系统启动时注册 cron 任务
- **那么** `internal/adapter/cron/scm_poll.go` 必须注册 SCM 轮询任务，调用 `case/project` 下的 `PollScmUseCase`，禁止在 bootstrap 中直接调用 `Services.Scm.PollOnce`

