## ADDED Requirements

### 需求:通知领域模型定义在 domain 层

通知相关的领域模型（`NotificationChannelModel`、`NotificationTemplateModel`、`ChannelConfig`、`ChannelType`、`Template`）和仓储接口（`INotificationChannelRepo`、`INotificationTemplateRepo`、`ITemplateRepository`）必须定义在 `internal/domain/notification/` 中，遵循现有 bounded context 的五文件模式（model.go、repository.go、service.go、event.go、consts.go）。

#### 场景:notification 领域目录结构
- **当** 检查 `internal/domain/notification/` 目录
- **那么** 存在 `model.go`、`repository.go`、`service.go`、`event.go`、`consts.go` 五个文件

#### 场景:通知仓储接口在 domain 层
- **当** 查找 `INotificationChannelRepo` 接口的定义
- **那么** 它位于 `internal/domain/notification/repository.go`

#### 场景:通知模型在 domain 层
- **当** 查找 `NotificationChannelModel` 的定义
- **那么** 它位于 `internal/domain/notification/model.go`

### 需求:通知编排逻辑位于用例层

`Manager`（通知管理器，负责根据事件类型选择渠道、渲染模板、发送通知的编排逻辑）必须位于 `internal/case/notification/` 包中。

#### 场景:通知 Manager 位置
- **当** 查找通知 `Manager` 的定义
- **那么** 它位于 `internal/case/notification/` 目录下

#### 场景:Manager 仅依赖领域层
- **当** 检查通知 Manager 的 import 列表
- **那么** 它仅导入 `internal/domain/notification` 和标准库/pkg 工具，不导入任何 infra 层包

### 需求:通知渠道实现位于基础设施层

所有具体通知渠道实现（`SlackChannel`、`TelegramChannel`、`DiscordChannel`、`WebhookChannel`、`DingTalkChannel`、`EmailChannel`、`FeishuCardChannel`、`LarkCardChannel`、`WeComChannel`、`FeishuAppChannel`、`LarkAppChannel`）必须位于 `internal/infra/notification/` 包中。

#### 场景:Slack 渠道实现位于 infra
- **当** 查找 `SlackChannel` 结构体的定义
- **那么** 它位于 `internal/infra/notification/` 目录下

#### 场景:所有渠道实现位于 infra
- **当** 列举所有实现 `INotifyChannel` 接口的结构体
- **那么** 它们均位于 `internal/infra/notification/` 目录下

#### 场景:渠道认证 provider 位于 infra
- **当** 查找 `IAuthProvider` 及其实现（TokenAuth、BearerAuth 等）
- **那么** 它们位于 `internal/infra/notification/auth/` 目录下

### 需求:internal/pkg/notify 完全移除

迁移完成后 `internal/pkg/notify` 目录必须不存在或为空。

#### 场景:pkg/notify 不再存在
- **当** 检查 `internal/pkg/notify/` 目录
- **那么** 该目录不存在
