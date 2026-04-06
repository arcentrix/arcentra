# pkg-restructuring 规范

## 目的
待定 - 由归档变更 migrate-to-ddd-layered-arch 创建。归档后请更新目的。
## 需求
### 需求:pkg 按职责分为 8 个分组目录

`pkg/` 下的所有包必须按职责归入以下 8 个分组目录：`foundation`（纯工具）、`telemetry`（可观测性）、`store`（数据存储）、`transport`（传输层）、`message`（消息与事件）、`engine`（CI/CD 引擎）、`integration`（外部集成）、`lifecycle`（生命周期）。迁移完成后，`pkg/` 顶层禁止存在未分组的包目录。

#### 场景:foundation 包含所有纯工具

- **当** 查看 `pkg/foundation/` 目录
- **那么** 必须包含以下子包：`env`、`id`、`net`、`num`、`safe`、`serde`、`time`、`util`、`version`、`retry`、`parallel`、`ringbuffer`、`loop`、`orderly`、`request`，且这些包禁止依赖 `pkg/` 下其他分组

#### 场景:engine 包含 CI/CD 引擎核心

- **当** 查看 `pkg/engine/` 目录
- **那么** 必须包含：`dag`、`dispatch`、`runner`、`sandbox`、`statemachine`、`taskqueue`、`logstream`、`record`

#### 场景:integration 包含外部集成

- **当** 查看 `pkg/integration/` 目录
- **那么** 必须包含：`scm`（含 github/gitlab/gitea/gitee/bitbucket 子包）、`sso`（含 ldap/oauth/oidc 子包）、`plugin`、`plugins`（含 git/svn 子包）、`agent`

#### 场景:顶层无残留包

- **当** 迁移完成后列出 `pkg/` 下的直接子目录
- **那么** 只允许出现 8 个分组目录名（foundation、telemetry、store、transport、message、engine、integration、lifecycle），禁止出现未分组的散落包

### 需求:telemetry 合并 log 与 logger

`pkg/log/` 和 `pkg/logger/` 必须合并为 `pkg/telemetry/log/`。合并后的包必须保留两者的公共 API，通过 type alias 或 wrapper 保持向后兼容。`pkg/telemetry/` 还必须包含 `metrics/`、`trace/`（含 context/inject 子包）、`pprof/`。

#### 场景:log 与 logger 合并后 API 兼容

- **当** 原 `pkg/log` 或 `pkg/logger` 的调用方更新 import 路径后
- **那么** 所有公共函数（如 `log.Info`、`log.Errorw`、`logger.New` 等）必须在 `pkg/telemetry/log/` 中可用，禁止出现编译错误

#### 场景:trace 子包结构保留

- **当** 查看 `pkg/telemetry/trace/`
- **那么** 必须保留 `context/` 和 `inject/` 子包，功能不变

### 需求:store 分组提供 Wire ProviderSet

`pkg/store/database/` 和 `pkg/store/cache/` 必须各自保留现有的 Wire ProviderSet，仅更新包路径。ProviderSet 的接口绑定（如 `IDatabase`、`ICache`）必须保持不变。

#### 场景:database ProviderSet 路径更新

- **当** Wire 构建 `cmd/arcentra/wire.go`
- **那么** 必须引用 `pkg/store/database.ProviderSet` 替代原 `pkg/database.ProviderSet`，绑定的接口和实现类型不变

### 需求:message 分组包含 nova

`pkg/message/` 必须包含 `mq/`（含 kafka/rocketmq）、`event/`、`outbox/`、`nova/`。`nova` 包（任务队列/Broker/聚合器）的内部结构和 API 保持不变，仅变更包路径。

#### 场景:nova 包功能完整保留

- **当** 将 `pkg/nova/` 迁移到 `pkg/message/nova/`
- **那么** 所有导出类型（Broker、TaskQueue、Aggregator 等）和方法签名必须保持不变，仅 import 路径变更

### 需求:import 路径批量替换

每个分组的迁移必须使用自动化工具（`goimports` + `sed` 或 `gorename`）批量替换全仓库的 import 路径。禁止手动逐文件修改 import。替换完成后必须通过 `go build ./...` 验证。

#### 场景:foundation 分组迁移后编译通过

- **当** 将 `pkg/env`、`pkg/id`、`pkg/safe` 等迁移到 `pkg/foundation/` 下并批量替换 import
- **那么** `go build ./...` 必须零错误通过

#### 场景:一个分组一个 PR

- **当** 执行 `pkg/` 重组
- **那么** 每个分组的迁移必须作为独立 PR 提交，禁止在单个 PR 中迁移多个分组（foundation 除外，因其无外部依赖可作为首批）

### 需求:分组间依赖方向约束

分组之间必须遵循依赖方向：`foundation` 不依赖任何其他分组；`telemetry` 仅可依赖 `foundation`；`store` 可依赖 `foundation` 和 `telemetry`；`transport` 可依赖 `foundation` 和 `telemetry`；`message` 可依赖 `foundation`、`telemetry` 和 `store`；`engine` 可依赖 `foundation`；`integration` 可依赖 `foundation`、`telemetry`、`store` 和 `transport`；`lifecycle` 可依赖 `foundation` 和 `telemetry`。

#### 场景:foundation 零内部依赖

- **当** 检查 `pkg/foundation/` 下任何包的 import
- **那么** 禁止出现 `pkg/telemetry`、`pkg/store`、`pkg/transport`、`pkg/message`、`pkg/engine`、`pkg/integration`、`pkg/lifecycle` 的引用

#### 场景:engine 不依赖 store

- **当** 检查 `pkg/engine/` 下任何包的 import
- **那么** 禁止出现 `pkg/store/`、`pkg/integration/` 的引用；引擎核心算法必须是纯逻辑，不依赖具体存储

