# app-bootstrap 规范

## 目的
待定 - 由归档变更 finalize-ddd-adapter-wiring 创建。归档后请更新目的。
## 需求
### 需求:App 结构体必须仅依赖 adapter 和 platform 层

`bootstrap.App` 结构体必须移除对 `internal/control/repo.Repositories` 和 `internal/control/service.Services` 的依赖，仅依赖 `adapter/http.Router`、`adapter/grpc.ServerWrapper`、`platform/config.AppConfig` 和 `pkg/` 层基础设施。

#### 场景:NewApp 参数签名
- **当** `NewApp` 被调用
- **那么** 参数列表中禁止出现 `*repo.Repositories`、`*service.Services`、`*controlrouter.Router` 类型，必须使用 `*adapterhttp.Router` 或 `*fiber.App`

#### 场景:App 字段定义
- **当** `App` 结构体被定义
- **那么** 禁止包含 `Repos` 或 `Services` 字段

### 需求:生命周期管理必须涵盖所有子系统

`App.Run()` 必须按正确顺序启动和关闭所有子系统：Cron、Metrics、gRPC、HTTP，以及优雅关闭时的反向清理。

#### 场景:启动顺序
- **当** `Run()` 被调用
- **那么** 必须按以下顺序启动：Cron 调度器 → Metrics 服务器 → gRPC 服务器 → HTTP 服务器

#### 场景:优雅关闭
- **当** 收到 SIGINT/SIGTERM 信号或 shutdown endpoint 触发
- **那么** 必须按反向顺序关闭：HTTP 服务器（带 30s 超时）→ cleanup 函数（Plugin、gRPC、Cron、OpenTelemetry）

### 需求:SCM 轮询必须从 bootstrap 迁出

SCM 轮询定时任务必须从 `bootstrap.Run()` 迁移到 `internal/adapter/cron/scm_poll.go`，通过 Wire 注入 Cron 适配器。

#### 场景:Cron 适配器注册 SCM 轮询
- **当** `adapter/cron.Adapter` 初始化
- **那么** 必须注册 `*/1 * * * *` 的 SCM 轮询任务，通过 case 层 Use Case 执行 `PollOnce`

