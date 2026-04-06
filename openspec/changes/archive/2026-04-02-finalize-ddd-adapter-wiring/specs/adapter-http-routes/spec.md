## ADDED Requirements

### 需求:HTTP 路由组必须依赖 case 层 Use Case

`internal/adapter/http/` 下的每个路由处理文件必须仅依赖 `internal/case/` 层的 Use Case 结构体，禁止直接引用 `internal/control/service`、`internal/control/repo` 或任何基础设施层类型。

#### 场景:Identity 路由组迁移
- **当** `router_user.go`、`router_identity.go`、`router_user_ext.go` 从 `control/router/` 迁移到 `adapter/http/`
- **那么** 所有 handler 函数必须调用 `ManageUserUseCase` 的方法，禁止出现对 `service.UserService` 或 `repo.UserRepo` 的引用

#### 场景:RBAC 路由组迁移
- **当** `router_role.go` 从 `control/router/` 迁移到 `adapter/http/`
- **那么** 所有 handler 函数必须调用 `ManageRoleUseCase` 的方法

#### 场景:Team 路由组迁移
- **当** `router_team.go` 从 `control/router/` 迁移到 `adapter/http/`
- **那么** 所有 handler 函数必须调用 `ManageTeamUseCase` 的方法

#### 场景:Project 路由组迁移
- **当** `router_project.go`、`router_secret.go`、`router_scm.go`、`router_general_settings.go` 从 `control/router/` 迁移到 `adapter/http/`
- **那么** 所有 handler 函数必须分别调用 `ManageProjectUseCase`、`ManageSecretUseCase`、`ManageSettingsUseCase` 的方法

#### 场景:Pipeline 路由组迁移
- **当** `router_pipeline.go` 从 `control/router/` 迁移到 `adapter/http/`
- **那么** 所有 handler 函数必须调用 `ManagePipelineUseCase` 的方法

#### 场景:Storage 路由组迁移
- **当** `router_storage.go` 从 `control/router/` 迁移到 `adapter/http/`
- **那么** handler 函数必须通过 `UploadUseCase` 处理上传操作

#### 场景:WebSocket 路由组迁移
- **当** `router_ws.go` 从 `control/router/` 迁移到 `adapter/http/`
- **那么** WebSocket handler 必须调用 `ManageStepRunUseCase` 或等效的 execution 层 Use Case

### 需求:路由注册必须在 registerRoutes 中统一调用

`Router.registerRoutes()` 方法必须注册所有 12 个路由组，每个路由组对应一个独立的方法调用。

#### 场景:所有路由组注册
- **当** `registerRoutes(api)` 被调用
- **那么** 以下 12 个路由方法必须全部被调用：`agentRoutes`、`userRoutes`、`identityRoutes`、`userExtRoutes`、`roleRoutes`、`teamRoutes`、`projectRoutes`、`secretRoutes`、`scmRoutes`、`generalSettingsRoutes`、`pipelineRoutes`、`storageRoutes`，以及 WebSocket 相关路由

### 需求:HTTP API 路径和行为保持不变

迁移后所有 HTTP 端点的 URL 路径、请求/响应 JSON 结构、状态码和中间件链必须与迁移前完全一致。

#### 场景:API 端点路径不变
- **当** 迁移完成后
- **那么** 所有 `/api/v1/` 下的端点路径必须与 `internal/control/router/` 中定义的路径完全匹配

#### 场景:中间件链保持不变
- **当** 迁移完成后
- **那么** JWT 认证、RBAC 授权、CORS、i18n 等中间件必须应用于与迁移前相同的路由组
