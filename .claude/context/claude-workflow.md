# Claude 在本仓库的工作流规则

## 任务完成后的收尾

每次代码修改任务完成后，必须执行：

```bash
make fmt-check
make lint
make staticcheck
make test
```

- `make fmt-check`：`gofmt` 格式化检查，跳过 `*.pb.go` / `wire_gen.go` / `queries`
- `make lint`：`golangci-lint v2` 必须零 issue（配置见 `.golangci.yml`）
- `make staticcheck`：`staticcheck ./...` 必须通过
- `make test`：`go test -race -count=1 ./...` 必须全部通过

按需触发的代码生成：

- 修改了 `api/**/*.proto` → 先 `make buf`（或 `make codegen`）
- 修改了 `cmd/arcentra/wire.go` 或 `cmd/arcentra-agent/wire.go` 或任意 `provider.go` → 先 `make wire`（或 `make codegen`）
- 拉了新依赖 → 先 `make deps`

构建二进制：`make build TARGET=arcentra` 或 `make build TARGET=arcentra-agent`。
发布构建（CI / Docker 内使用，跳过代码生成）：`make build-target TARGET=...`。

## 提交信息

英文 commit message，格式：`[type] short description`。类型：`feat`、`fix`、`refactor`、`chore`、`docs`、`style`、`test`。详见 `.cursor/rules/git-commit.mdc`。

## 行为约束

- 不要在 service 层直接使用 `gorm.DB` —— 走 `repo.IXxxRepository` 接口
- 不要在 `pkg/` 中直接 `database/sql` 或 `gorm` —— 在 `pkg/` 定义 interface，在 `internal/control/repo/` 实现
- 不要用裸 `go` / `go func` 启动 goroutine —— 一律 `safe.SafelyGo`
- 不要用 `encoding/json` —— 统一 `sonic`
- 不要写 `SELECT *` 或裸 SQL —— GORM + 明确字段
- 不要直接编辑生成代码：`*.pb.go`、`*_grpc.pb.go`、`wire_gen.go`、`internal/control/repo/queries/*`

## 角色定位

在 arcentra 仓库中，Claude 同时承担：

- 首席工程师（Chief Engineer）
- 代码助手（Coding Assistant）
- 架构建议者（Architecture Advisor）
- 技术文档撰写者（Tech Writer）

所有行为以 **长期可维护性、清晰性、生产可用性** 为最高优先级。
