## 为什么

前序变更 `migrate-to-ddd-layered-arch` 已完成领域层（`internal/domain/`）、基础设施层（`internal/infra/persistence/`）、应用层（`internal/case/`）和 `pkg/` 重组的全部工作，但**适配器层只完成了 Agent HTTP 路由一个模板**——其余 11 个路由组虽然在 `internal/adapter/http/router.go` 中注入了 Use Case 却未注册路由，gRPC 适配器是空壳，MQ/WS/Cron 适配器仅有空 `wire.NewSet()`。`cmd/arcentra/wire.go` 仍指向旧的 `internal/control/` 链路，`internal/control/bootstrap/bootstrap.go` 仍是生产引导入口。此外 `internal/pkg/storage` 和 `internal/pkg/notify` 两个基础设施子系统尚未对接到 domain 层接口。

现在所有"内层"就绪，需要一次性**打通适配器→应用层→领域层的完整链路**，切换 Wire DI 组合根到新架构，并删除旧代码，完成整体迁移。

## 变更内容

- 迁移 `internal/control/router/` 中的 12 个路由组（identity、user、user_ext、role、team、project、secret、scm、general_settings、pipeline、storage、ws）到 `internal/adapter/http/`，每个路由文件改为依赖 `internal/case/` 中的 Use Case 而非 `*service.Services`
- 实现 `internal/adapter/grpc/` 的完整服务注册：5 个 gRPC 服务（AgentService、GatewayService、PipelineService、StepRunService、StreamService）+ 拦截器（logging、token_verifier）迁移自 `internal/pkg/grpc/`
- 创建新的 `internal/control/bootstrap/bootstrap.go` 替代（或原地重构为）`App` 结构体 + 生命周期管理，依赖 `internal/adapter/` 提供的 HTTP/gRPC 入口
- 将 `internal/pkg/storage/`（S3、MinIO、OSS、COS、GCS 五种云厂商实现）对接到 `internal/domain/agent/repository.go` 中的 `IStorageRepository` 接口
- 将 `internal/pkg/notify/`（6+ 通知渠道 + 模板系统）对接到 domain 层的通知仓储接口
- 重写 `cmd/arcentra/wire.go` 和 `cmd/arcentra-agent/wire.go`，全部切换到 `platform → infra → case → adapter → bootstrap` 的新 ProviderSet 链路，重新生成 `wire_gen.go`
- **BREAKING**：删除 `internal/control/`（router、service、repo、model、config、bootstrap）和 `internal/pkg/grpc/`，完成新旧代码切换

## 功能 (Capabilities)

### 新增功能
- `adapter-http-routes`: 12 个 HTTP 路由组从 control/router 迁移到 adapter/http，依赖 case 层 Use Case
- `adapter-grpc-services`: gRPC 适配器完整实现，5 个服务注册 + 拦截器 + token verifier
- `app-bootstrap`: 新 App 引导结构体与生命周期管理，统一 HTTP/gRPC/Cron/MQ 启停
- `infra-storage-adapter`: storage 子系统对接 domain 层，支持 5 种云厂商
- `infra-notify-adapter`: notify 子系统对接 domain 层，通知渠道 + 模板仓储
- `wire-rewiring`: Wire DI 组合根全面切换到新架构 ProviderSet
- `legacy-cleanup`: 删除 internal/control/ 和 internal/pkg/grpc/，验证编译+测试通过

### 修改功能

## 影响

- **代码**：`internal/adapter/http/` 新增 12 个路由文件；`internal/adapter/grpc/` 从 stub 变为完整实现；`internal/control/` 和 `internal/pkg/grpc/` 整体删除；`cmd/arcentra/wire.go` 和 `cmd/arcentra-agent/wire.go` 完全重写
- **API**：外部 HTTP/gRPC API 行为不变，内部 Go 包路径变更
- **依赖**：Wire ProviderSet 从 `control.ProviderSet` 切换到 `adapter.ProviderSet + case.ProviderSet + infra.ProviderSet`
- **系统**：bootstrap 生命周期管理重构，SCM 轮询从 bootstrap 迁到 `adapter/cron/`
- **构建**：`wire_gen.go` 需要重新生成，CI pipeline 编译路径不变
