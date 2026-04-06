## 1. 迁移 HTTP 路由组到 adapter/http/

- [x] 1.1 迁移 `router_user.go`：从 `control/router/router_user.go` 复制路由定义和 handler 到 `adapter/http/router_user.go`，将 `rt.Services.User.Xxx()` 替换为 `rt.ManageUser.Xxx()`，对齐 DTO 类型
- [x] 1.2 迁移 `router_identity.go`：Identity Provider（OAuth/LDAP/OIDC）相关路由，替换为 `ManageUserUseCase` 中的身份认证方法
- [x] 1.3 迁移 `router_user_ext.go`：用户扩展信息路由，替换为 `ManageUserUseCase` 的扩展方法
- [x] 1.4 迁移 `router_role.go`：角色管理路由（CRUD + 菜单绑定），替换为 `ManageRoleUseCase`
- [x] 1.5 迁移 `router_team.go`：团队管理路由（CRUD + 成员管理），替换为 `ManageTeamUseCase`
- [x] 1.6 迁移 `router_project.go`：项目管理路由（CRUD + 成员 + 团队访问），替换为 `ManageProjectUseCase`
- [x] 1.7 迁移 `router_secret.go`：密钥管理路由，替换为 `ManageSecretUseCase`
- [x] 1.8 迁移 `router_scm.go`：SCM 配置与 webhook 路由，替换为 `ManageSettingsUseCase` 或新增 SCM Use Case
- [x] 1.9 迁移 `router_general_settings.go`：通用设置路由，替换为 `ManageSettingsUseCase`
- [x] 1.10 迁移 `router_pipeline.go`：Pipeline 管理路由（CRUD + 触发），替换为 `ManagePipelineUseCase`
- [x] 1.11 迁移 `router_storage.go`：存储/上传路由，替换为 `UploadUseCase`
- [x] 1.12 迁移 `router_ws.go`：WebSocket 日志流路由，替换为 `ManageStepRunUseCase`
- [x] 1.13 更新 `registerRoutes()` 方法，补全所有 12 个路由组的调用
- [x] 1.14 如果 Use Case 层缺少必要方法（如 SCM PollOnce、WebSocket 相关），在对应 `internal/case/` 中补充
- [x] 1.15 验证 `go build ./internal/adapter/http/...` 编译通过

## 2. 实现 adapter/grpc/ 服务注册

- [x] 2.1 将 `internal/pkg/grpc/grpc_server.go` 中的 `ServerWrapper`（NewGrpcServer、Start、Stop）迁移到 `internal/adapter/grpc/server.go`，改构造函数参数为 Use Case 依赖
- [x] 2.2 将 `internal/pkg/grpc/interceptor/` 的 logging.go、auth.go、token_verifier.go 迁移到 `internal/adapter/grpc/interceptor/`
- [x] 2.3 重构 `AgentTokenVerifier`：将其从依赖 `service.AgentService` + `repo.Repositories` 改为依赖 `case/agent` Use Case 或 domain 层接口
- [x] 2.4 迁移 `service_agent_pb.go`（AgentServiceImpl）到 `adapter/grpc/service_agent.go`，依赖 `case/agent` Use Case
- [x] 2.5 迁移 `service_gateway_pb.go`（GatewayServiceImpl）到 `adapter/grpc/service_gateway.go`
- [x] 2.6 迁移 `service_pipeline_pb.go`（PipelineServiceImpl）到 `adapter/grpc/service_pipeline.go`，依赖 `case/pipeline` Use Case
- [x] 2.7 迁移 `service_steprun_pb.go`（StepRunServiceImpl）到 `adapter/grpc/service_steprun.go`，依赖 `case/execution` Use Case
- [x] 2.8 迁移 StreamService 到 `adapter/grpc/service_stream.go`，改为依赖 case 层 Use Case（解除对 redis.Client、gorm.DB 的直接依赖）
- [x] 2.9 更新 `adapter/grpc/provider.go`，将 `ProviderSet` 从空集改为包含 `NewServerWrapper` + 所有服务实现构造函数
- [x] 2.10 迁移 `grpc_client.go`（ClientWrapper）到 `adapter/grpc/client.go`（如果控制面需要 gRPC 客户端）
- [x] 2.11 验证 `go build ./internal/adapter/grpc/...` 编译通过

## 3. 创建新 bootstrap

- [x] 3.1 重构 `internal/control/bootstrap/bootstrap.go` 中的 `App` 结构体，移除 `Repos`、`Services` 字段，改 `NewApp` 参数为 `*adapterhttp.Router` + `*adaptergrpc.ServerWrapper`
- [x] 3.2 将 `Run()` 中的 SCM 轮询 cron 任务迁移到 `internal/adapter/cron/scm_poll.go`，实现 `CronAdapter` 结构体并在 Wire 中注册
- [x] 3.3 更新 `cleanup` 函数，确保按正确顺序关闭 Metrics、Plugin、gRPC、Cron、OpenTelemetry
- [x] 3.4 验证 `App.Run()` 的启动顺序：Cron → Metrics → gRPC → HTTP → wait signal → graceful shutdown

## 4. 切换 internal/pkg/storage 到 domain 层

- [x] 4.1 在 `internal/domain/agent/repository.go` 中确认或补充存储相关接口方法（如果 `IStorageRepository` 尚不完整）
- [x] 4.2 创建 `internal/infra/storage/adapter.go`，实现适配器将 `internal/pkg/storage.IStorage` 包装为 domain 层存储接口
- [x] 4.3 创建 `internal/infra/storage/provider.go`，提供 Wire ProviderSet（含 `wire.Bind` 绑定到 domain 接口）
- [x] 4.4 验证 5 种云厂商后端（S3、MinIO、OSS、COS、GCS）都能通过适配器正确工作
- [x] 4.5 确认 Agent 端对 `internal/pkg/storage` 的引用不受影响

## 5. 切换 internal/pkg/notify 到 domain 层

- [x] 5.1 在 `internal/domain/` 中确认或定义 `INotificationChannelRepository` 和 `INotificationTemplateRepository` 接口
- [x] 5.2 修改 `internal/pkg/notify/provider.go` 的 `ProvideNotifyManager`，将参数从 `*repo.Repositories` 改为 domain 层通知仓储接口
- [x] 5.3 修改 `ChannelRepositoryAdapter` 适配 domain 层接口而非 control 层 repo
- [x] 5.4 验证编译通过且通知功能不受影响

## 6. 重写 wire.go + 重新生成 wire_gen.go

- [x] 6.1 重写 `cmd/arcentra/wire.go`：import 路径全部切换到 `internal/infra/persistence`、`internal/case/*`、`internal/adapter`；Build 链按 platform → infra → case → adapter → bootstrap 排列
- [x] 6.2 运行 `wire gen ./cmd/arcentra` 生成 `wire_gen.go`，修复所有类型不匹配和缺失 provider 的问题
- [x] 6.3 更新 `cmd/arcentra-agent/wire.go`，适配新包路径（如有引用 `internal/control/` 或 `internal/pkg/grpc/`）
- [x] 6.4 运行 `wire gen ./cmd/arcentra-agent` 重新生成 Agent 端 `wire_gen.go`
- [x] 6.5 验证 `go build ./cmd/arcentra` 和 `go build ./cmd/arcentra-agent` 编译通过

## 7. 删除旧代码 + 最终验证

- [x] 7.1 执行 `grep -r "internal/control" --include="*.go"` 确认无残留引用（排除 bootstrap 如保留）
- [x] 7.2 执行 `grep -r "internal/pkg/grpc" --include="*.go"` 确认无残留引用
- [x] 7.3 删除 `internal/control/router/`、`internal/control/service/`、`internal/control/repo/`、`internal/control/model/`、`internal/control/config/`、`internal/control/consts/`
- [x] 7.4 删除 `internal/pkg/grpc/`
- [x] 7.5 运行 `go build ./...` 验证编译通过
- [x] 7.6 运行 `go test ./...` 验证测试通过
- [x] 7.7 运行 `go build ./internal/...` + `go build ./pkg/...` 验证所有包编译通过（二进制需 wire gen 后完成）
