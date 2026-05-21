# Prompt Name

## Purpose

说明这个提示词用于解决什么问题。

## Context

说明 Claude 需要知道的项目上下文，链接到 `.claude/context/*.md` 中的相应文档：

- 编码规范：`.claude/context/go-coding-standards.md`
- 流水线引擎：`.claude/context/pipeline-engine.md`
- 控制平面 / Agent：`.claude/context/control-plane-agent.md`
- Claude 工作流：`.claude/context/claude-workflow.md`

## Task

请完成以下任务：

1. ...
2. ...
3. ...

## Constraints

必须遵守：

- ...
- ...

禁止：

- ...
- ...

## Output Format

请按照以下格式输出：

1. 总体结论
2. 问题列表
3. 修改建议
4. 风险点
5. 测试 / 代码生成建议（`make wire` / `make buf` / `make test`）
6. 可执行 patch / 示例代码

## Checklist

- [ ] 符合 Go 编码规范（`.claude/context/go-coding-standards.md`）
- [ ] Repository 接口 `I` 前缀，方法按业务 ID 暴露
- [ ] 所有 IO/RPC/DB 函数接收 `context.Context`
- [ ] goroutine 走 `safe.SafelyGo`
- [ ] JSON 使用 `sonic`
- [ ] 没有 `SELECT *` / 裸 SQL / 透传 `*gorm.DB`
- [ ] 没有 `fmt.Println` / `println` 残留
- [ ] GoDoc 中文注释完整且无句号结尾
- [ ] 修改 proto 后 `make buf` 已执行
- [ ] 修改 Wire 后 `make wire` 已执行
- [ ] 单元测试覆盖主要分支
