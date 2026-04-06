## ADDED Requirements

### 需求:internal/dal 作为 SQL 与生成代码的唯一事实来源

所有由 Arcentra 控制的持久化 SQL（SELECT/INSERT/UPDATE/DELETE）必须定义在 `internal/dal/sql/` 下，并通过根目录 `sqlc.yaml` 生成到 `internal/dal/queries/`。禁止在 `internal/infra/persistence/` 中拼接业务相关的完整 SQL 字符串作为常规做法（调试或一次性脚本除外）。

#### 场景:变更表结构后的工作流

- **当** 修改或新增表结构并更新 `internal/dal/schema/`（或既定 migrations 源）
- **那么** 必须同步更新对应 `internal/dal/sql/*.sql` 中的命名查询并执行 `sqlc generate`，且生成代码必须纳入版本控制并通过 CI

### 需求:sqlc Querier 的依赖注入

应用程序必须通过 Wire（或项目统一的 DI 机制）注入 `*db.Queries` 或 `db.Querier`，其底层 `DBTX` 必须来自 `pkg/store/database` 所管理的同一连接池。仓储构造函数不得自行 `sql.Open` 绕过统一配置。

#### 场景:Wire 构建控制面应用

- **当** 执行 `wire` 生成 `cmd/arcentra` 依赖图
- **那么** 至少一个已迁移的仓储必须能够解析到 `*dal.Queries` 且与 `database` 包提供的 `*sql.DB`（或等效 DBTX）绑定

### 需求:事务与 sqlc 一致

需要原子性的多语句写入必须使用 `database/sql` 事务，并通过 sqlc 生成的 `WithTx`（或等价方式）在同一 `Tx` 上执行查询，禁止仅依赖隐式自动提交而声称原子性。

#### 场景:跨多表更新

- **当** 某用例必须同时更新两张或以上业务表且失败时回滚
- **那么** 仓储层必须使用 `BeginTx` + `Queries.WithTx`（或等效模式）完成全部语句
