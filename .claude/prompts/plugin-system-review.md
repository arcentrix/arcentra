# Plugin System Review

## Purpose

审查 PR 是否正确实现 / 修改插件系统（基于接口的插件框架、双层注册、动作驱动执行、12 种内置插件类型、TOML 热加载）。

## Context

- 插件框架：`pkg/plugin/`
- 内置插件实现：`pkg/plugins/`
- 插件执行入口：`internal/shared/executor/plugin_executor.go`
- 插件管理：注册 / 初始化 / 健康检查 / 生命周期由 `pkg/plugin` 的 PluginManager 负责
- 配置：TOML 文件支持热加载

## Task

### 1. 插件接口

- 是否符合 `pkg/plugin` 定义的 `Plugin` 接口（`Init` / `Action` / `Health` / `Close` 等）？
- `Action` 路由是否清晰（按 `action` 字符串分发，不同 action 不同 handler）？
- 是否避免在插件接口里塞业务专属方法（保持通用）？
- 接口方法是否接收 `context.Context`？

### 2. 注册机制

- 双层注册：
  - 类型注册：插件类型在 `pkg/plugin/registry.go`（或类似位置）注册
  - 实例注册：用户配置（TOML）触发实例创建并注册到 `PluginManager`
- 是否避免在 `init()` 函数中产生副作用（应该通过 `ProviderSet` + 显式 `Register`）？
- 注册冲突（同名插件 / 同类型重名）是否报错？

### 3. 生命周期

- `Init(ctx, cfg)` 是否处理失败回滚（部分插件 init 失败应不影响其他插件启动）？
- `Health(ctx)` 是否轻量、不阻塞、不依赖外部慢调用？
- `Close(ctx)` 是否优雅释放资源（连接池、文件句柄、子进程）？
- 优雅停机时是否等所有插件 Close 完成？

### 4. Action 执行

- `plugin_executor.go` 调插件时是否传递完整 step 上下文（`pipelineRun` / `jobRun` / `step` / `env` / `inputs`）？
- inputs 校验：必填 / 类型 / 默认值 是否在执行前完成？
- outputs 透传：是否落到 `ExecutionContext` 供下游 step 引用？
- 超时：插件 action 是否带超时？超时是否走 cancel 而不是 kill？
- 错误：业务错误 vs 系统错误 是否分类（exit code / error category）？

### 5. 内置插件类型

12 种内置插件类型应保持完整且独立可测。检查：

- 新增 / 修改某类型时是否影响其他类型？
- 类型枚举是否在统一文件维护？
- 类型对应的默认 TOML 配置是否提供？
- 文档（`docs/`）是否同步？

### 6. TOML 配置热加载

- 监听文件变化（fsnotify / inotify）是否走独立 goroutine（`safe.SafelyGo`）？
- 配置变更时是否做 diff（避免无变化重启）？
- 重新 Init 时是否先 Close 旧实例？
- 是否支持灰度（部分实例先重载）？
- 配置非法（解析失败）时是否保持旧实例运行 + 告警？

### 7. 错误传播

- 插件 panic 是否被框架捕获（`safe.SafelyGo` + recover）？
- 插件错误是否走 CloudEvents 通知下游？
- 是否带 trace span，便于定位失败步骤？

### 8. 安全

- 插件执行外部命令是否走 `pkg/sandbox`？
- 是否避免插件直接读 `os.Environ()`，应该通过框架注入 env？
- 是否禁止插件写任意路径（限制在 workspace / cache 目录）？
- 网络访问是否有 allowlist（防 SSRF）？

### 9. 测试

- 每个插件是否有独立单元测试？
- Action 路由是否有表驱动测试？
- 配置解析 + 热加载是否有集成测试？

## Constraints

必须遵守：

- 插件接口方法接收 `context.Context`
- 插件 goroutine 一律 `safe.SafelyGo`
- 插件错误必须返回 error，不允许 panic 透出（除 init 阶段）
- 插件执行带超时
- 插件 Init / Close 必须幂等
- 配置热加载失败保持旧实例运行

禁止：

- 在 `pkg/plugin` 中直接访问数据库 / Redis（如需在 `pkg/` 定义接口，`internal/control/repo` 实现）
- 在 `init()` 中触发外部 IO
- 插件 panic 透传到 PluginManager
- 插件直接修改全局变量影响其他插件

## Output Format

1. 总体结论
2. 接口设计问题
3. 注册 / 生命周期问题
4. Action 执行 / 超时 / 输出 问题
5. 配置热加载风险
6. 安全 / 沙箱问题
7. 测试覆盖建议
8. 推荐 patch / 代码示例

## Checklist

- [ ] 插件接口方法带 `ctx`
- [ ] 双层注册无冲突
- [ ] `Init` / `Close` 幂等
- [ ] `Health` 轻量
- [ ] Action 带超时
- [ ] 热加载失败保留旧实例
- [ ] 执行外部命令走 sandbox
- [ ] 单测覆盖
