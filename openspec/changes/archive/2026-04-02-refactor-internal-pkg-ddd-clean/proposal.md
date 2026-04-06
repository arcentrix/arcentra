## 为什么

`internal/pkg` 当前承载了大量领域逻辑、基础设施实现和应用编排代码，违反了 DDD + Clean Architecture 的依赖规则：executor 向上依赖 `internal/control/config`，storage 反向耦合 `internal/domain/agent`，notify 内部自建了仓储接口与持久化模型。这些问题导致层间边界模糊，模块难以独立测试和替换。当前重构分支（`refactor/domain-event-arch`）已建立 `internal/domain`、`internal/case`、`internal/infra`、`internal/adapter` 分层体系，需要将 `internal/pkg` 中错位的代码归位到正确的层级，使整体架构一致。

## 变更内容

- **拆解 `internal/pkg/executor`**：将事件发布（EventPublisher/EventEmitter/KafkaPublisher）移至 `internal/infra/messaging`；将 `PipelineAdapter` 与依赖 `AppConfig` 的工厂函数移至 `internal/adapter` 或 `internal/infra`；executor 核心接口与 Manager 保留为纯领域/共享内核
- **拆解 `internal/pkg/storage`**：将 `IStorage` 接口定义移至 `internal/domain/agent`（或独立 storage domain）；将各云存储实现（S3/OSS/MinIO/GCS/COS）移至 `internal/infra/storage`；将 Wire ProviderSet 移至组合根
- **拆解 `internal/pkg/notify`**：将 `INotificationChannelRepo`、`INotificationTemplateRepo` 及模型移至 `internal/domain/notification`；将渠道实现（Slack/Telegram/DingTalk 等）移至 `internal/infra/notification`；将 `Manager`（编排逻辑）移至 `internal/case/notification`
- **拆解 `internal/pkg/pipeline`**：将流水线引擎核心类型（Context/Executor/Runner/StepRunner/Task/Reconciler）识别为领域服务，归入 `internal/domain/execution` 或 `internal/domain/pipeline`；将 builtin action handler 移至 `internal/infra/pipeline/builtin`；spec/validation 保留为共享内核
- **精简 `internal/pkg`**：仅保留真正的无状态工具包——`convert`、`dsl`、`prefixtree`、`sse`、`pipeline/spec`、`pipeline/validation`、`pipeline/interceptor`
- **BREAKING**：所有从 `internal/pkg` 导出的公共类型路径变更，所有引用方（`internal/agent`、`internal/control`、`cmd/`）需同步更新 import

## 功能 (Capabilities)

### 新增功能
- `executor-layer-split`: 将 executor 包按职责拆分为领域接口、基础设施实现和适配器三层
- `storage-layer-split`: 将 storage 包按职责拆分为领域接口（端口）和基础设施实现（适配器）
- `notify-layer-split`: 将 notify 包按职责拆分为领域模型/接口、用例编排和基础设施渠道实现
- `pipeline-layer-split`: 将 pipeline 引擎核心归位到领域层，builtin handler 归位到基础设施层

### 修改功能

## 影响

- **代码路径**：`internal/pkg/executor`、`internal/pkg/storage`、`internal/pkg/notify`、`internal/pkg/pipeline` 下所有文件需要迁移或重组
- **依赖方**：`internal/agent/bootstrap`、`internal/agent/taskqueue`、`internal/agent/outbox`、`internal/control/bootstrap`、`cmd/arcentra/wire.go`、`cmd/arcentra-agent/wire.go` 需同步更新 import 路径
- **Wire 注入**：`storage.ProviderSet`、`notify.ProviderSet`、`executor` 相关 Provider 需迁移到对应层级的 provider.go
- **API 无变化**：gRPC/HTTP 对外接口不受影响
- **数据库无变化**：无 schema 变更
