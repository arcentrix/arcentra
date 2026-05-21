# Claude Prompts

本目录维护 arcentra 项目的可复用 Claude 提示词。

## Prompt 列表

| 文件 | 用途 |
|------|------|
| `go-code-review.md` | Go 代码审查（通用） |
| `pipeline-execution-review.md` | 流水线执行流程审查（Process → Coordinator → Executor → Task → Reconciler 全链路） |
| `repo-service-layer-review.md` | Repository / Service 分层与领域接口审查 |
| `proto-grpc-compat.md` | proto / gRPC / Kafka 消息兼容性审查 |
| `nova-task-queue-review.md` | Nova 任务队列（Kafka + 时间轮 + 优先级 + 批量聚合）审查 |
| `plugin-system-review.md` | 插件系统（接口、生命周期、Action 路由、TOML 热加载）审查 |
| `notify-channel-review.md` | 通知通道（邮件 / Slack / 钉钉 / 飞书 / Webhook 等 11 通道）审查 |
| `db-migration-review.md` | MySQL 表结构 / 迁移脚本 / GORM 模型审查 |
| `wire-di-review.md` | Wire 依赖注入与 ProviderSet 审查 |

## 维护规则

- 新增 prompt 必须登记到本 README
- 文件名使用 kebab-case
- 每个 prompt 必须包含 Purpose、Context、Task、Constraints、Output Format
- 废弃 prompt 移动到 `.claude/archive/deprecated/`
- 不得包含任何密钥、token、生产密码
