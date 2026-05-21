# Wire DI Review

## Purpose

审查 PR 是否正确使用 / 修改 Google Wire 编译时依赖注入（`wire.go` + 生成的 `wire_gen.go`）。

## Context

- 控制平面入口：`cmd/arcentra/wire.go`、`cmd/arcentra/wire_gen.go`
- Agent 入口：`cmd/arcentra-agent/wire.go`、`cmd/arcentra-agent/wire_gen.go`
- 每个包在 `provider.go` 中定义 `ProviderSet`：`config/provider.go`、`repo/provider.go`、`service/provider.go`、`router/provider.go`、`process/provider.go`、`bootstrap/provider.go` 等
- 生成命令：`make wire`（或 `make codegen`）

## Task

### 1. ProviderSet 定义

- 新增包是否在 `provider.go` 中暴露 `ProviderSet`？
- ProviderSet 命名是否一致（`var ProviderSet = wire.NewSet(...)`）？
- 是否在 ProviderSet 中聚合本包所有 Provider 函数？
- 是否使用 `wire.Bind(new(IXxxRepository), new(*xxxRepository))` 绑定接口到实现？
- 是否使用 `wire.Struct(new(Repositories), "*")` 聚合多个 Repository？

### 2. wire.go 修改

- 是否在 `wire.Build(...)` 中正确引入新 ProviderSet？
- 是否避免重复 import 同一 ProviderSet（Wire 会报冲突）？
- 是否避免循环依赖（Wire 编译期报错，但 PR 应注意调整层次）？
- build constraints `//go:build wireinject` 是否保留？

### 3. wire_gen.go

- 是否执行 `make wire`（或 `make codegen`）重新生成？
- `wire_gen.go` 顶部 `//go:build !wireinject` 标记是否保留？
- `wire_gen.go` 是否提交到仓库？
- 是否避免手动编辑 `wire_gen.go`？

### 4. Provider 函数

- Provider 函数签名是否清晰（`func NewXxxService(deps...) IXxxService`）？
- Provider 是否返回接口（`IXxxService`）而非具体类型（`*XxxService`），便于解耦？
  - 例外：底层类型（如 `*gorm.DB`）可以返回具体类型
- 是否避免在 Provider 内做大量初始化（应该在返回的对象上做 lazy init）？
- 是否避免 Provider 返回 `error` 后没有走 `wire.NewSet` 的错误处理？

### 5. 依赖图

- 依赖方向是否单向（router → service → repo → pkg）？
- 是否避免内层反向依赖外层？
- Wire 注入时是否避免「巨型聚合 struct」（一个 struct 注入 30 个字段，难以测试 / 维护）？
- 测试时是否容易 mock（接口注入而非具体类型）？

### 6. 生命周期

- 单例：DB 连接 / Kafka 客户端 / Redis 客户端 应该是进程级单例
- 请求级对象：不应该通过 Wire 注入（用参数传递 `ctx` / 请求对象）
- Wire 不管理对象 `Close()`，需要在 `bootstrap.Bootstrap` 注册 shutdown 钩子

### 7. 测试

- 单元测试是否手动构造依赖（不需要 Wire）？
- 集成测试是否用独立 `wire_test.go` + `wire.Build`（如有需要）？

## Constraints

必须遵守：

- 每个包用 `provider.go` 暴露 `ProviderSet`
- 修改 `wire.go` 或 `provider.go` 后必须 `make wire`
- `wire_gen.go` 必须提交，不允许手编辑
- 使用 `wire.Bind` 绑定接口到实现
- 依赖方向单向（外层 → 内层）

禁止：

- 手编辑 `wire_gen.go`
- 在 Wire build constraints 上做奇怪的事（`//go:build wireinject` 必须存在）
- 通过 Wire 注入请求级对象（如 HTTP request、用户身份）
- 在 Wire ProviderSet 之外 `init()` 全局变量做初始化

## Output Format

1. 总体结论
2. ProviderSet 定义问题
3. wire.go 修改问题
4. wire_gen.go 同步问题（是否执行 `make wire`）
5. 依赖图 / 抽象层次 问题
6. 生命周期 / 单例 / shutdown 问题
7. 推荐 patch / 代码示例

## Checklist

- [ ] `provider.go` 暴露 `ProviderSet`
- [ ] `wire.Bind` 接口 ↔ 实现
- [ ] `wire.go` 引入新 ProviderSet
- [ ] `make wire` 已执行 `wire_gen.go` 已提交
- [ ] 依赖方向单向
- [ ] Provider 返回接口便于测试
- [ ] 单例对象 shutdown 钩子已注册
