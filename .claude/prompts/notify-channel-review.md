# Notify Channel Review

## Purpose

审查 PR 是否正确实现 / 修改通知系统（11 通道：邮件 / Slack / 钉钉 / 企微 / 飞书（Lark）/ Webhook / 短信 / Telegram 等）。

## Context

- 实现：`internal/shared/notify/`
- 调用点：流水线 Run 终态、Job 失败、审批节点、Agent 离线等业务事件
- 模板：Go template 渲染，按通道差异化
- 配置：每通道在 TOML / DB 中可独立启用 + 凭证

## Task

### 1. 通道接口

- 是否定义统一 `INotifier`（或 `Channel`）接口？方法签名稳定？
- 是否所有通道实现统一接口，便于工厂模式 / 注册？
- 是否支持多实例（同一通道多套配置，按租户 / 项目隔离）？

### 2. 工厂 / 注册

- 通道注册是否走 `ProviderSet`，避免 `init()` 副作用？
- 启动时未配置的通道是否优雅跳过（不报错退出）？
- 凭证缺失（API token 为空）是否在启动阶段 warn，不在发送时 panic？

### 3. 模板渲染

- 是否使用 Go `html/template` 或 `text/template`，按通道选择？
- 模板变量是否文档化（`{{ .RunID }}` / `{{ .Status }}` / `{{ .Pipeline.Name }}` …）？
- 模板渲染失败是否降级（fallback 到默认模板而非整体失败）？
- 是否避免在模板里写复杂业务逻辑（应在 service 层准备好数据）？
- 富文本（Slack block / 飞书卡片 / 钉钉 markdown）模板是否单独拆分？

### 4. 发送

- 是否走 HTTP 客户端封装（`pkg/http` 或 `pkg/request`），带超时 / 重试 / metric？
- 超时 / 重试策略是否合理（避免无限重试导致目标服务过载）？
- 错误处理：4xx 不重试 / 5xx 限次重试 / 网络错误指数退避？
- 是否带 trace 透传（X-Trace-Id header）？

### 5. 凭证管理

- 凭证（token / secret / webhook url）是否从配置 / 环境变量 / Secret Store 读取？
- 是否避免在日志中打印凭证？
- 是否对凭证脱敏（前 4 后 4）？
- 是否支持轮换（凭证变更后无需重启）？

### 6. 异步 / 限流

- 通知发送是否异步（不阻塞业务主流程）？
- 是否通过 `safe.SafelyGo` 启动 sender goroutine？
- 是否有限流（同一目标 / 同一通道 每秒 N 条），防止刷屏？
- 是否聚合：高频事件合并为一条（如「过去 5 分钟有 X 次失败」）？

### 7. 失败兜底

- 单通道失败是否不影响其他通道？
- 整体失败是否落 `t_notify_log` 便于后续排查？
- 是否提供重试机制（cron / 死信队列）？

### 8. 测试

- 每通道是否有 mock HTTP 测试？
- 模板渲染是否有快照测试？
- 限流 / 聚合 是否有时间相关测试（使用 `pkg/time` 注入时钟）？

### 9. 文档

- 新增通道是否更新 `docs/notify/` 配置示例？
- 是否提供 TOML 示例与字段说明？
- 是否说明该通道的限频 / 消息长度限制（钉钉 markdown 长度上限等）？

## Constraints

必须遵守：

- 通知发送异步，不阻塞业务
- goroutine 走 `safe.SafelyGo`
- 单通道失败不影响其他通道
- 凭证从配置 / 环境变量读，不硬编码
- 日志中凭证脱敏
- HTTP 调用带超时

禁止：

- 在主流程同步阻塞等待通知发送
- 在日志中打印完整 token / webhook url
- 通知失败时 panic
- 在模板里调外部服务（DB / HTTP）

## Output Format

1. 总体结论
2. 接口 / 工厂问题
3. 模板渲染问题
4. 发送 / 重试 / 超时 问题
5. 凭证管理问题
6. 异步 / 限流 / 聚合 问题
7. 失败兜底问题
8. 测试与文档建议
9. 推荐 patch / 代码示例

## Checklist

- [ ] 统一接口 + 工厂注册
- [ ] 模板渲染失败降级
- [ ] HTTP 调用带超时 + 合理重试
- [ ] 凭证从配置读 + 日志脱敏
- [ ] 发送异步、`safe.SafelyGo`、限流
- [ ] 单通道失败隔离
- [ ] 落 `t_notify_log` 便于排查
- [ ] 单测覆盖
