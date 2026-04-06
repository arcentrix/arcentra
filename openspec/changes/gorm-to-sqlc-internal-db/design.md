## 上下文

- **当前状态**：`internal/dal`（数据访问层）已维护 `schema/arcentra.sql`、`internal/dal/sql/*.sql` 与 `sqlc generate` 产物（`internal/dal/queries`，Go 包名 `dal`），但 `internal/infra/persistence/*` 仍以 `database.IDatabase` → `*gorm.DB` 执行 CRUD；`sqlc.yaml` 对部分 JSON 列使用 `gorm.io/datatypes.JSON` 覆盖。
- **约束**：领域层仅依赖仓储接口；Wire 通过 `persistence.ProviderSet` 注入；`pkg/store/database` 负责多驱动、连接池与（现有）迁移入口；需保持分阶段可编译、可测试，与既有 DDD 分层一致。
- **利益相关者**：实现持久化与 CI 的开发者、需审查 SQL 的 DBA/Reviewer。

## 目标 / 非目标

**目标：**

- 仓储实现以 **sqlc 生成的 `Querier`/`Queries`** 为默认数据访问路径，`internal/dal/sql` 为查询与写入的单一事实来源（与 schema 一致）。
- `pkg/store/database` 对外提供 **`database/sql` 可用的 DBTX**（或 `*sql.DB`），使 `db.New(...)` / 事务与现有生命周期兼容。
- 领域映射仍通过显式 `ToDomain` / `FromDomain`（或等价的行模型→领域转换），不泄漏 sqlc 类型到 `internal/domain`。
- 分模块（按有界上下文）迁移，每步可通过 `go test`/`wire` 验证。

**非目标：**

- 一次性删除 GORM 或重写所有业务逻辑。
- 变更对外 HTTP/gRPC API 契约。
- 引入第二套迁移工具替代当前 schema/migrations 策略（仅在必要时调整文档路径）。

## 决策

1. **DB 门面扩展**
   - **选择**：在 `database` 包增加 `SQL() *sql.DB`（或返回满足 `dal.DBTX` 的句柄），从现有 GORM 底层 `*sql.DB` 取出；仓储构造函数注入 `*dal.Queries`（或由 `dal.New(sqlDB)` 工厂创建）。
   - **理由**：最小改动连接管理与配置；过渡期 GORM 仍可存在直至无引用。
   - **备选**：完全移除 GORM，仅用 `database/sql` 打开连接——工作量大，适合末阶段。

2. **事务边界**
   - **选择**：多语句原子操作使用 `*sql.Tx` + `Queries.WithTx`；与现有 `IDatabase` 并存，必要时在仓储内 `BeginTx`。
   - **理由**：与 sqlc 生成 API 一致。

3. **JSON / 特殊类型**
   - **选择**：将 `sqlc.yaml` 中 `gorm.io/datatypes.JSON` 逐步改为 `encoding/json.RawMessage` 或项目内小包装类型，避免生成代码依赖 GORM。
   - **理由**：切断 `internal/dal/queries` 对 GORM 的编译依赖。

4. **迁移顺序**
   - **选择**：按上下文逐个替换（建议从只读多、事务少的模块试点，或从已有 sql 文件覆盖较全的模块开始），每个 PR 内：补 SQL → `sqlc generate` → 改 Repo → 测试。
   - **理由**：与 `openspec/specs/migration-strategy` 的增量原则一致。

5. **遥测**
   - **选择**：GORM 插件式 trace 移除后，在 `DBTX` 层使用 `sql` 包装或保留对 `*sql.DB` 的 OpenTelemetry 包装（若已有）。
   - **理由**：避免观测退化；细节在实现阶段对齐 `pkg/telemetry/trace`。

## 风险 / 权衡

| 风险 | 缓解 |
|------|------|
| GORM 隐式行为（零值、关联预加载）与手写 SQL 不一致 | 每表迁移时对照现有用例与集成测试；对敏感路径加回归测试 |
| 双栈并存期依赖体积与混淆 | 文档标明「新代码禁止新增 GORM 查询」；lint/评审检查 |
| 大结果集与 N+1 原由 GORM 预加载掩盖 | SQL 中显式 JOIN 或分步查询并在设计评审中说明 |

## 迁移计划

1. 扩展 `IDatabase`（或并行接口）暴露 `*sql.DB`；Wire 提供 `*dal.Queries`。
2. 选 1～2 个仓储全量切换到 sqlc，验证事务与缓存组合（如 `UserRepo`）。
3. 其余 `internal/infra/persistence/**` 按上下文迁移；每批次 `sqlc generate` + CI。
4. 移除 GORM 专用 trace/适配器；删除 `gorm.io/gorm` 与未使用的 `datatypes`。
5. 更新主规范 `openspec/specs/infrastructure-implementations/spec.md`（归档本变更时）。

## 开放问题

- `IDatabase` 是扩展方法还是拆分为 `IGormDB`（弃用）与 `ISQLDatabase`（长期）更利于渐进迁移？
- 是否所有二进制（`arcentra`、`arcentra-agent`、CLI）在同一阶段统一 sqlc，还是允许 agent 滞后？
