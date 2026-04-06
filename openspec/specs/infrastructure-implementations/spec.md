# infrastructure-implementations 规范

## 目的
待定 - 由归档变更 migrate-to-ddd-layered-arch 创建。归档后请更新目的。
## 需求
### 需求:仓储实现按上下文组织

领域仓储接口的实现必须放在 `internal/infra/persistence/<context>/` 下。每个实现文件必须包含 GORM 持久化模型和仓储实现结构体。仓储实现必须引用 `internal/domain/<context>/` 中定义的接口。

#### 场景:Identity 仓储实现

- **当** 查看 `internal/infra/persistence/identity/`
- **那么** 必须包含 `user_repo.go`（实现 `domain/identity.IUserRepository`）、`role_repo.go`（实现 `domain/identity.IRoleRepository`）等文件，每个文件必须包含对应的 GORM 模型和 `ToDomain()`/`FromDomain()` 转换方法

#### 场景:仓储实现满足接口约束

- **当** 编译 `internal/infra/persistence/identity/` 包
- **那么** 所有导出的仓储结构体必须通过 `var _ domain.IUserRepository = (*UserRepo)(nil)` 方式显式声明接口实现

### 需求:缓存基础设施封装

缓存操作的实现必须放在 `internal/infra/cache/` 下。缓存层必须对应用层透明——应用层通过仓储接口访问数据，缓存作为仓储实现的内部优化策略。

#### 场景:带缓存的用户查询

- **当** `UserRepo.GetByID()` 被调用
- **那么** 仓储实现内部必须先查 Redis 缓存，缓存未命中时查数据库并回填缓存；这一逻辑对 Use Case 和领域层完全透明

### 需求:消息队列生产者封装

MQ 生产者实现必须放在 `internal/infra/mq/` 下。领域层可以定义事件发布接口（如 `IEventPublisher`），由 `infra/mq` 提供 Kafka 实现。

#### 场景:Kafka 生产者实现

- **当** 需要发布领域事件到消息队列
- **那么** `internal/infra/mq/kafka/publisher.go` 必须实现领域层定义的 `IEventPublisher` 接口，负责序列化和发送

### 需求:持久化模型与领域模型的映射

`internal/infra/persistence/<context>/` 下必须提供持久化模型（带 GORM tag）和领域模型之间的双向转换。转换方法必须是持久化模型的方法（`ToDomain()` 返回领域模型，`FromDomain()` 接受领域模型）。

#### 场景:Pipeline 模型转换

- **当** 从数据库查询 Pipeline 记录
- **那么** 仓储实现必须先用 GORM 模型查询，然后调用 `pipelineModel.ToDomain()` 转为领域模型返回；写入时调用 `pipelineModel.FromDomain(domainPipeline)` 后持久化

### 需求:Wire ProviderSet 按层组织

基础设施层必须为每个子模块提供 Wire ProviderSet。所有 ProviderSet 必须将领域接口作为绑定目标（`wire.Bind`），确保注入的是接口而非具体实现。

#### 场景:Persistence ProviderSet

- **当** Wire 构建依赖图
- **那么** `internal/infra/persistence/provider.go` 必须包含所有仓储的 `wire.Bind` 声明（如 `wire.Bind(new(domain.IUserRepository), new(*identity.UserRepo))`）和 `wire.NewSet`

