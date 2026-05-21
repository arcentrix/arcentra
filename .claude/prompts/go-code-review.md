# Go Code Review

## Purpose

用于审查 arcentra 仓库中的 Go 代码变更（通用代码审查，不针对特定子系统）。

## Context

arcentra 是 Go 1.24+ 编写的 CI/CD 控制平面，由控制平面（`cmd/arcentra`）+ Agent（`cmd/arcentra-agent`）两个进程组成，使用 Google Wire 编译时依赖注入。

编码规范见 `.claude/context/go-coding-standards.md`，控制平面 / Agent 架构见 `.claude/context/control-plane-agent.md`，流水线执行细节见 `.claude/context/pipeline-engine.md`。

## Task

请审查当前代码变更，重点检查：

1. Go 代码风格与可维护性（命名、单一职责、包边界）
2. 错误处理是否合理（不静默吞掉 error、不字符串错误传递、`errors.Is`/`errors.As` 用法、`%w` 包装）
3. `context.Context` 是否一路透传，没有存到结构体字段
4. goroutine 是否一律走 `pkg/safe.SafelyGo`，没有裸 `go` / `go func`
5. JSON 是否统一用 `sonic`，没有 `encoding/json`
6. Repository 接口是否以 `I` 开头，是否只暴露领域语义方法（按业务 `xxx_id` 查询，不按主键 `id`）
7. Service 层是否避免直接使用 `gorm.DB`，统一走 `repo.IXxxRepository`
8. `pkg/` 是否没有直接访问数据库
9. 日志：HTTP/gRPC handler 是否没有打业务日志（业务日志在 service 层），是否没有 `fmt.Println` / `println` 残留
10. GoDoc 中文注释是否齐全（导出标识符），是否没有用句号「。」结尾，是否没有阿拉伯数字序号
11. 是否需要补充单元测试（`go test -race`）
12. golangci-lint（errcheck / staticcheck / unused / gosec）是否会报警
13. Wire ProviderSet：新增 provider 是否登记到对应 `provider.go`，是否需要 `make wire`
14. 是否引入了 `panic`（除 `init` / 不可恢复场景外不允许）

## Constraints

必须遵守：

- Repository 接口必须 `I` 开头，方法第一个参数必须是 `context.Context`
- 业务标识符使用 UUID 字符串（`xxx_id`），不暴露数据库主键 `id`
- 跨包 goroutine 一律 `safe.SafelyGo`
- JSON 一律 `sonic`
- 数据库一律 GORM + 明确字段，禁止 `SELECT *` 与裸 SQL
- 修改 `.proto` 后必须 `make buf`；修改 `wire.go` 或 `provider.go` 后必须 `make wire`

禁止：

- 在 `pkg/` 中 import `internal/`
- 在 `pkg/` 中直接使用 `database/sql` 或 `gorm`
- 在 service 层透传 `*gorm.DB`
- 用 `encoding/json`
- 用 `go func` / 裸 `go`
- 用阿拉伯数字序号注释（`// 1. xxx`）
- 注释中文句号「。」结尾
- 在 service 之外打业务日志

## Output Format

请按以下格式输出：

1. 总体结论（通过 / 部分需修改 / 必须修改）
2. 必须修改的问题（按 文件:行号 列出）
3. 建议优化的问题
4. 潜在风险（并发、错误处理、性能）
5. 测试与代码生成建议（`make wire` / `make buf` / `make test`）
6. 推荐 patch 或代码示例

## Checklist

- [ ] 符合 `.claude/context/go-coding-standards.md` 编码规范
- [ ] Repository 接口以 `I` 开头，方法按业务 ID 暴露
- [ ] 所有 IO/RPC/DB 函数接收 `context.Context`
- [ ] goroutine 走 `safe.SafelyGo`
- [ ] JSON 用 `sonic`
- [ ] 没有 `SELECT *` / 手写 SQL / 透传 `*gorm.DB`
- [ ] 没有 `fmt.Println` / `println` 残留
- [ ] GoDoc 中文注释完整且无句号结尾
- [ ] 新增 provider 已登记到 `provider.go` 且 `make wire` 已执行
- [ ] 修改 proto 后 `make buf` 已执行
- [ ] 单元测试覆盖主要分支
