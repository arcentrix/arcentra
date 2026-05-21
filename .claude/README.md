# Claude Code 提示词与上下文

本目录维护 arcentra 项目的 Claude Code 相关提示词、上下文与命令。

## 目录约定

| 目录 | 用途 |
|------|------|
| `prompts/` | 可复用任务提示词，例如代码审查、流水线执行流程审查、proto 兼容性审查、插件系统审查 |
| `commands/` | 短命令式提示词，用于快速触发常见任务 |
| `context/` | 长期项目上下文，例如 Go 编码规范、流水线执行引擎、控制平面与 Agent 架构 |
| `templates/` | 提示词模板 |
| `archive/` | 废弃或历史提示词 |

## 维护原则

- `CLAUDE.md` 只放长期稳定的项目级规则
- 任务型提示词放入 `.claude/prompts/`
- 领域上下文放入 `.claude/context/`
- 新增提示词必须在对应 README 中登记
- 废弃提示词移动到 `.claude/archive/deprecated/`
- 不允许在提示词中写入密钥、token、生产账号、数据库密码等敏感信息
- 修改 proto、Kafka topic、Nova 任务类型、Wire 依赖图、GORM 模型或流水线执行 FSM 时，需要同步更新相关 context 文件
- 不允许让 Claude 在 `pkg/` 中直接访问数据库，也不允许在 service 层直接使用 GORM
- 不允许让 Claude 用裸 `go func` 启动 goroutine，统一走 `safe.SafelyGo`
