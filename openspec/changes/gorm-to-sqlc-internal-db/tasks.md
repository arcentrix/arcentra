## 1. 数据库门面与 DI

- [ ] 1.1 在 `pkg/store/database` 暴露供 sqlc 使用的 `*sql.DB`（或满足 `internal/dal/queries`.DBTX 的句柄），并文档化与现有 `IDatabase.Database()` 的生命周期关系
- [ ] 1.2 新增 Wire 提供者：基于上述句柄构造 `*dal.Queries`，供 `internal/infra/persistence` 注入
- [ ] 1.3 运行 `wire` 校验 `cmd/arcentra`、`cmd/arcentra-agent` 等入口仍可生成

## 2. sqlc 与类型清理

- [ ] 2.1 审计 `sqlc.yaml` 中 `gorm.io/datatypes` 覆盖，逐项改为 `encoding/json.RawMessage` 或项目内 JSON 类型后执行 `sqlc generate`
- [ ] 2.2 确认 `go build ./...` 无对 `gorm.io/datatypes` 的仅生成代码依赖

## 3. 试点迁移（首个仓储全路径）

- [ ] 3.1 选择一个试点上下文（建议已有较全 `internal/dal/sql` 覆盖者），补全缺失的命名查询与 `:one`/`:many`/`:exec`
- [ ] 3.2 将该上下文下至少一个仓储改为仅使用 `*db.Queries`，保留缓存/事务行为与迁移前一致
- [ ] 3.3 为该上下文补充或更新单元测试（含事务与错误路径）

## 4. 全面迁移 persistence

- [ ] 4.1 按 `identity`、`project`、`pipeline`、`execution`、`agent` 等子包分批将 GORM 调用替换为 sqlc 调用
- [ ] 4.2 每批次执行 `sqlc generate`、`go test ./...`、关键集成测试
- [ ] 4.3 移除不再使用的 GORM PO 上的查询用 tag（或删除仅服务 GORM 的模型文件），统一映射层

## 5. 移除 GORM 运行时依赖

- [ ] 5.1 删除 `pkg/store/database` 中对 `gorm.io/gorm` 的打开/封装，改为纯 `database/sql`（或保留最小薄封装直至全仓库无 GORM）
- [ ] 5.2 更新或移除 `pkg/telemetry/trace/inject/gorm.go` 等 GORM 专用观测
- [ ] 5.3 清理 `go.mod` 中 GORM 相关 require，并全仓库 grep 确认无残留

## 6. 规范与 CI

- [ ] 6.1 将本变更下 `specs/` 增量合并入 `openspec/specs/`（在归档流程中执行）
- [ ] 6.2 （可选）在 CI 增加 `sqlc generate` 与 `git diff --exit-code internal/dal/queries` 一致性检查
