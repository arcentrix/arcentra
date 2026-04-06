## ADDED Requirements

### 需求:gRPC ServerWrapper 必须在 adapter/grpc/ 中实现

`internal/adapter/grpc/` 必须提供完整的 `ServerWrapper` 结构体，负责创建 gRPC Server、注册服务、配置拦截器、启动和停止。

#### 场景:创建 gRPC 服务器
- **当** `NewServerWrapper` 被调用并传入 gRPC 配置
- **那么** 必须创建 `grpc.Server` 实例，配置 MaxConcurrentStreams，并按正确顺序注册 Stream 和 Unary 拦截器链：OpenTelemetry trace → tags → logging → auth → recovery

#### 场景:注册所有 gRPC 服务
- **当** ServerWrapper 初始化完成
- **那么** 必须注册 5 个 gRPC 服务：`AgentService`、`GatewayService`、`PipelineService`、`StepRunService`、`StreamService`，并启用 reflection

### 需求:gRPC 服务实现必须依赖 case 层

5 个 gRPC 服务实现（`AgentServiceImpl`、`GatewayServiceImpl`、`PipelineServiceImpl`、`StepRunServiceImpl`、`StreamService`）必须从 `internal/control/service/service_*_pb.go` 迁移到 `internal/adapter/grpc/`，改为依赖 `internal/case/` 层的 Use Case。

#### 场景:AgentServiceImpl 依赖迁移
- **当** `AgentServiceImpl` 被实例化
- **那么** 必须通过 `case/agent` 的 Use Case 处理 Agent 注册、心跳、状态更新等 RPC，禁止引用 `control/service.AgentService`

#### 场景:StepRunServiceImpl 依赖迁移
- **当** `StepRunServiceImpl` 被实例化
- **那么** 必须通过 `case/execution` 的 Use Case 处理步骤运行相关 RPC

### 需求:拦截器必须迁移到 adapter/grpc/interceptor/

`internal/pkg/grpc/interceptor/` 中的 logging、auth、token_verifier 拦截器必须迁移到 `internal/adapter/grpc/interceptor/`。

#### 场景:TokenVerifier 依赖解耦
- **当** `AgentTokenVerifier` 被创建
- **那么** 必须依赖 domain 层接口或 case 层 Use Case 进行 Agent token 验证，禁止依赖 `control/service.AgentService` 和 `control/repo.Repositories`

### 需求:gRPC ProviderSet 必须包含完整构造

`adapter/grpc/` 的 `ProviderSet` 必须包含 `NewServerWrapper` 和所有 gRPC 服务实现的构造函数，以支持 Wire 注入。

#### 场景:Wire 可解析 gRPC 依赖
- **当** `wire gen` 被执行
- **那么** `adapter/grpc.ProviderSet` 必须能正确解析所有 gRPC 服务实现及其依赖的 Use Case
