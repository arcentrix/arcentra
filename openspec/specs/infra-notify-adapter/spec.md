# infra-notify-adapter 规范

## 目的
待定 - 由归档变更 finalize-ddd-adapter-wiring 创建。归档后请更新目的。
## 需求
### 需求:通知管理器必须解耦 control/repo 依赖

`internal/pkg/notify/` 的 `ProvideNotifyManager` 必须不再依赖 `*repo.Repositories`（control 层上帝对象），改为依赖 domain 层定义的通知渠道仓储接口。

#### 场景:ProvideNotifyManager 参数变更
- **当** `ProvideNotifyManager` 被调用
- **那么** 必须接收 domain 层的 `INotificationChannelRepository` 接口，禁止接收 `*repo.Repositories`

#### 场景:ChannelRepositoryAdapter 适配 domain 接口
- **当** `ChannelRepositoryAdapter` 被创建
- **那么** 必须适配 domain 层通知渠道仓储接口，而非 control 层的 `repo.NotificationChannelRepo`

### 需求:通知模板仓储必须对接 domain 层

如果存在通知模板的持久化需求，模板仓储必须依赖 domain 层定义的模板仓储接口。

#### 场景:模板仓储接口绑定
- **当** 通知模板服务初始化
- **那么** 必须通过 domain 层接口访问模板数据，禁止直接引用 `control/repo`

