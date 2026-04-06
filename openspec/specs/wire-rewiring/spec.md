# wire-rewiring 规范

## 目的
待定 - 由归档变更 finalize-ddd-adapter-wiring 创建。归档后请更新目的。
## 需求
### 需求:Wire Build 链必须遵循新分层顺序

`cmd/arcentra/wire.go` 的 `wire.Build` 必须按 `platform → infra → case → adapter → bootstrap` 的顺序组织 ProviderSet，禁止引用 `internal/control/` 或 `internal/pkg/grpc/` 中的 ProviderSet。

#### 场景:wire.go ProviderSet 组成
- **当** `wire.Build` 被执行
- **那么** 必须包含以下 ProviderSet（按顺序）：`config.ProviderSet`、`log.ProviderSet`、`database.ProviderSet`、`cache.ProviderSet`、`metrics.ProviderSet`、`persistence.ProviderSet`、各 case ProviderSet、`adapter.ProviderSet`、`plugin.ProviderSet`、`bootstrap.NewApp`

#### 场景:禁止旧路径引用
- **当** `wire.go` 被编辑完成
- **那么** 文件中禁止出现 `internal/control/repo`、`internal/control/service`、`internal/control/router`、`internal/pkg/grpc` 的 import

### 需求:wire_gen.go 必须成功生成

运行 `wire gen ./cmd/arcentra` 必须成功，生成的 `wire_gen.go` 必须能通过编译。

#### 场景:Wire 生成成功
- **当** 执行 `wire gen ./cmd/arcentra`
- **那么** 必须无错误退出，生成的 `wire_gen.go` 中所有类型和构造函数引用必须正确

### 需求:Agent 端 wire.go 同步更新

`cmd/arcentra-agent/wire.go` 必须适配新的包路径（如果有引用 `internal/control/` 或 `internal/pkg/grpc/` 的地方），确保 Agent 二进制也能编译。

#### 场景:Agent wire 编译通过
- **当** 执行 `go build ./cmd/arcentra-agent`
- **那么** 必须编译成功，无 import 错误

