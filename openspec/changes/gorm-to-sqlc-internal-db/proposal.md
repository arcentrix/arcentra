## 为什么

持久化层目前以 GORM 为运行时 ORM（`pkg/store/database` + `internal/infra/persistence/*` 中的 `Table`/`Create`/`Updates` 等），而 `internal/dal`（数据访问层）已具备 schema、手写 SQL 与 sqlc 生成物，两套路径并存导致类型与查询分散、迁移与代码审查成本高。将主数据访问统一到 sqlc（以 `internal/dal` 为单一事实来源）可在保持领域接口与 Wire 结构的前提下，获得显式 SQL、编译期校验与更可预测的 SQL 行为。

## 变更内容

- 以 `internal/dal`（`schema/`、`sql/`、`queries/`）为权威：新增/补齐与领域仓储对应的 sqlc 查询，仓储实现改为调用 `dal.Queries`/`Querier` + `database/sql`（或项目统一的 DB 门面暴露 `*sql.DB`/`DBTX`）。
- 逐步从 `internal/infra/persistence/<context>/` 中移除对 GORM 链式 API 的依赖；持久化模型可保留为纯 struct（仅列映射/JSON），或从 sqlc 生成模型映射到领域（`ToDomain`/`FromDomain`），**不再要求** GORM struct tag 作为查询前提。
- `sqlc.yaml` 中 JSON 等类型的 Go 映射：评估用标准库或项目内类型替代 `gorm.io/datatypes`，减少生成代码对 GORM 的依赖。
- `pkg/store/database`：在过渡期保留连接/迁移/多数据源能力；明确对外暴露供 sqlc 使用的 `DBTX`（或等价接口），并评估 GORM 仅用于尚存的少数路径直至完全移除。
- **BREAKING（对外行为）**：无计划变更 HTTP/gRPC 契约；若存在依赖 GORM 特有行为（隐式默认值、钩子、软删除等），需在迁移查询中显式表达，否则行为可能差异，需在分阶段迁移中逐表验证。

## 功能 (Capabilities)

### 新增功能

- `sqlc-db-access`: 定义 `internal/dal` 与运行时连接的组合方式、sqlc `Querier` 的 Wire 提供方式，以及仓储实现仅通过生成查询访问数据库的约束。

### 修改功能

- `infrastructure-implementations`: 更新「持久化模型与领域模型的映射」及仓储组织相关需求——数据访问以 sqlc 生成方法为主，持久化模型不再绑定「必须用 GORM 查询」；补充与 `pkg/store/database` 门面的集成场景。

## 影响

- **代码**：`internal/infra/persistence/**` 全部仓储文件、`pkg/store/database/**`、可选 `pkg/telemetry/trace/inject/gorm.go`、根目录 `sqlc.yaml`、`go.mod`（去除或缩减 GORM 依赖）。
- **流程**：每次修改查询需运行 `sqlc generate`；CI 建议校验生成物与 SQL 一致。
- **规范**：`openspec/specs/infrastructure-implementations/spec.md` 需增量更新；新增 `openspec/specs/sqlc-db-access/spec.md`（本变更的 specs 目录下落地为增量稿，归档时再合并入主 specs）。
