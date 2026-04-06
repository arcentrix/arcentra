## 1. Executor 层拆分

- [x] 1.1 在 `internal/pkg/executor` 中提取 `EventPublisher` 接口定义（如尚未存在独立接口），确保 `KafkaPublisher`/`MultiPublisher`/`LogPublisher` 等实现与接口解耦
- [x] 1.2 创建 `internal/infra/messaging/` 包，将 `publisher_kafka.go`（KafkaPublisher、KafkaTopicPublisher）、`event_publisher.go`（MultiPublisher）、`log_publisher.go`（KafkaLogPublisher、LogPublisher）从 `internal/pkg/executor/` 移入
- [x] 1.3 在 `internal/infra/messaging/` 中创建 `provider.go`，定义 Wire ProviderSet
- [x] 1.4 从 `internal/pkg/executor/event_provider.go` 中移除 `NewEventPublisherFromConfig`、`BuildEventEmitterConfig` 中对 `config.AppConfig` 的直接依赖，改为接受 `EventEmitterConfig` 和 `KafkaConfig` DTO
- [x] 1.5 从 `internal/pkg/executor/pipeline_adapter.go` 中移除 `NewExecutorManagerWithDefaultsAndEvents` 对 `config.AppConfig` 的依赖，将配置转换逻辑移至 `internal/agent/bootstrap/` 或 `cmd/` 层
- [x] 1.6 更新 `internal/agent/bootstrap/bootstrap.go`、`internal/agent/outbox/`、`internal/agent/taskqueue/` 中对 executor 和 messaging 的 import 路径
- [x] 1.7 更新 `cmd/arcentra-agent/wire.go` 的 ProviderSet 引用，确保注入路径正确
- [x] 1.8 运行 `go build ./...` 验证编译通过

## 2. Storage 层拆分

- [x] 2.1 将 `IStorage` 接口定义从 `internal/pkg/storage/storage_interface.go` 移至 `internal/domain/agent/repository.go`（或新建 `storage.go`）
- [x] 2.2 将 `storage_s3.go`、`storage_oss.go`、`storage_minio.go`、`storage_gcs.go`、`storage_cos.go` 从 `internal/pkg/storage/` 移至 `internal/infra/storage/`
- [x] 2.3 将 `storage.go` 中的 `DbProvider`、`NewStorageDBProvider`、`RefreshStorageConfig` 等运行时管理逻辑移至 `internal/infra/storage/`
- [x] 2.4 将 `provider.go` 中的 `ProvideStorageFromDB` 和 `ProviderSet` 移至 `internal/infra/storage/provider.go`（合并已有的 `adapter.go` 逻辑）
- [x] 2.5 删除 `internal/pkg/storage/` 目录
- [x] 2.6 更新 `internal/control/bootstrap/bootstrap.go`、`cmd/arcentra/wire.go` 中的 storage import 路径
- [x] 2.7 更新 `internal/infra/storage/adapter.go` 中对 `IStorage` 的引用指向 domain 层
- [x] 2.8 运行 `go build ./...` 验证编译通过

## 3. Notify 层拆分

- [x] 3.1 创建 `internal/domain/notification/` 目录，按五文件模式创建 `model.go`、`repository.go`、`service.go`、`event.go`、`consts.go`
- [x] 3.2 将 `NotificationChannelModel`、`ChannelConfig`、`ChannelType`、`NotificationTemplateModel`、`Template` 等模型定义移至 `internal/domain/notification/model.go`
- [x] 3.3 将 `INotificationChannelRepo`、`INotificationTemplateRepo`、`ITemplateRepository`、`ChannelRepository` 等接口移至 `internal/domain/notification/repository.go`
- [x] 3.4 创建 `internal/infra/notification/` 目录，将 `internal/pkg/notify/channel/` 中所有渠道实现（slack.go、telegram.go、dingtalk.go、discord.go、webhook.go、email.go、feishu_card.go、lark_card.go、wecom.go、feishu_app.go、lark_app.go）移入
- [x] 3.5 将 `internal/pkg/notify/auth/` 移至 `internal/infra/notification/auth/`
- [x] 3.6 将 `internal/pkg/notify/channel_repository_adapter.go`（`ChannelRepositoryAdapter`）移至 `internal/infra/notification/`
- [x] 3.7 创建 `internal/case/notification/` 目录，将 `internal/pkg/notify/manager.go`（Manager 编排逻辑）移入，重命名为用例层代码
- [x] 3.8 将 `internal/pkg/notify/template/` 中 `DatabaseTemplateRepository` 移至 `internal/infra/notification/template/`；模板引擎（Engine、predefined）评估后决定保留位置
- [x] 3.9 将 `internal/pkg/notify/provider.go` 中的 Wire ProviderSet 拆分到 `internal/case/notification/provider.go` 和 `internal/infra/notification/provider.go`
- [x] 3.10 删除 `internal/pkg/notify/` 目录
- [x] 3.11 更新所有引用 notify 的外部代码（`internal/control/bootstrap`、`cmd/arcentra/wire.go` 等）的 import 路径
- [x] 3.12 运行 `go build ./...` 验证编译通过

## 4. Pipeline Builtin 层拆分

- [x] 4.1 创建 `internal/infra/pipeline/builtin/` 目录
- [x] 4.2 将 `internal/pkg/pipeline/builtin/` 中所有文件（manager.go、shell.go、stdout.go、artifacts.go、reports.go、scm.go、types.go）移至 `internal/infra/pipeline/builtin/`
- [x] 4.3 确保 builtin Manager 通过 `ActionHandler` 接口注册到 pipeline 引擎，不引入从 `internal/pkg/pipeline` 到 `internal/infra` 的反向依赖
- [x] 4.4 在 `internal/infra/pipeline/` 创建 `provider.go` 定义 Wire ProviderSet
- [x] 4.5 删除 `internal/pkg/pipeline/builtin/` 目录
- [x] 4.6 更新 `internal/pkg/dsl/` 中对 builtin 的引用（如有）
- [x] 4.7 更新 `cmd/` 层的 Wire 注入
- [x] 4.8 运行 `go build ./...` 验证编译通过

## 5. 清理与验证

- [x] 5.1 确认 `internal/pkg/` 仅保留：`convert/`、`dsl/`、`executor/`（纯接口+Manager）、`gateway/`、`pipeline/`（核心+spec+validation+interceptor+definition）、`prefixtree/`、`sse/`
- [x] 5.2 运行 `go vet ./...` 检查代码质量
- [x] 5.3 运行 `go test ./...` 确保所有现有测试通过
- [x] 5.4 检查 `internal/pkg/executor` 无 `internal/control/config` import
- [x] 5.5 检查 `internal/pkg/` 下无 `internal/domain` import（共享内核不应反向依赖领域层，storage 接口已迁入 domain）
- [x] 5.6 确认各层依赖方向正确：domain ← case ← infra/adapter，pkg 作为共享内核被各层引用但不反向依赖
