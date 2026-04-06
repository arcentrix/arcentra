## ADDED Requirements

### 需求:Executor 核心接口独立于应用配置

executor 包（`internal/pkg/executor`）中的核心类型（`Executor` 接口、`Manager`、`ExecutionRequest`、`ExecutionResult`、`ShellExecutor`、`UnifiedExecutor`、`PluginExecutor`、`HTTPExecutor`）必须不依赖 `internal/control/config` 包。所有配置注入必须通过已解析的 DTO 结构体传入。

#### 场景:executor 包无 control/config import
- **当** 检查 `internal/pkg/executor` 目录下所有 `.go` 文件的 import 列表
- **那么** 不存在任何对 `internal/control/config` 的导入

#### 场景:ExecutorManager 通过 DTO 接受配置
- **当** 创建带事件发布能力的 `ExecutorManager` 时
- **那么** 工厂函数接受 `EventEmitterConfig` 和 `KafkaConfig` 等已解析的结构体参数，而非 `*config.AppConfig`

### 需求:事件发布实现移至基础设施层

`EventPublisher`、`MultiPublisher`、`KafkaPublisher`、`KafkaTopicPublisher`、`KafkaLogPublisher`、`LogPublisher` 的具体实现必须位于 `internal/infra/messaging/` 包中。`internal/pkg/executor` 仅保留 `EventPublisher` 接口定义。

#### 场景:Kafka 发布者位于 infra 层
- **当** 查找 `KafkaPublisher` 和 `KafkaTopicPublisher` 结构体的定义
- **那么** 它们位于 `internal/infra/messaging/` 目录下

#### 场景:executor 包仅持有发布者接口
- **当** 在 `internal/pkg/executor` 中查找 `EventPublisher`
- **那么** 仅存在接口定义，不存在任何具体实现结构体

#### 场景:LogPublisher 位于 infra 层
- **当** 查找 `LogPublisher` 和 `KafkaLogPublisher` 结构体的定义
- **那么** 它们位于 `internal/infra/messaging/` 目录下

### 需求:PipelineAdapter 配置装配移至组合根

`NewExecutorManagerWithDefaultsAndEvents` 和 `NewEventPublisherFromConfig` 等接受 `AppConfig` 的工厂函数必须从 `internal/pkg/executor` 中移除，移至 `cmd/` 层或 `internal/control/bootstrap` 中的组装代码。

#### 场景:executor 包无 AppConfig 工厂
- **当** 在 `internal/pkg/executor` 中搜索接受 `*config.AppConfig` 参数的函数
- **那么** 搜索结果为空

#### 场景:组合根完成配置转换
- **当** `cmd/arcentra-agent/wire.go` 或 bootstrap 代码构建 ExecutorManager 时
- **那么** 先从 `AppConfig` 提取配置 DTO，再传给 executor 包的工厂函数
